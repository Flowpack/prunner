package prunner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/go-chi/jwtauth/v5"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"networkteam.com/lab/prunner/definition"
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
	taskRunner := &mockRunner{}

	pRunner, err := newPipelineRunner(defs, taskRunner)
	require.NoError(t, err)

	outputStore := newMockStore()

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
	log.SetLevel(log.DebugLevel)

	taskRunner := &mockRunner{}

	pRunner, err := newPipelineRunner(defs, taskRunner)
	require.NoError(t, err)

	outputStore := newMockStore()

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

	job := pRunner.findJob(jobID)
	require.NotNil(t, job)

	// Wait until job is completed (busy waiting style)
	waitForCondition(t, func() bool {
		j := pRunner.findJob(jobID)
		return j != nil && j.Completed
	}, 50*time.Millisecond, "job exists and is completed")
}

