package prunner

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/taskctl/taskctl/pkg/task"
)

type mockRunner struct {
	wg sync.WaitGroup
}

func (m *mockRunner) Run(t *task.Task) error {
	t.Start = time.Now()

	log.WithField("component", "mockRunner").Debugf("Running task %s", t.Name)

	t.End = time.Now()

	return nil
}

func (m *mockRunner) Cancel() {
}

func (m *mockRunner) Finish() {
}

type mockStore struct {
	mx sync.Mutex

	outputs map[string]bytes.Buffer
}

func newMockStore() *mockStore {
	return &mockStore{
		outputs: make(map[string]bytes.Buffer),
	}
}

func (m *mockStore) Writer(jobID string, taskName string, outputName string) (io.WriteCloser, error) {
	m.mx.Lock()
	buf := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	m.mx.Unlock()

	return &writeCloser{&buf}, nil
}

func (m *mockStore) Reader(jobID string, taskName string, outputName string) (io.ReadCloser, error) {
	m.mx.Lock()
	buf := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	m.mx.Unlock()
	// This is not strictly correct, since the byte slice could be changed on subsequent operations on the buffer,
	// but it should be okay for tests.
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

type writeCloser struct {
	io.Writer
}

func (wc *writeCloser) Close() error {
	return nil
}

func waitForCondition(t *testing.T, f func() bool, wait time.Duration, msg string) {
	t.Helper()
	var d time.Duration
	for {
		if d > 5*time.Second {
			t.Fatalf("Timed out waiting for condition: %s", msg)
		}
		if f() {
			break
		}
		time.Sleep(wait)
		d += wait
	}
}
