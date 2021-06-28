package prunner

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/friendsofgo/errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/urfave/cli/v2"
	"networkteam.com/lab/prunner/definition"
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

	// Set up pipeline runner
	pRunner, err := newPipelineRunner(defs)
	if err != nil {
		return err
	}

	h := handler{
		pRunner: pRunner,
	}

	// Set up a simple REST API for listing jobs and scheduling pipelines

	r := chi.NewRouter()
	r.Use(newHttpLogger(c))
	r.Use(middleware.Recoverer)
	// Seek, verify and validate JWT tokens
	r.Use(jwtauth.Verifier(tokenAuth))
	// Handle valid / invalid tokens
	r.Use(jwtauth.Authenticator)

	r.Route("/pipelines", func(r chi.Router) {
		r.Get("/", h.pipelines)
		r.Get("/jobs", h.pipelinesJobs)
		r.Post("/schedule", h.pipelinesSchedule)
	})

	log.
		WithField("component", "cli").
		Infof("HTTP API Listening on %s", c.String("address"))
	return http.ListenAndServe(c.String("address"), r)
}

func newPipelineRunner(defs *definition.PipelinesDef) (*pipelineRunner, error) {
	taskRunner, err := runner.NewTaskRunner()
	if err != nil {
		return nil, errors.Wrap(err, "building task runner")
	}
	taskRunner.Stdout = io.Discard
	taskRunner.Stderr = io.Discard
	sched := scheduler.NewScheduler(taskRunner)

	return &pipelineRunner{
		defs:  defs,
		sched: sched,
		jobs:  make(map[uuid.UUID]*pipelineJob),
	}, nil
}

type pipelineRunner struct {
	sched *scheduler.Scheduler
	defs  *definition.PipelinesDef
	jobs  map[uuid.UUID]*pipelineJob

	mx sync.RWMutex
}

type pipelineJob struct {
	ID        uuid.UUID
	Pipeline  string
	Completed bool
	Start     time.Time
	End       time.Time
	User      string

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

func (r *pipelineRunner) ScheduleAsync(pipeline string, opts ScheduleOpts) (*pipelineJob, error) {
	r.mx.Lock()
	defer r.mx.Unlock()

	pipelineDef, ok := r.defs.Pipelines[pipeline]
	if !ok {
		return nil, errors.Errorf("pipeline %q is not defined", pipeline)
	}

	// If pipeline does not allow concurrent runs: check if same pipeline is not running already
	if !pipelineDef.Concurrent {
		for _, job := range r.jobs {
			if !job.Completed && job.Pipeline == pipeline {
				return nil, errors.Errorf("pipeline %q is already running and not marked as concurrent", pipeline)
			}
		}
	}

	var stages []*scheduler.Stage

	for taskName, taskDef := range pipelineDef.Tasks {
		t := task.FromCommands(taskDef.Script...)
		t.Name = taskName
		t.AllowFailure = taskDef.AllowFailure

		s := &scheduler.Stage{
			Name:         taskName,
			Task:         t,
			DependsOn:    taskDef.DependsOn,
			AllowFailure: taskDef.AllowFailure,
		}

		stages = append(stages, s)
	}

	g, err := scheduler.NewExecutionGraph(stages...)
	if err != nil {
		return nil, errors.Wrap(err, "building execution graph")
	}

	id, err := uuid.NewV4()
	if err != nil {
		return nil, errors.Wrap(err, "generating job UUID")
	}
	job := &pipelineJob{
		ID:       id,
		Pipeline: pipeline,
		Start:    time.Now(),
		User:     opts.User,
		graph:    g,
	}
	job.calculateTaskOrder()

	r.jobs[id] = job

	// TODO Add possibility to cancel a running job (e.g. inject cancelable context in task nodes?)
	// Run graph asynchronously
	go func() {
		_ = r.sched.Schedule(g)
		r.jobCompleted(job.ID)
	}()

	return job, nil
}

func (r *pipelineRunner) jobCompleted(id uuid.UUID) {
	r.mx.Lock()
	defer r.mx.Unlock()

	job := r.jobs[id]
	if job == nil {
		return
	}
	job.Completed = true
	job.End = time.Now()

	log.
		WithField("component", "runner").
		WithField("jobID", id).
		WithField("pipeline", job.Pipeline).
		Debug("Job completed")

	// TODO Persist job results and remove from in-memory map
}

func (r *pipelineRunner) ListJobs() []pipelineJobResult {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []pipelineJobResult{}

	for _, pJob := range r.jobs {
		jobRes := jobToResult(pJob)
		res = append(res, jobRes)
	}

	sort.Slice(res, func(i, j int) bool {
		return !res[i].Start.Before(res[j].Start)
	})

	return res
}

func (r *pipelineRunner) ListPipelines() []pipelineResult {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []pipelineResult{}

	for pipeline, pipelineDef := range r.defs.Pipelines {
		running := r.isRunning(pipeline)

		res = append(res, pipelineResult{
			Pipeline:   pipeline,
			Concurrent: pipelineDef.Concurrent,
			Running:    running,
		})
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].Pipeline < res[j].Pipeline
	})

	return res
}

