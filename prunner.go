package prunner

import (
	"context"
	stderrors "errors"
	"io"
	"net/http"
	"path"
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

// appAction is the main function which starts everything. This starts the HTTP server.
func appAction(c *cli.Context) error {
	conf, err := loadOrCreateConfig(c.String("config"))
	if err != nil {
		return err
	}

	tokenAuth := jwtauth.New("HS256", []byte(conf.JWTSecret), nil)

	// Load declared pipelines recursively

	defs, err := definition.LoadRecursively(filepath.Join(c.String("path"), c.String("pattern")))
	if err != nil {
		return errors.Wrap(err, "loading definitions")
	}

	log.
		WithField("component", "cli").
		WithField("pipelines", defs.Pipelines.NamesWithSourcePath()).
		Infof("Loaded %d pipeline definitions", len(defs.Pipelines))

	// TODO Handle signal USR1 for reloading config

	outputStore, err := taskctl.NewOutputStore(path.Join(c.String("data"), "logs"))
	if err != nil {
		return errors.Wrap(err, "building output store")
	}

	store, err := newJSONDataStore(path.Join(c.String("data")))
	if err != nil {
		return errors.Wrap(err, "building pipeline runner store")
	}

	// Set up pipeline runner
	pRunner, err := newPipelineRunner(c.Context, defs, func() taskctl.Runner {
		// taskctl.NewTaskRunner never actually returns an error
		taskRunner, _ := taskctl.NewTaskRunner(outputStore)

		// Do not output task stdout / stderr to the server process. NOTE: Before/After execution logs won't be visible because of this
		taskRunner.Stdout = io.Discard
		taskRunner.Stderr = io.Discard

		return taskRunner
	}, store)
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

// newPipelineRunner creates the central data structure which controls the full runner state; so this knows what is currently running
func newPipelineRunner(ctx context.Context, defs *definition.PipelinesDef, createTaskRunner func() taskctl.Runner, store dataStore) (*pipelineRunner, error) {
	pRunner := &pipelineRunner{
		defs: defs,
		// jobsByID contains ALL jobs, no matter whether they are on the waitlist or are scheduled or cancelled.
		jobsByID: make(map[uuid.UUID]*pipelineJob),
		// jobsByPipeline contains ALL jobs, no matter whether they are on the waitlist or are scheduled or cancelled.
		jobsByPipeline: make(map[string][]*pipelineJob),
		// waitListByPipeline additionally contains all the jobs currently waiting, but not yet started (because concurrency limits have been reached)
		waitListByPipeline: make(map[string][]*pipelineJob),
		store:              store,
		// Use channel buffered with one extra slot so we can keep save requests while a save is running without blocking
		persistRequests:  make(chan struct{}, 1),
		createTaskRunner: createTaskRunner,
	}

	if store != nil {
		err := pRunner.InitialLoadFromStore()
		if err != nil {
			return nil, errors.Wrap(err, "loading from store")
		}

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-pRunner.persistRequests:
					pRunner.SaveToStore()
					// Perform save at most every 3 seconds
					time.Sleep(3 * time.Second)
				}
			}
		}()
	}

	return pRunner, nil
}

// pipelineRunner is the main data structure which is basically a runtime state "singleton"
//
// All exported functions are synced with the mx mutex and are safe for concurrent use
type pipelineRunner struct {
	defs               *definition.PipelinesDef
	jobsByID           map[uuid.UUID]*pipelineJob
	jobsByPipeline     map[string][]*pipelineJob
	waitListByPipeline map[string][]*pipelineJob

	// store is the implementation for persisting data
	store dataStore
	// persistRequests is for triggering saving-the-store, which is then handled asynchronously, at most every 3 seconds (see newPipelineRunner)
	// externally, call requestPersist()
	persistRequests chan struct{}

	// Mutex for reading or writing jobs and job state
	mx               sync.RWMutex
	createTaskRunner func() taskctl.Runner
}

