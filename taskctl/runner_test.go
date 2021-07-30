package taskctl

import (
	"fmt"
	"io/ioutil"
	"strings"
	"testing"
	"time"

	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"
)

// These tests are copied from github.com/taskctl/taskctl/pkg/runner to test the custom TaskRunner implementation

func TestTaskRunner(t *testing.T) {
	c := runner.NewExecutionContext(nil, "/", variables.NewVariables(), []string{"true"}, []string{"false"}, []string{"echo 1"}, []string{"echo 2"})

	runnr, err := NewTaskRunner(nil, WithContexts(map[string]*runner.ExecutionContext{"local": c}))
	if err != nil {
		t.Fatal(err)
	}
	runnr.SetContexts(map[string]*runner.ExecutionContext{
		"default": runner.DefaultContext(),
		"local":   c,
	})
	if _, ok := runnr.contexts["default"]; !ok {
		t.Error()
	}

	runnr.Stdout, runnr.Stderr = ioutil.Discard, ioutil.Discard
	runnr.SetVariables(variables.FromMap(map[string]string{"Root": "/tmp"}))
	runnr.WithVariable("Root", "/")

	task1 := task.NewTask()
	task1.Context = "local"
	task1.ExportAs = "EXPORT_NAME"

	task1.Commands = []string{"echo 'taskctl'"}
	task1.Name = "some test task"
	task1.Dir = "{{.Root}}"
	task1.After = []string{"echo 'after task1'"}

	d := 1 * time.Minute
	task2 := task.NewTask()
	task2.Timeout = &d
	task2.Variations = []map[string]string{{"GOOS": "windows"}, {"GOOS": "linux"}}

	task2.Commands = []string{"false"}
	task2.Name = "some test task"
	task2.Dir = "{{.Root}}"
	task2.Interactive = true

	task3 := task.NewTask()
	task3.Condition = "exit 1"

	task4 := task.NewTask()
	task4.Commands = []string{"function test_func() { echo \"BBB\"; } ", "test_func"}

	cases := []struct {
		t                *task.Task
		skipped, errored bool
		status           int16
		output           string
	}{
		{t: task1, output: "taskctl"},
		{t: task2, status: 1, errored: true},
		{t: task3, status: -1, skipped: true},
		{t: task4, output: "BBB"},
	}

	for _, testCase := range cases {
		err = runnr.Run(testCase.t)
		if err != nil && !testCase.errored && !testCase.skipped {
			t.Fatal(err)
		}

		if !testCase.skipped && testCase.t.Start.IsZero() {
			t.Error()
		}

		if !strings.Contains(testCase.t.Output(), testCase.output) {
			t.Error()
		}

		if testCase.errored && !testCase.t.Errored {
			t.Error()
		}

		if !testCase.errored && testCase.t.Errored {
			t.Error()
		}

		if testCase.t.ExitCode != testCase.status {
			t.Error()
		}
	}

	runnr.Finish()
}

func ExampleTaskRunner_Run() {
	t := task.FromCommands("go fmt ./...", "go build ./..")
	r, err := NewTaskRunner(nil)
	if err != nil {
		return
	}
	err = r.Run(t)
	if err != nil {
		fmt.Println(err, t.ExitCode, t.ErrorMessage())
	}
	fmt.Println(t.Output())
}