func (r *pipelineRunner) isRunning(pipeline string) bool {
	for _, job := range r.jobs {
		if !job.Completed && job.Pipeline == pipeline {
			return true
		}
	}
	return false
}

type handler struct {
	pRunner *pipelineRunner
}

type ScheduleOpts struct {
	User string
}

func (h *handler) pipelinesSchedule(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	var user string
	if sub, ok := claims["sub"].(string); ok {
		user = sub
	}

	var in scheduleInput
	err := json.NewDecoder(r.Body).Decode(&in)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error decoding JSON: %v", err))
		return
	}

	pJob, err := h.pRunner.ScheduleAsync(in.Pipeline, ScheduleOpts{User: user})
	if err != nil {
		h.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error scheduling pipeline: %v", err))
		return
	}

	log.
		WithField("component", "api").
		WithField("jobID", pJob.ID).
		WithField("pipeline", in.Pipeline).
		WithField("user", user).
		Info("Job scheduled")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	_ = json.NewEncoder(w).Encode(struct {
		JobID string `json:"jobId"`
	}{
		JobID: pJob.ID.String(),
	})
}

func (h *handler) sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}

type pipelinesJobsResponse struct {
	Pipelines []pipelineResult    `json:"pipelines"`
	Jobs      []pipelineJobResult `json:"jobs"`
}

func (h *handler) pipelinesJobs(w http.ResponseWriter, r *http.Request) {
	pipelinesRes := h.pRunner.ListPipelines()
	jobsRes := h.pRunner.ListJobs()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pipelinesJobsResponse{
		Pipelines: pipelinesRes,
		Jobs:      jobsRes,
	})
}

type pipelinesResponse struct {
	Pipelines []pipelineResult `json:"pipelines"`
}

func (h *handler) pipelines(w http.ResponseWriter, r *http.Request) {
	res := h.pRunner.ListPipelines()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pipelinesResponse{
		Pipelines: res,
	})
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
	Stdout   string    `json:"stdout"`
	Stderr   string    `json:"stderr"`
}

type pipelineJobResult struct {
	ID        uuid.UUID    `json:"id"`
	Pipeline  string       `json:"pipeline"`
	Tasks     []taskResult `json:"tasks"`
	Completed bool         `json:"completed"`
	Errored   bool         `json:"errored"`
	Start     time.Time    `json:"start"`
	End       time.Time    `json:"end"`
	User      string       `json:"user"`
}

type pipelineResult struct {
	Pipeline   string `json:"pipeline"`
	Concurrent bool   `json:"concurrent"`
	Running    bool   `json:"running"`
}

func jobToResult(j *pipelineJob) pipelineJobResult {
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
			res.Stdout = t.Log.Stdout.String()
			res.Stderr = t.Log.Stderr.String()
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
		Errored:   j.graph.LastError() != nil,
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