// pipelineJob is a single execution context (a single run of a single pipeline). Can be scheduled (in the waitListByPipeline of pipelineRunner),
// or currently running (jobsByID / jobsByPipeline in pipelineRunner)
type pipelineJob struct {
	ID        uuid.UUID
	Pipeline  string
	Variables map[string]interface{}

	Completed bool
	Canceled  bool
	// Created is the schedule / queue time of the job
	Created time.Time
	// Start is the actual start time of the job
	Start *time.Time
	// End is the actual end time of the job (can be nil if incomplete)
	End  *time.Time
	User string
	// Tasks is an in-memory representation with state of tasks, sorted by dependencies
	Tasks     jobTasks
	LastError error

	sched      *taskctl.Scheduler
	taskRunner runner.Runner
}

func (j *pipelineJob) isRunning() bool {
	return j.Start != nil && !j.Completed && !j.Canceled
}

func (r *pipelineRunner) initScheduler(j *pipelineJob) {
	// For correct cancellation of tasks a single task runner and scheduler per job is used

	taskRunner := r.createTaskRunner()

	sched := taskctl.NewScheduler(taskRunner)

	// Listen on task and stage changes for syncing the job / task state
	taskRunner.SetOnTaskChange(r.HandleTaskChange)
	sched.OnStageChange(r.HandleStageChange)

	j.taskRunner = taskRunner
	j.sched = sched
}

// deinitScheduler resets the scheduler and task runner for this job since they are no longer needed after a job is completed
func (j *pipelineJob) deinitScheduler() {
	j.sched.Finish()
	j.sched = nil
	j.taskRunner = nil
}

// jobTask is a single task invocation inside the pipelineJob
type jobTask struct {
	definition.TaskDef
	Name string

	Status   string
	Start    *time.Time
	End      *time.Time
	Skipped  bool
	ExitCode int16
	Errored  bool
	Error    error
}

type jobTasks []jobTask

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
var errJobNotFound = errors.New("job not found")
var errJobAlreadyCompleted = errors.New("job is already completed")
var errJobNotStarted = errors.New("job is not started")

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

	id, err := uuid.NewV4()
	if err != nil {
		return nil, errors.Wrap(err, "generating job UUID")
	}

	defer r.requestPersist()

	job := &pipelineJob{
		ID:        id,
		Pipeline:  pipeline,
		Created:   time.Now(),
		Tasks:     buildJobTasks(pipelineDef.Tasks),
		Variables: opts.Variables,
		User:      opts.User,
	}

	r.jobsByID[id] = job
	r.jobsByPipeline[pipeline] = append(r.jobsByPipeline[pipeline], job)

	switch action {
	case scheduleActionQueue:
		r.waitListByPipeline[pipeline] = append(r.waitListByPipeline[pipeline], job)

		log.
			WithField("component", "runner").
			WithField("pipeline", job.Pipeline).
			WithField("jobID", job.ID).
			WithField("variables", job.Variables).
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
			WithField("variables", job.Variables).
			Debugf("Queued: replaced job on wait list")

		return job, nil
	}

	r.startJob(job)

	log.
		WithField("component", "runner").
		WithField("pipeline", job.Pipeline).
		WithField("jobID", job.ID).
		WithField("variables", job.Variables).
		Debugf("Started: scheduled job execution")

	return job, nil
}

func buildJobTasks(tasks map[string]definition.TaskDef) (result jobTasks) {
	result = make(jobTasks, 0, len(tasks))

	for taskName, taskDef := range tasks {
		result = append(result, jobTask{
			TaskDef: taskDef,
			Name:    taskName,
			Status:  toStatus(scheduler.StatusWaiting),
		})
	}

	result.sortTasksByDependencies()

	return result
}

func buildPipelineGraph(id uuid.UUID, tasks jobTasks, vars map[string]interface{}) (*scheduler.ExecutionGraph, error) {
	var stages []*scheduler.Stage
	for _, taskDef := range tasks {
		t := task.FromCommands(taskDef.Script...)
		t.Name = taskDef.Name
		t.AllowFailure = taskDef.AllowFailure

		taskVariables := variables.FromMap(map[string]string{
			// Inject job id for later use in the task runner (see HandleStageChange and HandleTaskChange)
			taskctl.JobIDVariableName: id.String(),
		})

		for name, value := range vars {
			if name == taskctl.JobIDVariableName {
				return nil, errors.Errorf("variable name %s is reserved for internal use", taskctl.JobIDVariableName)
			}

			taskVariables.Set(name, value)
		}

		s := &scheduler.Stage{
			Name:         taskDef.Name,
			Task:         t,
			DependsOn:    taskDef.DependsOn,
			AllowFailure: taskDef.AllowFailure,
			Variables:    taskVariables,
		}

		stages = append(stages, s)
	}

	g, err := scheduler.NewExecutionGraph(stages...)
	if err != nil {
		return nil, errors.Wrap(err, "building execution graph")
	}

	return g, nil
}

