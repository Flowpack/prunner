package prunner

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/apex/log"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	jsontime "github.com/liamylian/jsontime/v2/v2"
	"networkteam.com/lab/prunner/taskctl"
)

var json = jsontime.ConfigWithCustomTimeFormat

func init() {
	jsontime.SetDefaultTimeFormat(time.RFC3339, time.Local)
}

type server struct {
	pRunner     *pipelineRunner
	handler     http.Handler
	outputStore taskctl.OutputStore
}

func newServer(pRunner *pipelineRunner, outputStore taskctl.OutputStore, logger func(http.Handler) http.Handler, tokenAuth *jwtauth.JWTAuth) *server {
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
		r.Get("/logs", srv.jobLogs)
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

func (h *server) pipelinesSchedule(w http.ResponseWriter, r *http.Request) {
	_, claims, _ := jwtauth.FromContext(r.Context())
	var user string
	if sub, ok := claims["sub"].(string); ok {
		user = sub
	}

	var in pipelinesScheduleRequest
	err := json.NewDecoder(r.Body).Decode(&in)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, fmt.Sprintf("Error decoding JSON: %v", err))
		return
	}

	pJob, err := h.pRunner.ScheduleAsync(in.Pipeline, ScheduleOpts{Variables: in.Variables, User: user})
	if err != nil {
		// TODO Send JSON error and include expected errors (see resolveScheduleAction)

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

	_ = json.NewEncoder(w).Encode(pipelinesScheduleResponse{
		JobID: pJob.ID.String(),
	})
}

type pipelinesJobsResponse struct {
	Pipelines []pipelineResult    `json:"pipelines"`
	Jobs      []pipelineJobResult `json:"jobs"`
}

func (h *server) pipelinesJobs(w http.ResponseWriter, r *http.Request) {
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

func (h *server) pipelines(w http.ResponseWriter, r *http.Request) {
	res := h.pRunner.ListPipelines()
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

func (h *server) jobLogs(w http.ResponseWriter, r *http.Request) {
	vars := r.URL.Query()
	jobIDString := vars.Get("id")
	jobID, err := uuid.FromString(jobIDString)
	if err != nil {
		log.
			WithError(err).
			WithField("jobIdString", jobIDString).
			Warn("Invalid job ID")
		h.sendError(w, http.StatusBadRequest, "Invalid job id")
		return
	}
	taskName := vars.Get("task")
	if taskName == "" {
		h.sendError(w, http.StatusBadRequest, "Invalid task name")
		return
	}

	job := h.pRunner.FindJob(jobID)
	if job == nil {
		h.sendError(w, http.StatusNotFound, "Job not found")
		return
	}

	if task := job.Tasks.byName(taskName); task == nil {
		h.sendError(w, http.StatusNotFound, "Task not found")
		return
	}

	var (
		stdout []byte
		stderr []byte
	)
	stdoutReader, err := h.outputStore.Reader(job.ID.String(), taskName, "stdout")
	if err != nil {
		log.
			WithError(err).
			Errorf("failed to read output store")
	} else {
		stdout, _ = ioutil.ReadAll(stdoutReader)
		stdoutReader.Close()
	}

	stderrReader, err := h.outputStore.Reader(job.ID.String(), taskName, "stderr")
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

func (h *server) sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{
		Error: msg,
	})
}
