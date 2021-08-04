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

type pipelinesScheduleRequest struct {
	Pipeline  string
	Variables map[string]interface{}
}

type pipelinesScheduleResponse struct {
	JobID string `json:"jobId"`
}

func (s *server) pipelinesSchedule(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	var user string
	if sub, ok := claims["sub"].(string); ok {
		user = sub
	}

	var in pipelinesScheduleRequest
	err := json.NewDecoder(r.Body).Decode(&in)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error decoding JSON: %v", err))
		return
	}

	pJob, err := s.pRunner.ScheduleAsync(in.Pipeline, prunner.ScheduleOpts{Variables: in.Variables, User: user})
	if err != nil {
		// TODO Send JSON error and include expected errors (see resolveScheduleAction)

		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error scheduling pipeline: %v", err))
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

	_ = json.NewEncoder(w).Encode(pipelinesScheduleResponse{
		JobID: pJob.ID.String(),
	})
}

type pipelinesJobsResponse struct {
	Pipelines []pipelineResult    `json:"pipelines"`
	Jobs      []pipelineJobResult `json:"jobs"`
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

func jobToResult(j *prunner.PipelineJob) pipelineJobResult {
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
			Error:    helper.ErrToStrPtr(t.Error),
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
		LastError: helper.ErrToStrPtr(j.LastError),

		Variables: j.Variables,
		User:      j.User,
	}
}

func (s *server) pipelinesJobs(w http.ResponseWriter, r *http.Request) {
	pipelinesRes := s.listPipelines()
	jobsRes := s.listPipelineJobs()

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

type pipelineResult struct {
	Pipeline    string `json:"pipeline"`
	Schedulable bool   `json:"schedulable"`
	Running     bool   `json:"running"`
}

func (s *server) pipelines(w http.ResponseWriter, r *http.Request) {
	res := s.listPipelines()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pipelinesResponse{
		Pipelines: res,
	})
}

type jobLogsResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func (s *server) jobLogs(w http.ResponseWriter, r *http.Request) {
	vars := r.URL.Query()
	jobIDString := vars.Get("id")
	jobID, err := uuid.FromString(jobIDString)
	if err != nil {
		log.
			WithError(err).
			WithField("jobIdString", jobIDString).
			Warn("Invalid job ID")
		s.sendError(w, http.StatusBadRequest, "Invalid job id")
		return
	}
	taskName := vars.Get("task")
	if taskName == "" {
		s.sendError(w, http.StatusBadRequest, "Invalid task name")
		return
	}

	var taskExists bool
	err = s.pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
		if task := j.Tasks.ByName(taskName); task != nil {
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
	stdoutReader, err := s.outputStore.Reader(jobID.String(), taskName, "stdout")
	if err != nil {
		log.
			WithError(err).
			Errorf("failed to read output store")
	} else {
		stdout, _ = ioutil.ReadAll(stdoutReader)
		stdoutReader.Close()
	}

	stderrReader, err := s.outputStore.Reader(jobID.String(), taskName, "stderr")
	if err != nil {
		log.
			WithError(err).
			Errorf("failed to read output store")
	} else {
		stderr, _ = ioutil.ReadAll(stderrReader)
		stderrReader.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jobLogsResponse{
		Stdout: string(stdout),
		Stderr: string(stderr),
	})
}

func (s *server) jobDetail(w http.ResponseWriter, r *http.Request) {
	vars := r.URL.Query()
	jobIDString := vars.Get("id")
	jobID, err := uuid.FromString(jobIDString)
	if err != nil {
		log.
			WithError(err).
			WithField("jobIdString", jobIDString).
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

func (s *server) jobCancel(w http.ResponseWriter, r *http.Request) {
	vars := r.URL.Query()
	jobIDString := vars.Get("id")
	jobID, err := uuid.FromString(jobIDString)
	if err != nil {
		log.
			WithError(err).
			WithField("jobIdString", jobIDString).
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

func (s *server) sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
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