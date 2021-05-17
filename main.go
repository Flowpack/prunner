package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/apex/log/handlers/text"
	"github.com/friendsofgo/errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gofrs/uuid"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"
	"github.com/taskctl/taskctl/pkg/task"
	"networkteam.com/lab/prunner/definition"
)

type scheduleInput struct {
	Pipeline string
}

func main() {
	path := "./examples"
	pattern := "**/pipelines.{yml,yaml}"

	log.SetLevel(log.DebugLevel)
	// TODO Use different handler when not running in TTY
	log.SetHandler(text.New(os.Stderr))

	// TODO Generate secret/JWT on first start (if not present / configured) and output on CLI
	// TODO Add JWT auth middleware

	// Load declared pipelines

	defs, err := definition.LoadRecursively(filepath.Join(path, pattern))
	failErr(err)

	// TODO Reload pipelines on file changes

	// Set up pipeline runner
	pRunner, err := newPipelineRunner()
	failErr(err)
	pRunner.defs = defs

	h := handler{
		pRunner: pRunner,
	}

	// Set up a simple REST API for listing jobs and scheduling pipelines

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Route("/pipelines", func(r chi.Router) {
		r.Get("/", h.pipelines)
		r.Get("/jobs", h.pipelinesJobs)
		r.Post("/schedule", h.pipelinesSchedule)
	})

	address := ":9009"
	log.Infof("HTTP API Listening on %s", address)
	err = http.ListenAndServe(address, r)
	failErr(err)
}

func newPipelineRunner() (*pipelineRunner, error) {
	taskRunner, err := runner.NewTaskRunner()
	if err != nil {
		return nil, errors.Wrap(err, "building task runner")
	}
	taskRunner.Stdout = io.Discard
	taskRunner.Stderr = io.Discard
	sched := scheduler.NewScheduler(taskRunner)

	return &pipelineRunner{
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

	graph *scheduler.ExecutionGraph
}

func (r *pipelineRunner) ScheduleAsync(pipeline string) (*pipelineJob, error) {
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
		graph:    g,
	}

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

	log.Debugf("Job %s completed", id)

	// TODO Persist job results and remove from in-memory map
}

func (r *pipelineRunner) ListJobs() []pipelineJobResult {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []pipelineJobResult{}

	for _, pJob := range r.jobs {
		jobRes := graphToPipelineJobResult(pJob.graph)
		jobRes.ID = pJob.ID
		jobRes.Pipeline = pJob.Pipeline
		jobRes.Completed = pJob.Completed
		jobRes.Start = pJob.Start
		jobRes.End = pJob.End
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

func (h *handler) pipelinesSchedule(w http.ResponseWriter, r *http.Request) {
	var in scheduleInput
	err := json.NewDecoder(r.Body).Decode(&in)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Error decoding JSON: %v", err)
		return
	}

	log.Infof("Scheduling pipeline %s", in.Pipeline)

	pJob, err := h.pRunner.ScheduleAsync(in.Pipeline)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "Error scheduling pipeline: %v", err)
		return
	}

	log.Debugf("Job %s scheduled", pJob.ID)

	w.WriteHeader(http.StatusAccepted)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		JobID string `json:"jobId"`
	}{JobID: pJob.ID.String()})
}

func (h *handler) pipelinesJobs(w http.ResponseWriter, r *http.Request) {
	res := h.pRunner.ListJobs()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

func (h *handler) pipelines(w http.ResponseWriter, r *http.Request) {
	res := h.pRunner.ListPipelines()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

type taskResult struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Skipped   bool      `json:"skipped"`
	ExitCode  int16     `json:"exitCode"`
	Errored   bool      `json:"errored"`
	Error     *string   `json:"error"`
	Stdout    string    `json:"stdout"`
	Stderr    string    `json:"stderr"`
	EdgesFrom []string  `json:"edgesFrom"`
	EdgesTo   []string  `json:"edgesTo"`
}

type pipelineJobResult struct {
	ID        uuid.UUID    `json:"id"`
	Pipeline  string       `json:"pipeline"`
	Tasks     []taskResult `json:"tasks"`
	Completed bool         `json:"completed"`
	Start     time.Time    `json:"start"`
	End       time.Time    `json:"end"`
}

type pipelineResult struct {
	Pipeline   string `json:"pipeline"`
	Concurrent bool   `json:"concurrent"`
	Running    bool   `json:"running"`
}

func graphToPipelineJobResult(g *scheduler.ExecutionGraph) pipelineJobResult {
	var taskResults []taskResult

	for _, stage := range g.Nodes() {
		res := taskResult{
			Name:      stage.Name,
			Status:    toStatus(stage.Status),
			Start:     stage.Start,
			End:       stage.End,
			EdgesFrom: g.To(stage.Name),
			EdgesTo:   g.From(stage.Name),
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

	// TODO Order by rank in graph first, then by name
	sort.Slice(taskResults, func(i, j int) bool {
		return taskResults[i].Name < taskResults[j].Name
	})

	return pipelineJobResult{Tasks: taskResults}
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

func failErr(err error) {
	if err != nil {
		log.WithError(err).Errorf("Fatal error occurred")
		os.Exit(1)
	}
}
