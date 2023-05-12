package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/taskctl/taskctl/pkg/task"

	"github.com/Flowpack/prunner"
	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/taskctl"
	"github.com/Flowpack/prunner/test"
)

var defs = &definition.PipelinesDef{
	Pipelines: map[string]definition.PipelineDef{
		"release_it": {
			// Concurrency of 1 is the default for a single concurrent execution
			Concurrency:   1,
			QueueLimit:    nil,
			QueueStrategy: definition.QueueStrategyAppend,
			Tasks: map[string]definition.TaskDef{
				"test": {
					Script: []string{"go test"},
				},
				"lint": {
					Script: []string{"go lint"},
				},
				"generate": {
					Script: []string{"go generate"},
				},
				"build": {
					Script:    []string{"go build -o bin/out"},
					DependsOn: []string{"generate"},
				},
				"deploy": {
					Script: []string{
						"cp bin/out my-target/out",
						"echo done",
					},
					DependsOn: []string{"build", "test", "lint"},
				},
			},
			SourcePath: "fixtures",
		},
	},
}

func TestServer_Pipelines(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest(http.MethodGet, "/pipelines", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	assert.JSONEq(t, `{
		"pipelines": [{
			"pipeline": "release_it",
			"running": false,
			"schedulable": true
		}]
	}`, rec.Body.String())
}

func TestServer_PipelinesCanNotBeAccessedWithWrongJwtToken(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	wrongTokenAuthClient := jwtauth.New("HS256", []byte("THIS-IS-WRONG"), nil)
	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := wrongTokenAuthClient.Encode(claims)

	req := httptest.NewRequest(http.MethodGet, "/pipelines", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServer_PipelinesSchedule(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest(http.MethodPost, "/pipelines/schedule", strings.NewReader(`{
		"pipeline": "release_it"
	}`))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var result struct{ JobID string }
	err = json.NewDecoder(rec.Body).Decode(&result)
	require.NoError(t, err)

	assert.NotEmpty(t, result.JobID)
	jobID := uuid.Must(uuid.FromString(result.JobID))

	err = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {})
	require.NoError(t, err)

	// Wait until job is completed (busy waiting style)
	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 50*time.Millisecond, "job exists and is completed")
}

func TestServer_JobCreationTimeIsRoundedForPhpCompatibility(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest(http.MethodPost, "/pipelines/schedule", strings.NewReader(`{
		"pipeline": "release_it"
	}`))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var result struct{ JobID string }
	err = json.NewDecoder(rec.Body).Decode(&result)
	require.NoError(t, err)

	assert.NotEmpty(t, result.JobID)
	jobID := uuid.Must(uuid.FromString(result.JobID))

	var jobCreated time.Time
	err = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
		jobCreated = j.Created
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/pipelines/jobs", strings.NewReader(""))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var result2 struct {
		Jobs []struct {
			Created string
		}
	}
	err = json.NewDecoder(rec.Body).Decode(&result2)
	require.NoError(t, err)

	// the default is RFC3339Nano; but we use RFC3339
	require.Equal(t, jobCreated.In(time.UTC).Format(time.RFC3339), result2.Jobs[0].Created)
}

func TestServer_JobCancel(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	// Test cancellation by using a wait group to wait until the task runner was canceled (simulating a long-running task)
	var wg sync.WaitGroup
	wg.Add(1)

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				wg.Wait()
				return nil
			},
			OnCancel: func() {
				wg.Done()
			},
		}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	// Schedule pipeline run

	req := httptest.NewRequest(http.MethodPost, "/pipelines/schedule", strings.NewReader(`{
		"pipeline": "release_it"
	}`))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var result struct{ JobID string }
	err = json.NewDecoder(rec.Body).Decode(&result)
	require.NoError(t, err)

	assert.NotEmpty(t, result.JobID)
	jobID := uuid.Must(uuid.FromString(result.JobID))

	// Cancel job

	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/job/cancel?id=%s", result.JobID), nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Check job was canceled

	err = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {})
	require.NoError(t, err)

	// Wait until job is completed (busy waiting style)
	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
			completed = j.Completed
		})
		return completed

	}, 50*time.Millisecond, "job exists and was completed")
}

func TestServer_NoAccessToProfilingRoutesIfDisabled(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof", nil)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServer_AccessToProfilingWorksIfEnabledWithoutToken(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	outputStore := test.NewMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	// !!! the last parameter is "enableProfiling: true"
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, true)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestServer_JobLogs(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	outputStore := test.NewMockOutputStore()

	pRunner, err := prunner.NewPipelineRunner(ctx, defs, func(j *prunner.PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				// We make an explicit call to the output store here, since we do not use a real executor for these tests
				w, err := outputStore.Writer(j.ID.String(), t.Name, "stdout")
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(w, "out from %s", t.Name)
				w, err = outputStore.Writer(j.ID.String(), t.Name, "stderr")
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(w, "err from %s", t.Name)
				return nil
			},
		}
	}, nil, outputStore)
	require.NoError(t, err)

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := NewServer(pRunner, outputStore, noopMiddleware, tokenAuth, false)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest(http.MethodPost, "/pipelines/schedule", strings.NewReader(`{
		"pipeline": "release_it"
	}`))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var result struct{ JobID string }
	err = json.NewDecoder(rec.Body).Decode(&result)
	require.NoError(t, err)

	assert.NotEmpty(t, result.JobID)
	jobID := uuid.Must(uuid.FromString(result.JobID))

	err = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {})
	require.NoError(t, err)

	// Wait until job is completed (busy waiting style)
	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *prunner.PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 50*time.Millisecond, "job exists and is completed")

	// Get logs
	req = httptest.NewRequest(http.MethodGet, "/job/logs?id="+jobID.String()+"&task=lint", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var logs struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	err = json.NewDecoder(rec.Body).Decode(&logs)
	require.NoError(t, err)

	assert.NotEmpty(t, logs.Stdout)
	assert.NotEmpty(t, logs.Stderr)
}
