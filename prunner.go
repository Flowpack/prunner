package prunner

import (
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/friendsofgo/errors"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"
	"github.com/urfave/cli/v2"
	"networkteam.com/lab/prunner/definition"
	"networkteam.com/lab/prunner/taskctl"
)

type scheduleInput struct {
	Pipeline string
}

func newDebugCmd() *cli.Command {
	return &cli.Command{
		Name:  "debug",
		Usage: "Get authorization information for debugging",
		Action: func(c *cli.Context) error {
			conf, err := loadOrCreateConfig(c.String("config"))
			if err != nil {
				return err
			}

			tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

			claims := make(map[string]interface{})
			jwtauth.SetIssuedNow(claims)
			_, tokenString, _ := tokenAuth.Encode(claims)
			log.Infof("Send the following HTTP header for JWT authorization:\n    Authorization: Bearer %s", tokenString)

			return nil
		},
	}
}

func run(c *cli.Context) error {
	conf, err := loadOrCreateConfig(c.String("config"))
	if err != nil {
		return err
	}

	tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

	// Load declared pipelines

	defs, err := definition.LoadRecursively(filepath.Join(c.String("path"), c.String("pattern")))
	if err != nil {
		return errors.Wrap(err, "loading definitions")
	}

	log.
		WithField("component", "cli").
		WithField("pipelines", defs.Pipelines.NamesWithSourcePath()).
		Infof("Loaded %d pipeline definitions", len(defs.Pipelines))

	// TODO Reload pipelines on file changes

	outputStore, err := taskctl.NewOutputStore(".prunner")
	if err != nil {
		return errors.Wrap(err, "building output store")
	}

	taskRunner, err := taskctl.NewTaskRunner(outputStore)
	if err != nil {
		return errors.Wrap(err, "building task runner")
	}
	taskRunner.Stdout = io.Discard
	taskRunner.Stderr = io.Discard

	// Set up pipeline runner
	pRunner, err := newPipelineRunner(defs, taskRunner)
	if err != nil {
		return err
	}

	srv := newServer(
		pRunner,
		outputStore,
		newHttpLogger(c),
		tokenAuth,
	)

	// Set up a simple REST API for listing jobs and scheduling pipelines

	log.
		WithField("component", "cli").
		Infof("HTTP API Listening on %s", c.String("address"))
	return http.ListenAndServe(c.String("address"), srv)
}

func newPipelineRunner(defs *definition.PipelinesDef, taskRunner runner.Runner) (*pipelineRunner, error) {
	sched := scheduler.NewScheduler(taskRunner)

	return &pipelineRunner{
		defs:               defs,
		sched:              sched,
		taskRunner:         taskRunner,
		jobsByID:           make(map[uuid.UUID]*pipelineJob),
		jobsByPipeline:     make(map[string][]*pipelineJob),
		waitListByPipeline: make(map[string][]*pipelineJob),
	}, nil
}

type pipelineRunner struct {
	sched              *scheduler.Scheduler
	taskRunner         runner.Runner
	defs               *definition.PipelinesDef
	jobsByID           map[uuid.UUID]*pipelineJob
	jobsByPipeline     map[string][]*pipelineJob
	waitListByPipeline map[string][]*pipelineJob

	mx sync.RWMutex
}

type pipelineJob struct {
	ID        uuid.UUID
	Pipeline  string
	Completed bool
	Canceled  bool
	// Created is the schedule / queue time of the job
	Created time.Time
	// Start is the actual start time of the job
	Start *time.Time
	// End is the actual end time of the job (can be nil if incomplete)
	End  *time.Time
	User string

	graph *scheduler.ExecutionGraph
	// order of tasks sorted 1. by topology (rank in DAG) and 2. by name ascending
	taskOrder map[string]int
}