func (r *pipelineRunner) FindJob(id uuid.UUID) *pipelineJob {
	r.mx.RLock()
	defer r.mx.RUnlock()

	return r.jobsByID[id]
}

func (r *pipelineRunner) startJob(job *pipelineJob) {
	defer r.requestPersist()

	r.initScheduler(job)

	graph, err := buildPipelineGraph(job.ID, job.Tasks, job.Variables)
	if err != nil {
		log.
			WithError(err).
			WithField("jobID", job.ID).
			WithField("pipeline", job.ID)

		job.LastError = err
		job.Canceled = true

		// A job was canceled, so there might be room for other jobs to start
		r.startJobsOnWaitList(job.Pipeline)

		return
	}

	// Actually start job
	now := time.Now()
	job.Start = &now

	// Run graph asynchronously
	go func() {
		lastErr := job.sched.Schedule(graph)
		r.JobCompleted(job.ID, lastErr)
	}()
}

// HandleTaskChange will be called when the task state changes in the task runner
func (r *pipelineRunner) HandleTaskChange(t *task.Task) {
	r.mx.Lock()
	defer r.mx.Unlock()

	jobIDString := t.Variables.Get(taskctl.JobIDVariableName).(string)
	jobID, _ := uuid.FromString(jobIDString)
	j, ok := r.jobsByID[jobID]
	if !ok {
		return
	}

	jt := j.Tasks.byName(t.Name)
	if jt == nil {
		return
	}
	if !t.Start.IsZero() {
		start := t.Start
		jt.Start = &start
	}
	if !t.End.IsZero() {
		end := t.End
		jt.End = &end
	}
	jt.Errored = t.Errored
	jt.Error = t.Error
	jt.ExitCode = t.ExitCode
	jt.Skipped = t.Skipped

	// Set canceled flag on the job if a task was canceled through the context
	if errors.Is(t.Error, context.Canceled) {
		j.Canceled = true
	}

	r.requestPersist()
}

// HandleStageChange will be called when the stage state changes in the scheduler
func (r *pipelineRunner) HandleStageChange(stage *scheduler.Stage) {
	r.mx.Lock()
	defer r.mx.Unlock()

	jobIDString := stage.Variables.Get(taskctl.JobIDVariableName).(string)
	jobID, _ := uuid.FromString(jobIDString)
	j, ok := r.jobsByID[jobID]
	if !ok {
		return
	}

	jt := j.Tasks.byName(stage.Name)
	if jt == nil {
		return
	}

	jt.Status = toStatus(stage.ReadStatus())

	r.requestPersist()
}

func (r *pipelineRunner) JobCompleted(id uuid.UUID, err error) {
	r.mx.Lock()
	defer r.mx.Unlock()

	job := r.jobsByID[id]
	if job == nil {
		return
	}

	job.deinitScheduler()

	job.Completed = true
	now := time.Now()
	job.End = &now
	job.LastError = err

	pipeline := job.Pipeline
	log.
		WithField("component", "runner").
		WithField("jobID", id).
		WithField("pipeline", pipeline).
		Debug("Job completed")

	// A job finished, so there might be room to start other jobs on the wait list
	r.startJobsOnWaitList(pipeline)

	r.requestPersist()
}

func (r *pipelineRunner) startJobsOnWaitList(pipeline string) {
	// Check wait list if another job is queued
	waitList := r.waitListByPipeline[pipeline]

	// Schedule as many jobs as are schedulable
	for len(waitList) > 0 && r.resolveScheduleAction(pipeline) == scheduleActionStart {
		queuedJob := waitList[0]
		waitList = waitList[1:]

		r.startJob(queuedJob)

		log.
			WithField("component", "runner").
			WithField("pipeline", queuedJob.Pipeline).
			WithField("jobID", queuedJob.ID).
			Debugf("Dequeue: scheduled job execution")
	}
	r.waitListByPipeline[pipeline] = waitList
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
		if job.isRunning() {
			return true
		}
	}
	return false
}

