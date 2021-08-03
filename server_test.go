package prunner

import (
	"context"
	"fmt"
	"io"
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
	"networkteam.com/lab/prunner/definition"
	"networkteam.com/lab/prunner/taskctl"
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

	pRunner, err := newPipelineRunner(ctx, defs, func() taskctl.Runner {
		return &mockRunner{}
	}, nil)
	require.NoError(t, err)

	outputStore := newMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := newServer(pRunner, outputStore, noopMiddleware, tokenAuth)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest("GET", "/pipelines", nil)
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

func TestServer_PipelinesSchedule(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := newPipelineRunner(ctx, defs, func() taskctl.Runner {
		return &mockRunner{}
	}, nil)
	require.NoError(t, err)

	outputStore := newMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := newServer(pRunner, outputStore, noopMiddleware, tokenAuth)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest("POST", "/pipelines/schedule", strings.NewReader(`{
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

	job := pRunner.FindJob(jobID)
	require.NotNil(t, job)

	// Wait until job is completed (busy waiting style)
	waitForCondition(t, func() bool {
		j := pRunner.FindJob(jobID)
		return j != nil && j.Completed
	}, 50*time.Millisecond, "job exists and is completed")
}

func TestServer_JobCreationTimeIsRoundedForPhpCompatibility(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	pRunner, err := newPipelineRunner(ctx, defs, func() taskctl.Runner {
		return &mockRunner{}
	}, nil)
	require.NoError(t, err)

	outputStore := newMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := newServer(pRunner, outputStore, noopMiddleware, tokenAuth)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest("POST", "/pipelines/schedule", strings.NewReader(`{
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

	job := pRunner.FindJob(jobID)
	require.NotNil(t, job)

	req = httptest.NewRequest("GET", "/pipelines/jobs", strings.NewReader(""))
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
	require.Equal(t, job.Created.In(time.UTC).Format(time.RFC3339), result2.Jobs[0].Created)
}

func TestServer_JobCancel(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	// Test cancellation by using a wait group to wait until the task runner was canceled (simulating a long-running task)
	var wg sync.WaitGroup
	wg.Add(1)

	pRunner, err := newPipelineRunner(ctx, defs, func() taskctl.Runner {
		return &mockRunner{
			onRun: func(t *task.Task) {
				wg.Wait()
			},
			onCancel: func() {
				wg.Done()
			},
		}
	}, nil)
	require.NoError(t, err)

	outputStore := newMockOutputStore()

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := newServer(pRunner, outputStore, noopMiddleware, tokenAuth)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	// Schedule pipeline run

	req := httptest.NewRequest("POST", "/pipelines/schedule", strings.NewReader(`{
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

	req = httptest.NewRequest("POST", fmt.Sprintf("/job/cancel?id=%s", result.JobID), nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenString))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	// Check job was canceled

	job := pRunner.FindJob(jobID)
	require.NotNil(t, job)

	// Wait until job is completed (busy waiting style)
	waitForCondition(t, func() bool {
		j := pRunner.FindJob(jobID)
		return j != nil && j.Completed
	}, 50*time.Millisecond, "job exists and was completed")
}

// NOTE: the test does not currently fail in OSX, because there the kernel supports way longer arguments.
// In CI though, the test fails properly.
func TestServer_HugeOutput(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"testHugeOutput": {
				Concurrency:   1,
				QueueLimit:    nil,
				QueueStrategy: definition.QueueStrategyAppend,

				Tasks: map[string]definition.TaskDef{
					"generateHugeOutput": {
						// 20 MB output
						Script: []string{"head -c 20000000 </dev/urandom | base64"},
					},
					"nextTask": {
						Script:    []string{"echo hello"},
						DependsOn: []string{"generateHugeOutput"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	outputStore := newMockOutputStore()
	pRunner, err := newPipelineRunner(ctx, defs, func() taskctl.Runner {
		// taskctl.NewTaskRunner never actually returns an error
		taskRunner, _ := taskctl.NewTaskRunner(outputStore)

		// Do not output task stdout / stderr to the server process. NOTE: Before/After execution logs won't be visible because of this
		taskRunner.Stdout = io.Discard
		taskRunner.Stderr = io.Discard

		return taskRunner
	}, nil)
	require.NoError(t, err)

	tokenAuth := jwtauth.New("HS256", []byte("not-very-secret"), nil)
	noopMiddleware := func(next http.Handler) http.Handler { return next }
	srv := newServer(pRunner, outputStore, noopMiddleware, tokenAuth)

	claims := make(map[string]interface{})
	jwtauth.SetIssuedNow(claims)
	_, tokenString, _ := tokenAuth.Encode(claims)

	req := httptest.NewRequest("POST", "/pipelines/schedule", strings.NewReader(`{
		"pipeline": "testHugeOutput"
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

	job := pRunner.FindJob(jobID)
	require.NotNil(t, job)

	// Wait until job is completed (busy waiting style)
	waitForCondition(t, func() bool {
		j := pRunner.FindJob(jobID)
		fmt.Printf("%v", j.Completed)
		fmt.Printf("%v", j.Canceled)
		fmt.Printf("%s", j.LastError)
		return j != nil && (j.Completed || j.Canceled)
	}, 50*time.Millisecond, "job exists and is completed")
	j := pRunner.FindJob(jobID)
	for _, t := range j.Tasks {
		fmt.Printf("%s: %s", t.Name, t.Status)
	}
}
