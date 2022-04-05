package server

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"time"

	"github.com/apex/log"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	jsontime "github.com/liamylian/jsontime/v2/v2"

	"github.com/Flowpack/prunner"
	"github.com/Flowpack/prunner/helper"
	"github.com/Flowpack/prunner/taskctl"
)

var json = jsontime.ConfigWithCustomTimeFormat

func init() {
	// Use RFC3339 format without nanoseconds for compatibility with json_decode in PHP
	jsontime.SetDefaultTimeFormat(time.RFC3339, time.UTC)
}

type server struct {
	pRunner     *prunner.PipelineRunner
	handler     http.Handler
	outputStore taskctl.OutputStore
}

func NewServer(pRunner *prunner.PipelineRunner, outputStore taskctl.OutputStore, logger func(http.Handler) http.Handler, tokenAuth *jwtauth.JWTAuth) *server {
	srv := &server{
		pRunner:     pRunner,
		outputStore: outputStore,
	}

	r := chi.NewRouter()
	r.Use(logger)
	r.Use(middleware.Recoverer)
	// Seek, verify and validate JWT tokens
	r.Use(jwtauth.Verifier(tokenAuth))
	// Handle valid / invalid tokens
	r.Use(jwtauth.Authenticator)

	r.Route("/pipelines", func(r chi.Router) {
		r.Get("/", srv.pipelines)
		r.Get("/jobs", srv.pipelinesJobs)
		r.Post("/schedule", srv.pipelinesSchedule)
	})
	r.Route("/job", func(r chi.Router) {
		r.Get("/detail", srv.jobDetail)
		r.Get("/logs", srv.jobLogs)
		r.Post("/cancel", srv.jobCancel)
	})

	srv.handler = r

	return srv
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

var _ http.Handler = &server{}

// swagger:parameters pipelinesSchedule
type pipelinesScheduleRequest struct {
	// in: body
	Body struct {
		// Pipeline name
		// required: true
		// example: my_pipeline
		Pipeline string `json:"pipeline"`

		// Job variables
		// example: {"tag_name": "v1.17.4", "databases": ["mysql", "postgresql"]}
		Variables map[string]interface{} `json:"variables"`
	}
}

// swagger:response
type pipelinesScheduleResponse struct {
	// in: body
	Body struct {
		// Id of the scheduled job
		//
		// swagger:strfmt uuid4
		// example: 52a5cb79-7556-4c52-8e6f-dd6aaf1bc4c8
		JobID string `json:"jobId"`
	}
}

// swagger:route POST /pipelines/schedule pipelinesSchedule
//
// Schedule a pipeline execution
//
// This will create a job for execution of the specified pipeline and variables.
// If the pipeline is not schedulable (running and no queue / limit or concurrency exceeded) it will error.
//
//     Consumes:
//     - application/json
//
//     Produces:
//     - application/json
//
//     Responses:
//       default: pipelinesScheduleResponse
//       400: genericErrorResponse
func (s *server) pipelinesSchedule(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	var user string
	if sub, ok := claims["sub"].(string); ok {
		user = sub
	}

	var in pipelinesScheduleRequest
	err := json.NewDecoder(r.Body).Decode(&in.Body)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error decoding JSON: %v", err))
		return
	}

	pJob, err := s.pRunner.ScheduleAsync(in.Body.Pipeline, prunner.ScheduleOpts{Variables: in.Body.Variables, User: user})
	if err != nil {
		// TODO Send JSON error and include expected errors (see resolveScheduleAction)
		if errors.Is(err, prunner.ErrShuttingDown) {
			s.sendError(w, http.StatusServiceUnavailable, "Server is shutting down")
			return
		}

		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error scheduling pipeline: %v", err))
		return
	}

	log.
		WithField("component", "api").
		WithField("jobID", pJob.ID).
		WithField("pipeline", in.Body.Pipeline).
		WithField("user", user).
		Info("Job scheduled")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	var resp pipelinesScheduleResponse
	resp.Body.JobID = pJob.ID.String()

	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:response
type pipelinesJobsResponse struct {
	// in: body
	Body struct {
		Pipelines []pipelineResult    `json:"pipelines"`
		Jobs      []pipelineJobResult `json:"jobs"`
	}
}

// swagger:model task
type taskResult struct {
	// Task name
	// example: task_name
	Name string `json:"name"`
	// Task names this task depends on
	DependsOn []string `json:"dependsOn,omitempty"`
	// Status of task
	// enum: waiting,running,skipped,done,error,canceled
	Status string `json:"status"`
	// When the task was started
	Start *time.Time `json:"start,omitempty"`
	// When the task was finished
	End *time.Time `json:"end,omitempty"`
	// If the task was skipped
	Skipped bool `json:"skipped"`
	// Exit code of command
	ExitCode int16 `json:"exitCode"`
	// If the task had an error
	Errored bool `json:"errored"`
	// Error message of task when an error occured
	Error *string `json:"error,omitempty"`
}

// swagger:model job
type pipelineJobResult struct {
	// Job id
	// swagger:strfmt uuid4
	// example: 52a5cb79-7556-4c52-8e6f-dd6aaf1bc4c8
	ID string `json:"id"`
	// Pipeline name
	// example: my_pipeline
	Pipeline string `json:"pipeline"`
	// List of tasks in job (ordered topologically by dependencies and task name)
	Tasks []taskResult `json:"tasks"`
	// If the job is completed
	Completed bool `json:"completed"`
	// If the job was canceled
	Canceled bool `json:"canceled"`
	// If the job had an error
	Errored bool `json:"errored"`
	// When the job was created
	Created time.Time `json:"created"`
	// When the job was started
	Start *time.Time `json:"start,omitempty"`
	// When the job was finished
	End *time.Time `json:"end,omitempty"`
	// Error message of last task that had an error
	LastError *string `json:"lastError,omitempty"`

	// Assigned variables of job
	// example: {"tags": ["foo", "bar"]}
	Variables map[string]interface{} `json:"variables,omitempty"`
	// User that scheduled the job
	// example: j.doe
	User string `json:"user"`
}

func jobToResult(j *prunner.PipelineJob) pipelineJobResult {
	var taskResults []taskResult

	errored := false
	for _, t := range j.Tasks {
		res := taskResult{
			Name:      t.Name,
			DependsOn: t.DependsOn,
			Status:    t.Status,
			Start:     t.Start,
			End:       t.End,
			Skipped:   t.Skipped,
			ExitCode:  t.ExitCode,
			Errored:   t.Errored,
			Error:     helper.ErrToStrPtr(t.Error),
		}
		taskResults = append(taskResults, res)
		// Collect if job had a errored task
		// TODO Check if this works if AllowFailure is true!
		errored = errored || t.Errored
	}

	return pipelineJobResult{
		Tasks:     taskResults,
		ID:        j.ID.String(),
		Pipeline:  j.Pipeline,
		Completed: j.Completed,
		Canceled:  j.Canceled,
		Errored:   errored,
		Created:   j.Created,
		Start:     j.Start,
		End:       j.End,
		LastError: helper.ErrToStrPtr(j.LastError),

		Variables: j.Variables,
		User:      j.User,
	}
}

// swagger:route GET /pipelines/jobs pipelinesJobs
//
// Get pipelines and jobs
//
// This is a combined operation to fetch pipelines and jobs in one request.
//
//     Produces:
//     - application/json
//
//     Responses:
//       default: pipelinesJobsResponse
func (s *server) pipelinesJobs(w http.ResponseWriter, r *http.Request) {
	pipelinesRes := s.listPipelines()
	jobsRes := s.listPipelineJobs()

	var resp pipelinesJobsResponse
	resp.Body.Pipelines = pipelinesRes
	resp.Body.Jobs = jobsRes

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:response
type pipelinesResponse struct {
	// in: body
	Body struct {
		Pipelines []pipelineResult `json:"pipelines"`
	}
}

// swagger:model pipeline
type pipelineResult struct {
	// Pipeline name
	//
	// example: my_pipeline
	Pipeline string `json:"pipeline"`

	// Is a new job for the pipeline schedulable
	Schedulable bool `json:"schedulable"`

	// Is a job for the pipeline running
	Running bool `json:"running"`
}

// swagger:route GET /pipelines/ pipelines
//
// List pipelines
//
// This will show all defined pipelines with included information about running state or if it is possible to schedule
// a job for this pipeline.
//
//     Produces:
//     - application/json
//
//     Responses:
//       default: pipelinesResponse
func (s *server) pipelines(w http.ResponseWriter, r *http.Request) {
	res := s.listPipelines()

	var resp pipelinesResponse
	resp.Body.Pipelines = res

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:parameters jobLogs
type jobLogsParams struct {
	// Job id
	//
	// required: true
	// in: query
	// example: 52a5cb79-7556-4c52-8e6f-dd6aaf1bc4c8
	Id string `json:"id"`

	// Task name
	//
	// required: true
	// in: query
	// example: my_task
	Task string `json:"task"`
}

// swagger:response
type jobLogsResponse struct {
	// in: body
	Body struct {
		// STDOUT output of task
		Stdout string `json:"stdout"`
		// STDERR output of task
		Stderr string `json:"stderr"`
	}
}

// swagger:route GET /job/logs jobLogs
//
// Get job logs
//
// Task output for the given job and task will be fetched and returned for STDOUT / STDERR.
//
//     Produces:
//     - application/json
//
//     Responses:
//       default: jobLogsResponse
//       400: genericErrorResponse
//       404:
//       500:
func (s *server) jobLogs(w http.ResponseWriter, r *http.Request) {
	var params jobLogsParams

	vars := r.URL.Query()
	params.Id = vars.Get("id")
	jobID, err := uuid.FromString(params.Id)
	if err != nil {
		log.
			WithError(err).
			WithField("jobID", params.Id).
			Warn("Invalid job ID")
		s.sendError(w, http.StatusBadRequest, "Invalid job id")
		return
	}
	params.Task = vars.Get("task")
	if params.Task == "" {
		s.sendError(w, http.StatusBadRequest, "Invalid task name")
		return
	}

	var taskExists bool
	err = s.pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
		if task := j.Tasks.ByName(params.Task); task != nil {
			taskExists = true
		}
	})
	if errors.Is(err, prunner.ErrJobNotFound) {
		s.sendError(w, http.StatusNotFound, "Job not found")
		return
	} else if err != nil {
		log.
			WithError(err).
			WithField("jobID", jobID).
			Errorf("Error reading job")
		s.sendError(w, http.StatusInternalServerError, "Error reading job")
	}

	if !taskExists {
		s.sendError(w, http.StatusNotFound, "Task not found")
		return
	}

	var (
		stdout []byte
		stderr []byte
	)
	stdoutReader, err := s.outputStore.Reader(jobID.String(), params.Task, "stdout")
	if err != nil {
		log.
			WithError(err).
			Errorf("failed to read output store")
	} else {
		stdout, _ = ioutil.ReadAll(stdoutReader)
		stdoutReader.Close()
	}

	stderrReader, err := s.outputStore.Reader(jobID.String(), params.Task, "stderr")
	if err != nil {
		log.
			WithError(err).
			Errorf("failed to read output store")
	} else {
		stderr, _ = ioutil.ReadAll(stderrReader)
		stderrReader.Close()
	}

	var resp jobLogsResponse
	resp.Body.Stdout = string(stdout)
	resp.Body.Stderr = string(stderr)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:parameters jobDetail
type jobDetailParams struct {
	// Job id
	//
	// required: true
	// in: query
	// example: 52a5cb79-7556-4c52-8e6f-dd6aaf1bc4c8
	Id string `json:"id"`
}

// swagger:response
type jobDetailResponse struct {
	// in: body
	Body pipelineJobResult
}

// swagger:route GET /job/detail jobDetail
//
// Get job details
//
// Get details about a single job.
//
//     Produces:
//     - application/json
//
//     Responses:
//       default: jobDetailResponse
//       400: genericErrorResponse
//       404:
func (s *server) jobDetail(w http.ResponseWriter, r *http.Request) {
	var params jobDetailParams

	vars := r.URL.Query()
	params.Id = vars.Get("id")
	jobID, err := uuid.FromString(params.Id)
	if err != nil {
		log.
			WithError(err).
			WithField("jobID", params.Id).
			Warn("Invalid job ID")
		s.sendError(w, http.StatusBadRequest, "Invalid job id")
		return
	}

	var result pipelineJobResult

	err = s.pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
		result = jobToResult(j)
	})
	if errors.Is(err, prunner.ErrJobNotFound) {
		s.sendError(w, http.StatusNotFound, "Job not found")
		return
	} else if err != nil {
		log.
			WithError(err).
			WithField("jobID", jobID).
			Errorf("Error reading job")
		s.sendError(w, http.StatusInternalServerError, "Error reading job")
	}

	var resp jobDetailResponse
	resp.Body = result

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:parameters jobCancel
type jobCancelParams struct {
	// Job id
	//
	// required: true
	// in: query
	// example: 52a5cb79-7556-4c52-8e6f-dd6aaf1bc4c8
	Id string `json:"id"`
}

// swagger:route POST /job/cancel jobCancel
//
// Cancel a running job
//
// Cancels the job and all tasks, but does not wait until all tasks are canceled.
//
//     Produces:
//     - application/json
//
//     Responses:
//       default:
//       400: genericErrorResponse
//       404:
func (s *server) jobCancel(w http.ResponseWriter, r *http.Request) {
	var params jobCancelParams

	vars := r.URL.Query()
	params.Id = vars.Get("id")
	jobID, err := uuid.FromString(params.Id)
	if err != nil {
		log.
			WithError(err).
			WithField("jobIdString", params.Id).
			Warn("Invalid job ID")
		s.sendError(w, http.StatusBadRequest, "Invalid job id")
		return
	}

	log.
		WithField("component", "api").
		WithField("jobID", jobID).
		Info("Canceling job")

	err = s.pRunner.CancelJob(jobID)
	if errors.Is(err, prunner.ErrJobNotFound) {
		s.sendError(w, http.StatusNotFound, "Job not found")
		return
	} else if err != nil {
		log.
			WithError(err).
			WithField("jobID", jobID).
			Errorf("Error canceling job")
		s.sendError(w, http.StatusInternalServerError, "Error canceling job")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(true)
}

func (s *server) listPipelineJobs() []pipelineJobResult {
	res := []pipelineJobResult{}
	s.pRunner.IterateJobs(func(j *prunner.PipelineJob) {
		res = append(res, jobToResult(j))
	})
	sort.Slice(res, func(i, j int) bool {
		return !res[i].Created.Before(res[j].Created)
	})
	return res
}

func (s *server) listPipelines() []pipelineResult {
	pipelineInfos := s.pRunner.ListPipelines()
	res := make([]pipelineResult, len(pipelineInfos))

	for i, pipelineInfo := range pipelineInfos {
		res[i] = pipelineResult{
			Pipeline:    pipelineInfo.Pipeline,
			Schedulable: pipelineInfo.Schedulable,
			Running:     pipelineInfo.Running,
		}
	}

	return res
}

func (s *server) sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	var resp genericErrorResponse
	resp.Body.Error = msg

	_ = json.NewEncoder(w).Encode(resp.Body)
}

// swagger:response genericErrorResponse
type genericErrorResponse struct {
	// in: body
	Body struct {
		// Error message
		Error string `json:"error"`
	}
}
