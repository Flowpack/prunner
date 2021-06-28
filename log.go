package prunner

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/apex/log"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
)

func newHttpLogger(c *cli.Context) func(next http.Handler) http.Handler {
	disableAnsi := c.Bool("disable-ansi")

	if isatty.IsTerminal(os.Stdout.Fd()) && !disableAnsi {
		return middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: apexLogAdapter{log.WithField("component", "api")}})
	} else {
		return middleware.RequestLogger(&structuredLogger{logger: log.WithField("component", "api")})
	}
}

type apexLogAdapter struct {
	logger log.Interface
}

func (l apexLogAdapter) Print(v ...interface{}) {
	l.logger.Info(fmt.Sprint(v...))
}

type structuredLogger struct {
	logger log.Interface
}

func (l *structuredLogger) NewLogEntry(r *http.Request) middleware.LogEntry {
	entry := &structuredLoggerEntry{Logger: l.logger}
	logFields := make(log.Fields)

	if reqID := middleware.GetReqID(r.Context()); reqID != "" {
		logFields["reqID"] = reqID
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	logFields["httpScheme"] = scheme
	logFields["httpProto"] = r.Proto
	logFields["httpMethod"] = r.Method

	logFields["remoteAddr"] = r.RemoteAddr
	logFields["userAgent"] = r.UserAgent()

	logFields["httpHost"] = r.Host
	logFields["httpPath"] = r.RequestURI

	entry.Logger = entry.Logger.WithFields(logFields)

	entry.Logger.Info("request started")

	return entry
}

type structuredLoggerEntry struct {
	Logger log.Interface
}

func (l *structuredLoggerEntry) Write(status, bytes int, header http.Header, elapsed time.Duration, extra interface{}) {
	l.Logger = l.Logger.WithFields(log.Fields{
		"respStatus":       status,
		"respBytesLength": bytes,
		"respElapsedMs":   float64(elapsed.Nanoseconds()) / 1000000.0,
	})

	l.Logger.Info("request complete")
}

func (l *structuredLoggerEntry) Panic(v interface{}, stack []byte) {
	l.Logger = l.Logger.WithFields(log.Fields{
		"stack": string(stack),
		"panic": fmt.Sprintf("%+v", v),
	})
}

// Helper methods used by the application to get the request-scoped
// logger entry and set additional fields between handlers.
//
// This is a useful pattern to use to set state on the entry as it
// passes through the handler chain, which at any point can be logged
// with a call to .Print(), .Info(), etc.

func GetLogEntry(r *http.Request) log.Interface {
	entry := middleware.GetLogEntry(r).(*structuredLoggerEntry)
	return entry.Logger
}

func LogEntrySetField(r *http.Request, key string, value interface{}) {
	if entry, ok := r.Context().Value(middleware.LogEntryCtxKey).(*structuredLoggerEntry); ok {
		entry.Logger = entry.Logger.WithField(key, value)
	}
}

func LogEntrySetFields(r *http.Request, fields map[string]interface{}) {
	if entry, ok := r.Context().Value(middleware.LogEntryCtxKey).(*structuredLoggerEntry); ok {
		entry.Logger = entry.Logger.WithFields(log.Fields(fields))
	}
}
