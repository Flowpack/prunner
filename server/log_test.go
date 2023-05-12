package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/apex/log/handlers/memory"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Flowpack/prunner/server"
)

func TestStructuredLogger(t *testing.T) {
	memHandler := memory.New()
	logger := &log.Logger{
		Handler: memHandler,
		Level:   log.DebugLevel,
	}

	sLogger := server.StructuredLogFormatter(logger)

	// mock http request
	req, err := http.NewRequest("GET", "/test", nil)
	require.NoError(t, err)

	ctx := context.WithValue(req.Context(), middleware.RequestIDKey, "123")
	req = req.WithContext(ctx)

	h := middleware.RequestLogger(sLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Microsecond)
		w.WriteHeader(200)
		w.Write([]byte("test"))
	}))

	// mock http response
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// check the log entries
	assert.Len(t, memHandler.Entries, 2)
	assert.Equal(t, "request started", memHandler.Entries[0].Message)
	assert.Equal(t, "request complete", memHandler.Entries[1].Message)

	assert.Equal(t, "GET", memHandler.Entries[0].Fields["httpMethod"])
	assert.Equal(t, "/test", memHandler.Entries[0].Fields["httpPath"])
	assert.Equal(t, "http", memHandler.Entries[0].Fields["httpScheme"])
	assert.Equal(t, "123", memHandler.Entries[0].Fields["reqID"])

	assert.Equal(t, 200, memHandler.Entries[1].Fields["respStatus"])
	assert.Equal(t, 4, memHandler.Entries[1].Fields["respBytesLength"])
	assert.Greater(t, memHandler.Entries[1].Fields["respElapsedMs"], float64(0))
}
