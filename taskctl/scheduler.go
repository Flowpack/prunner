package taskctl

import (
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"

	"github.com/taskctl/taskctl/pkg/utils"
)

// Scheduler executes ExecutionGraph
type Scheduler struct {
	taskRunner runner.Runner
	pause      time.Duration

	cancelled int32

	onStageChange func(stage *scheduler.Stage)
}

// NewScheduler create new Scheduler instance
func NewScheduler(r runner.Runner) *Scheduler {
	s := &Scheduler{
		pause:      50 * time.Millisecond,
		taskRunner: r,
	}

	return s
}

func (s *Scheduler) OnStageChange(f func(stage *scheduler.Stage)) {
	s.onStageChange = f
}

// Schedule starts execution of the given ExecutionGraph
//
// Modified to notify on stage changes
func (s *Scheduler) Schedule(g *scheduler.ExecutionGraph) error {
	var wg sync.WaitGroup

	var (
		lastErr error
		mx      sync.Mutex
	)

	for !s.isDone(g) {
		if atomic.LoadInt32(&s.cancelled) == 1 {
			break
		}

		for _, stage := range g.Nodes() {
			status := stage.ReadStatus()
			if status != scheduler.StatusWaiting {
				continue
			}

			if stage.Condition != "" {
				meets, err := checkStageCondition(stage.Condition)
				if err != nil {
					logrus.Error(err)
					stage.UpdateStatus(scheduler.StatusError)
					s.notifyStageChange(stage)
					s.Cancel()
					continue
				}

				if !meets {
					stage.UpdateStatus(scheduler.StatusSkipped)
					s.notifyStageChange(stage)
					continue
				}
			}

			if !checkStatus(g, stage) {
				continue
			}

			wg.Add(1)
			stage.UpdateStatus(scheduler.StatusRunning)
			s.notifyStageChange(stage)
			go func(stage *scheduler.Stage) {
				defer func() {
					stage.End = time.Now()
					wg.Done()
				}()

				stage.Start = time.Now()

				err := s.runStage(stage)
				if err != nil {
					stage.UpdateStatus(scheduler.StatusError)
					s.notifyStageChange(stage)

					if !stage.AllowFailure {
						mx.Lock()
						lastErr = err
						mx.Unlock()
						return
					}
				}

				stage.UpdateStatus(scheduler.StatusDone)
				s.notifyStageChange(stage)
			}(stage)
		}

		time.Sleep(s.pause)
	}

	wg.Wait()

	return lastErr
}

// Cancel cancels executing tasks
func (s *Scheduler) Cancel() {
	atomic.StoreInt32(&s.cancelled, 1)
	s.taskRunner.Cancel()
}

// Finish finishes scheduler's TaskRunner
func (s *Scheduler) Finish() {
	s.taskRunner.Finish()
}

func (s *Scheduler) isDone(p *scheduler.ExecutionGraph) bool {
	for _, stage := range p.Nodes() {
		switch stage.ReadStatus() {
		case scheduler.StatusWaiting, scheduler.StatusRunning:
			return false
		}
	}

	return true
}

func (s *Scheduler) runStage(stage *scheduler.Stage) error {
	if stage.Pipeline != nil {
		return s.Schedule(stage.Pipeline)
	}

	t := stage.Task
	if stage.Env != nil {
		if t.Env == nil {
			t.Env = stage.Env
		} else {
			t.Env = t.Env.Merge(stage.Env)
		}
	}

	if stage.Variables != nil {
		if t.Variables == nil {
			t.Variables = stage.Variables
		} else {
			t.Variables = t.Env.Merge(stage.Variables)
		}
	}

	return s.taskRunner.Run(stage.Task)
}

func (s *Scheduler) notifyStageChange(stage *scheduler.Stage) {
	if s.onStageChange != nil {
		s.onStageChange(stage)
	}
}

func checkStatus(p *scheduler.ExecutionGraph, stage *scheduler.Stage) (ready bool) {
	ready = true
	for _, dep := range p.To(stage.Name) {
		depStage, err := p.Node(dep)
		if err != nil {
			logrus.Fatal(err)
		}

		switch depStage.ReadStatus() {
		case scheduler.StatusDone, scheduler.StatusSkipped:
			continue
		case scheduler.StatusError:
			if !depStage.AllowFailure {
				ready = false
				stage.UpdateStatus(scheduler.StatusCanceled)
			}
		case scheduler.StatusCanceled:
			ready = false
			stage.UpdateStatus(scheduler.StatusCanceled)
		default:
			ready = false
		}
	}

	return ready
}

func checkStageCondition(condition string) (bool, error) {
	cmd := exec.Command(condition)
	err := cmd.Run()
	if err != nil {
		if utils.IsExitError(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}