func (r *pipelineRunner) runningJobsCount(pipeline string) int {
	running := 0
	for _, job := range r.jobsByPipeline[pipeline] {
		if job.isRunning() {
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
	Variables map[string]interface{}
	User      string
}

type taskResult struct {
	Name     string     `json:"name"`
	Status   string     `json:"status"`
	Start    *time.Time `json:"start"`
	End      *time.Time `json:"end"`
	Skipped  bool       `json:"skipped"`
	ExitCode int16      `json:"exitCode"`
	Errored  bool       `json:"errored"`
	Error    *string    `json:"error"`
}

// TODO Move to server package
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
	LastError *string      `json:"lastError"`

	Variables map[string]interface{} `json:"variables"`
	User      string                 `json:"user"`
}

type pipelineResult struct {
	Pipeline    string `json:"pipeline"`
	Schedulable bool   `json:"schedulable"`
	Running     bool   `json:"running"`
}

func (r *pipelineRunner) jobToResult(j *pipelineJob) pipelineJobResult {
	var taskResults []taskResult

	errored := false
	for _, t := range j.Tasks {
		res := taskResult{
			Name:     t.Name,
			Status:   t.Status,
			Start:    t.Start,
			End:      t.End,
			Skipped:  t.Skipped,
			ExitCode: t.ExitCode,
			Errored:  t.Errored,
			Error:    errToStrPtr(t.Error),
		}
		taskResults = append(taskResults, res)
		// Collect if pipelines had a errored task
		// TODO Check if this works if AllowFailure is true!
		errored = errored || t.Errored
	}

	return pipelineJobResult{
		Tasks:     taskResults,
		ID:        j.ID,
		Pipeline:  j.Pipeline,
		Completed: j.Completed,
		Canceled:  j.Canceled,
		Errored:   errored,
		Created:   j.Created,
		Start:     j.Start,
		End:       j.End,
		LastError: errToStrPtr(j.LastError),

		Variables: j.Variables,
		User:      j.User,
	}
}

func (r *pipelineRunner) InitialLoadFromStore() error {
	log.
		WithField("component", "runner").
		Debug("Loading state from store")

	r.mx.Lock()
	defer r.mx.Unlock()

	data, err := r.store.Load()
	if err != nil {
		return errors.Wrap(err, "loading data")
	}

	for _, pJob := range data.Jobs {
		job := buildJobFromPersistedJob(pJob)

		// Cancel job with tasks if it appears to be still running (which it cannot if we initialize from the store)
		if job.isRunning() {
			for i := range job.Tasks {
				jt := &job.Tasks[i]
				if jt.Status == "waiting" || jt.Status == "running" {
					jt.Status = "canceled"
				}
			}

			// TODO Maybe add a new "Incomplete" flag?
			job.Canceled = true

			log.
				WithField("component", "runner").
				WithField("jobID", job.ID).
				WithField("pipeline", job.Pipeline).
				Warnf("Found running job when restoring state, marked as canceled")
		}

		// Cancel jobs which have been scheduled on wait list but never been started
		if job.Start == nil {
			job.Canceled = true

			log.
				WithField("component", "runner").
				WithField("jobID", job.ID).
				WithField("pipeline", job.Pipeline).
				Warnf("Found job on wait list when restoring state, marked as canceled")
		}

		r.jobsByID[pJob.ID] = job
		r.jobsByPipeline[pJob.Pipeline] = append(r.jobsByPipeline[pJob.Pipeline], job)
	}

	return nil
}

func (r *pipelineRunner) SaveToStore() {
	log.
		WithField("component", "runner").
		Debugf("Saving job state to data store")

	r.mx.RLock()
	data := &persistedData{
		Jobs: make([]persistedJob, 0, len(r.jobsByID)),
	}
	for _, job := range r.jobsByID {
		tasks := make([]persistedTask, len(job.Tasks))
		for i, t := range job.Tasks {
			tasks[i] = persistedTask{
				Name:         t.Name,
				Script:       t.Script,
				DependsOn:    t.DependsOn,
				AllowFailure: t.AllowFailure,
				Status:       t.Status,
				Start:        t.Start,
				End:          t.End,
				Skipped:      t.Skipped,
				ExitCode:     t.ExitCode,
				Errored:      t.Errored,
				Error:        errToStrPtr(t.Error),
			}
		}

		data.Jobs = append(data.Jobs, persistedJob{
			ID:        job.ID,
			Pipeline:  job.Pipeline,
			Completed: job.Completed,
			Canceled:  job.Canceled,
			Created:   job.Created,
			Start:     job.Start,
			End:       job.End,
			Tasks:     tasks,
			Variables: job.Variables,
			User:      job.User,
		})
	}
	r.mx.RUnlock()

	// We do not need to lock here, the single save loops guarantees non-concurrent saves

	err := r.store.Save(data)
	if err != nil {
		log.
			WithField("component", "runner").
			WithError(err).
			Errorf("Error saving job state to data store")
	}
}

func (r *pipelineRunner) requestPersist() {
	// Debounce persist requests by not sending if the persist channel is already full (buffered with length 1)
	select {
	case r.persistRequests <- struct{}{}:
		// The default case prevents blocking when sending to a full channel
	default:
	}
}

func (r *pipelineRunner) CancelJob(id uuid.UUID) error {
	r.mx.Lock()

	job, ok := r.jobsByID[id]
	if !ok {
		r.mx.Unlock()
		return errJobNotFound
	}

	if job.Completed {
		r.mx.Unlock()
		return errJobAlreadyCompleted
	}

	if job.Start == nil {
		r.mx.Unlock()
		return errJobNotStarted
	}

	// Unlock mutext before calling cancel to prevent deadlocks from state updates
	r.mx.Unlock()

	// SAFEGUARD: it could happen that a job is cancelled which has never been scheduled.
	// thus, we need to check for the existance of the job scheduler before cancelling.
	if job.sched != nil {
		job.sched.Cancel()
	}

	return nil
}

func buildJobFromPersistedJob(pJob persistedJob) *pipelineJob {
	job := &pipelineJob{
		ID:        pJob.ID,
		Pipeline:  pJob.Pipeline,
		Completed: pJob.Completed,
		Canceled:  pJob.Canceled,
		Created:   pJob.Created,
		Start:     pJob.Start,
		End:       pJob.End,
		Variables: pJob.Variables,
		User:      pJob.User,
	}

	tasks := make(jobTasks, len(pJob.Tasks))
	for i, pJobTask := range pJob.Tasks {
		tasks[i] = jobTask{
			Name: pJobTask.Name,
			TaskDef: definition.TaskDef{
				Script:       pJobTask.Script,
				DependsOn:    pJobTask.DependsOn,
				AllowFailure: pJobTask.AllowFailure,
			},
			Status:   pJobTask.Status,
			Start:    pJobTask.Start,
			End:      pJobTask.End,
			Skipped:  pJobTask.Skipped,
			ExitCode: pJobTask.ExitCode,
			Errored:  pJobTask.Errored,
			Error:    strPtrToErr(pJobTask.Error),
		}
	}
	job.Tasks = tasks

	return job
}

// sortTasksByDependencies is used only for the UI, to have a stable sorting
func (jt jobTasks) sortTasksByDependencies() {
	// Apply topological sorting (see https://en.wikipedia.org/wiki/Topological_sorting#Kahn's_algorithm)

	var queue []string

	type tmpNode struct {
		name     string
		incoming map[string]struct{}
		order    int
	}

	tmpNodes := make(map[string]*tmpNode)

	// Build temporary graph
	for _, n := range jt {
		inc := make(map[string]struct{})
		for _, from := range n.DependsOn {
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

	sort.Slice(jt, func(i, j int) bool {
		ri := tmpNodes[jt[i].Name].order
		rj := tmpNodes[jt[j].Name].order
		// For same rank order by name
		if ri == rj {
			return jt[i].Name < jt[j].Name
		}
		// Otherwise order by rank
		return ri < rj
	})
}

func (jt jobTasks) byName(name string) *jobTask {
	for i := range jt {
		if jt[i].Name == name {
			return &jt[i]
		}
	}
	return nil
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

func errToStrPtr(err error) *string {
	if err != nil {
		s := err.Error()
		return &s
	}
	return nil
}

func strPtrToErr(s *string) error {
	if s == nil || *s == "" {
		return nil
	}
	return stderrors.New(*s)
}
