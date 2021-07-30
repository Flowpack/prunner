package taskctl

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"
)

// These tests are copied from github.com/taskctl/taskctl/pkg/scheduler to test the custom Scheduler implementation

type mockTaskRunner struct {
}

func (t2 mockTaskRunner) Run(t *task.Task) error {
	if t.Commands[0] == "/usr/bin/false" {
		t.ExitCode = 1
		t.Errored = true
		return errors.New("task failed")
	}

	return nil
}

func (t2 mockTaskRunner) Cancel() {}

func (t2 mockTaskRunner) Finish() {}

func TestExecutionGraph_Scheduler(t *testing.T) {
	stage1 := &scheduler.Stage{
		Name: "stage1",
		Task: task.FromCommands("/usr/bin/true"),
	}
	stage2 := &scheduler.Stage{
		Name:      "stage2",
		Task:      task.FromCommands("/usr/bin/false"),
		DependsOn: []string{"stage1"},
	}
	stage3 := &scheduler.Stage{
		Name:      "stage3",
		Task:      task.FromCommands("/usr/bin/false"),
		DependsOn: []string{"stage2"},
	}
	stage4 := &scheduler.Stage{
		Name:      "stage4",
		Task:      task.FromCommands("true"),
		DependsOn: []string{"stage3"},
	}

	graph, err := scheduler.NewExecutionGraph(stage1, stage2, stage3, stage4)
	if err != nil {
		t.Fatal(err)
	}

	taskRunner := mockTaskRunner{}

	schdlr := NewScheduler(taskRunner)
	err = schdlr.Schedule(graph)
	if err == nil {
		t.Fatal(err)
	}

	if graph.Duration() <= 0 {
		t.Fatal()
	}

	if stage3.Status != scheduler.StatusCanceled || stage4.Status != scheduler.StatusCanceled {
		t.Fatal("stage3 was not cancelled")
	}
}

func TestExecutionGraph_Scheduler_AllowFailure(t *testing.T) {
	stage1 := &scheduler.Stage{
		Name: "stage1",
		Task: task.FromCommands("true"),
	}
	stage2 := &scheduler.Stage{
		Name:         "stage2",
		Task:         task.FromCommands("false"),
		AllowFailure: true,
		DependsOn:    []string{"stage1"},
	}
	stage3 := &scheduler.Stage{
		Name:      "stage3",
		Task:      task.FromCommands("{{.command}}"),
		DependsOn: []string{"stage2"},
		Variables: variables.FromMap(map[string]string{"command": "true"}),
		Env:       variables.NewVariables(),
	}

	graph, err := scheduler.NewExecutionGraph(stage1, stage2, stage3)
	if err != nil {
		t.Fatal(err)
	}

	taskRunner := mockTaskRunner{}

	schdlr := NewScheduler(taskRunner)
	err = schdlr.Schedule(graph)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if stage3.Status == scheduler.StatusCanceled {
		t.Fatal("stage3 was cancelled")
	}

	if stage3.Duration() <= 0 {
		t.Error()
	}

	schdlr.Finish()
}

func TestSkippedStage(t *testing.T) {
	stage1 := &scheduler.Stage{
		Name:      "stage1",
		Task:      task.FromCommands("true"),
		Condition: "true",
	}
	stage2 := &scheduler.Stage{
		Name:         "stage2",
		Task:         task.FromCommands("false"),
		AllowFailure: true,
		DependsOn:    []string{"stage1"},
		Condition:    "false",
	}

	graph, err := scheduler.NewExecutionGraph(stage1, stage2)
	if err != nil {
		t.Fatal(err)
	}

	taskRunner := mockTaskRunner{}

	schdlr := NewScheduler(taskRunner)
	err = schdlr.Schedule(graph)
	if err != nil {
		t.Fatal(err)
	}

	if stage1.Status != scheduler.StatusDone || stage2.Status != scheduler.StatusSkipped {
		t.Error()
	}
}

func TestScheduler_Cancel(t *testing.T) {
	stage1 := &scheduler.Stage{
		Name: "stage1",
		Task: task.FromCommands("sleep 60"),
	}

	graph, err := scheduler.NewExecutionGraph(stage1)
	if err != nil {
		t.Fatal(err)
	}

	taskRunner := mockTaskRunner{}

	schdlr := NewScheduler(taskRunner)
	go func() {
		schdlr.Cancel()
	}()

	err = schdlr.Schedule(graph)
	if err != nil {
		t.Fatal(err)
	}

	if atomic.LoadInt32(&schdlr.cancelled) != 1 {
		t.Error()
	}
}

func TestConditionErroredStage(t *testing.T) {
	stage1 := &scheduler.Stage{
		Name:      "stage1",
		Task:      task.FromCommands("true"),
		Condition: "true",
	}
	stage2 := &scheduler.Stage{
		Name:         "stage2",
		Task:         task.FromCommands("false"),
		AllowFailure: true,
		DependsOn:    []string{"stage1"},
		Condition:    "/unknown-bin",
	}

	graph, err := scheduler.NewExecutionGraph(stage1, stage2)
	if err != nil {
		t.Fatal(err)
	}

	taskRunner := mockTaskRunner{}

	schdlr := NewScheduler(taskRunner)
	err = schdlr.Schedule(graph)
	if err != nil {
		t.Fatal(err)
	}

	if stage1.Status != scheduler.StatusDone || stage2.Status != scheduler.StatusError {
		t.Error()
	}
}

func ExampleScheduler_Schedule() {
	format := task.FromCommands("go fmt ./...")
	build := task.FromCommands("go build ./..")
	r, _ := runner.NewTaskRunner()
	s := NewScheduler(r)

	graph, err := scheduler.NewExecutionGraph(
		&scheduler.Stage{Name: "format", Task: format},
		&scheduler.Stage{Name: "build", Task: build, DependsOn: []string{"format"}},
	)
	if err != nil {
		return
	}

	err = s.Schedule(graph)
	if err != nil {
		fmt.Println(err)
	}
}