func (j *pipelineJob) calculateTaskOrder() {
	var nodes []taskNode
	for _, stage := range j.graph.Nodes() {
		nodes = append(nodes, taskNode{
			Name:      stage.Name,
			EdgesFrom: stage.DependsOn,
		})
	}

	sortTaskNodesTopological(nodes)

	taskOrder := make(map[string]int)
	for i, v := range nodes {
		taskOrder[v.Name] = i
	}

	j.taskOrder = taskOrder
}

type scheduleAction int

const (
	scheduleActionStart scheduleAction = iota
	scheduleActionQueue
	scheduleActionReplace
	scheduleActionNoQueue
	scheduleActionQueueFull
)

var errNoQueue = errors.New("concurrency exceeded and queueing disabled for pipeline")
var errQueueFull = errors.New("concurrency exceeded and queue limit reached for pipeline")

func (r *pipelineRunner) ScheduleAsync(pipeline string, opts ScheduleOpts) (*pipelineJob, error) {
	r.mx.Lock()
	defer r.mx.Unlock()

	pipelineDef, ok := r.defs.Pipelines[pipeline]
	if !ok {
		return nil, errors.Errorf("pipeline %q is not defined", pipeline)
	}

	action := r.resolveScheduleAction(pipeline)

	switch action {
	case scheduleActionNoQueue:
		return nil, errNoQueue
	case scheduleActionQueueFull:
		return nil, errQueueFull
	}

	var stages []*scheduler.Stage

	id, err := uuid.NewV4()
	if err != nil {
		return nil, errors.Wrap(err, "generating job UUID")
	}

	for taskName, taskDef := range pipelineDef.Tasks {
		t := task.FromCommands(taskDef.Script...)
		t.Name = taskName
		t.AllowFailure = taskDef.AllowFailure

		s := &scheduler.Stage{
			Name:         taskName,
			Task:         t,
			DependsOn:    taskDef.DependsOn,
			AllowFailure: taskDef.AllowFailure,
			Variables: variables.FromMap(map[string]string{
				// Inject job id for later use in the task runner
				"jobID": id.String(),
			}),
		}

		stages = append(stages, s)
	}

	g, err := scheduler.NewExecutionGraph(stages...)
	if err != nil {
		return nil, errors.Wrap(err, "building execution graph")
	}

	job := &pipelineJob{
		ID:       id,
		Pipeline: pipeline,
		Created:  time.Now(),
		User:     opts.User,
		graph:    g,
	}
	job.calculateTaskOrder()

	r.jobsByID[id] = job
	r.jobsByPipeline[pipeline] = append(r.jobsByPipeline[pipeline], job)

	switch action {
	case scheduleActionQueue:
		r.waitListByPipeline[pipeline] = append(r.waitListByPipeline[pipeline], job)

		log.
			WithField("component", "runner").
			WithField("pipeline", job.Pipeline).
			WithField("jobID", job.ID).
			Debugf("Queued: added job to wait list")

		return job, nil
	case scheduleActionReplace:
		waitList := r.waitListByPipeline[pipeline]
		previousJob := waitList[len(waitList)-1]
		previousJob.Canceled = true
		waitList[len(waitList)-1] = job

		log.
			WithField("component", "runner").
			WithField("pipeline", job.Pipeline).
			WithField("jobID", job.ID).
			Debugf("Queued: replaced job on wait list")

		return job, nil
	}

	// TODO Add possibility to cancel a running job (e.g. inject cancelable context in task nodes?)

	r.startJob(job)

	log.
		WithField("component", "runner").
		WithField("pipeline", job.Pipeline).
		WithField("jobID", job.ID).
		Debugf("Started: scheduled job execution")

	return job, nil
}

func (r *pipelineRunner) findJob(id uuid.UUID) *pipelineJob {
	r.mx.RLock()
	defer r.mx.RUnlock()

	return r.jobsByID[id]
}

func (r *pipelineRunner) startJob(job *pipelineJob) {
	// Actually start job
	now := time.Now()
	job.Start = &now

	// Run graph asynchronously
	go func() {
		_ = r.sched.Schedule(job.graph)
		r.jobCompleted(job.ID)
	}()
}

