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

	outputs map[string]*bytes.Buffer
}

var _ taskctl.OutputStore = &mockOutputStore{}

func NewMockOutputStore() *mockOutputStore {
	return &mockOutputStore{
		outputs: make(map[string]*bytes.Buffer),
	}
}

func (m *mockOutputStore) Writer(jobID string, taskName string, outputName string) (io.WriteCloser, error) {
	m.mx.Lock()
	defer m.mx.Unlock()

	key := fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)
	buf, ok := m.outputs[key]
	if !ok {
		buf = new(bytes.Buffer)
		m.outputs[key] = buf
	}

	return &writeCloser{buf}, nil
}

func (m *mockOutputStore) Reader(jobID string, taskName string, outputName string) (io.ReadCloser, error) {
	m.mx.Lock()
	defer m.mx.Unlock()
	buf, ok := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	if !ok {
		return nil, fmt.Errorf("missing output")
	}

	// This is not strictly correct, since the byte slice could be changed on subsequent operations on the buffer,
	// but it should be okay for tests.
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (m *mockOutputStore) Remove(jobID string) error {
	return nil
}

func (m *mockOutputStore) GetBytes(jobID string, taskName string, outputName string) []byte {
	m.mx.Lock()
	defer m.mx.Unlock()
	buf, ok := m.outputs[fmt.Sprintf("%s-%s.%s", jobID, taskName, outputName)]
	if !ok {
		return nil
	}
	return buf.Bytes()
}