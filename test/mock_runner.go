package test

import (
	"time"

	"github.com/apex/log"
	"github.com/taskctl/taskctl/pkg/task"

	"github.com/Flowpack/prunner/taskctl"
)

type MockRunner struct {
	onTaskChange func(t *task.Task)
	OnRun        func(t *task.Task) error
	OnCancel     func()
}

func (m *MockRunner) SetOnTaskChange(f func(t *task.Task)) {
	m.onTaskChange = f
}

var _ taskctl.Runner = &MockRunner{}

func (m *MockRunner) Run(t *task.Task) error {
	t.Start = time.Now()
	if m.onTaskChange != nil {
		m.onTaskChange(t)
	}

	log.WithField("component", "mockRunner").Debugf("Running task %s", t.Name)
	time.Sleep(1 * time.Millisecond)

	var err error

	if m.OnRun != nil {
		err = m.OnRun(t)
	}

	t.End = time.Now()
	if m.onTaskChange != nil {
		m.onTaskChange(t)
	}

	return err
}

func (m *MockRunner) Cancel() {
	if m.OnCancel != nil {
		m.OnCancel()
	}
}

func (m *MockRunner) Finish() {
}