func (r *pipelineRunner) jobCompleted(id uuid.UUID) {
	r.mx.Lock()
	defer r.mx.Unlock()

	job := r.jobsByID[id]
	if job == nil {
		return
	}
	job.Completed = true
	now := time.Now()
	job.End = &now

	log.
		WithField("component", "runner").
		WithField("jobID", id).
		WithField("pipeline", job.Pipeline).
		Debug("Job completed")

	// Check wait list if another job is queued
	waitList := r.waitListByPipeline[job.Pipeline]
	// Schedule as many jobs as are schedulable
	for len(waitList) > 0 && r.resolveScheduleAction(job.Pipeline) == scheduleActionStart {
		queuedJob := waitList[0]
		waitList = waitList[1:]

		r.startJob(queuedJob)

		log.
			WithField("component", "runner").
			WithField("pipeline", queuedJob.Pipeline).
			WithField("jobID", queuedJob.ID).
			Debugf("Dequeue: scheduled job execution")
	}
	r.waitListByPipeline[job.Pipeline] = waitList

	// TODO Persist job results and remove from in-memory map
}

func (r *pipelineRunner) ListJobs() []pipelineJobResult {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []pipelineJobResult{}

	for _, pJob := range r.jobsByID {
		jobRes := r.jobToResult(pJob)
		res = append(res, jobRes)
	}

	sort.Slice(res, func(i, j int) bool {
		return !res[i].Created.Before(res[j].Created)
	})

	return res
}

func (r *pipelineRunner) ListPipelines() []pipelineResult {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []pipelineResult{}

	for pipeline := range r.defs.Pipelines {
		running := r.isRunning(pipeline)

		res = append(res, pipelineResult{
			Pipeline:    pipeline,
			Schedulable: r.isSchedulable(pipeline),
			Running:     running,
		})
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].Pipeline < res[j].Pipeline
	})

	return res
}

func (r *pipelineRunner) isRunning(pipeline string) bool {
	for _, job := range r.jobsByPipeline[pipeline] {
		if !job.Completed {
			return true
		}
	}
	return false
}

func (r *pipelineRunner) runningJobsCount(pipeline string) int {
	running := 0
	for _, job := range r.jobsByPipeline[pipeline] {
		if job.Start != nil && !job.Completed {
			running++
		}
	}
	return running
}

func (r *pipelineRunner) resolveScheduleAction(pipeline string) scheduleAction {
	pipelineDef := r.defs.Pipelines[pipeline]

	runningJobsCount := r.runningJobsCount(pipeline)
	if runningJobsCount >= pipelineDef.Concurrency {
		// Check if jobs should be queued if concurrency factor is exceeded
		if pipelineDef.QueueLimit != nil && *pipelineDef.QueueLimit == 0 {
			return scheduleActionNoQueue
		}

		// Check if a queued job on the wait list should be replaced depending on queue strategy
		waitList := r.waitListByPipeline[pipeline]
		if pipelineDef.QueueStrategy == definition.QueueStrategyReplace && len(waitList) > 0 {
			return scheduleActionReplace
		}

		// Error if there is a queue limit and the number of queued jobs exceeds the allowed queue limit
		if pipelineDef.QueueLimit != nil && len(waitList) >= *pipelineDef.QueueLimit {
			return scheduleActionQueueFull
		}

		return scheduleActionQueue
	}

	return scheduleActionStart
}

func (r *pipelineRunner) isSchedulable(pipeline string) bool {
	action := r.resolveScheduleAction(pipeline)
	switch action {
	case scheduleActionReplace:
		fallthrough
	case scheduleActionQueue:
		fallthrough
	case scheduleActionStart:
		return true
	}
	return false
}

type ScheduleOpts struct {
	User string
}

