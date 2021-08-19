package test

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/Flowpack/prunner/taskctl"
)

type mockOutputStore struct {
	mx sync.Mutex

	outputs map[string]bytes.Buffer
}

var _ taskctl.OutputStore = &mockOutputStore{}

func NewMockOutputStore() *mockOutputStore {
	return &mockOutputStore{
		outputs: make(map[string]bytes.Buffer),
	}
}

func (m *mockOutputStore) Writer(jobID string, taskName string, outputName string) (io.WriteCloser, error) {
	m.mx.Lock()
	buf := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	m.mx.Unlock()

	return &writeCloser{&buf}, nil
}

func (m *mockOutputStore) Reader(jobID string, taskName string, outputName string) (io.ReadCloser, error) {
	m.mx.Lock()
	buf := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	m.mx.Unlock()
	// This is not strictly correct, since the byte slice could be changed on subsequent operations on the buffer,
	// but it should be okay for tests.
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (m *mockOutputStore) Remove(jobID string) error {
	return nil
}