type taskResult struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Skipped  bool      `json:"skipped"`
	ExitCode int16     `json:"exitCode"`
	Errored  bool      `json:"errored"`
	Error    *string   `json:"error"`
}

type pipelineJobResult struct {
	ID        uuid.UUID    `json:"id"`
	Pipeline  string       `json:"pipeline"`
	Tasks     []taskResult `json:"tasks"`
	Completed bool         `json:"completed"`
	Canceled  bool         `json:"canceled"`
	Errored   bool         `json:"errored"`
	Created   time.Time    `json:"created"`
	Start     *time.Time   `json:"start"`
	End       *time.Time   `json:"end"`
	User      string       `json:"user"`
}

type pipelineResult struct {
	Pipeline    string `json:"pipeline"`
	Schedulable bool   `json:"schedulable"`
	Running     bool   `json:"running"`
}

func (r *pipelineRunner) jobToResult(j *pipelineJob) pipelineJobResult {
	var taskResults []taskResult

	for _, stage := range j.graph.Nodes() {
		res := taskResult{
			Name:   stage.Name,
			Status: toStatus(stage.ReadStatus()),
			Start:  stage.Start,
			End:    stage.End,
		}

		t := stage.Task
		if t != nil {
			res.Start = t.Start
			res.End = t.End
			res.Skipped = t.Skipped
			res.ExitCode = t.ExitCode
			res.Errored = t.Errored
			if t.Error != nil {
				s := t.Error.Error()
				res.Error = &s
			}
		}
		taskResults = append(taskResults, res)
	}

	sort.Slice(taskResults, func(x, y int) bool {
		return j.taskOrder[taskResults[x].Name] < j.taskOrder[taskResults[y].Name]
	})

	return pipelineJobResult{
		Tasks:     taskResults,
		ID:        j.ID,
		Pipeline:  j.Pipeline,
		Completed: j.Completed,
		Canceled:  j.Canceled,
		Errored:   j.graph.LastError() != nil,
		Created:   j.Created,
		Start:     j.Start,
		End:       j.End,
		User:      j.User,
	}
}

type taskNode struct {
	Name      string
	EdgesFrom []string
}

func sortTaskNodesTopological(nodes []taskNode) {
	// Apply topological sorting (see https://en.wikipedia.org/wiki/Topological_sorting#Kahn's_algorithm)

	var queue []string

	type tmpNode struct {
		name     string
		incoming map[string]struct{}
		order    int
	}

	tmpNodes := make(map[string]*tmpNode)

	// Build temporary graph
	for _, n := range nodes {
		inc := make(map[string]struct{})
		for _, from := range n.EdgesFrom {
			inc[from] = struct{}{}
		}
		tmpNodes[n.Name] = &tmpNode{
			name:     n.Name,
			incoming: inc,
		}
		if len(inc) == 0 {
			queue = append(queue, n.Name)
		}
	}
	// Make sure a stable sorting is used for the traversal of nodes (map has no defined order)
	sort.Strings(queue)

	i := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		tmpNodes[n].order = i
		i++

		for _, m := range tmpNodes {
			if _, exist := m.incoming[n]; exist {
				delete(m.incoming, n)

				if len(m.incoming) == 0 {
					queue = append(queue, m.name)
				}
			}
		}
		sort.Strings(queue)
	}

	sort.Slice(nodes, func(i, j int) bool {
		ri := tmpNodes[nodes[i].Name].order
		rj := tmpNodes[nodes[j].Name].order
		// For same rank order by name
		if ri == rj {
			return nodes[i].Name < nodes[j].Name
		}
		return ri < rj
	})
}

func toStatus(status int32) string {
	switch status {
	case scheduler.StatusWaiting:
		return "waiting"
	case scheduler.StatusRunning:
		return "running"
	case scheduler.StatusSkipped:
		return "skipped"
	case scheduler.StatusDone:
		return "done"
	case scheduler.StatusError:
		return "error"
	case scheduler.StatusCanceled:
		return "canceled"
	}
	return ""
}
