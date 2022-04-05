package taskctl_test

import (
	"bytes"
	"context"
	"io/ioutil"
	"testing"
	"time"

	"github.com/taskctl/taskctl/pkg/executor"

	"github.com/Flowpack/prunner/taskctl"
)

func TestPgidExecutor_Execute(t *testing.T) {
	e, err := taskctl.NewPgidExecutor(nil, ioutil.Discard, ioutil.Discard, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	job1 := executor.NewJobFromCommand("echo 'success'")
	to := 1 * time.Minute
	job1.Timeout = &to

	output, err := e.Execute(context.Background(), job1)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(output, []byte("success")) {
		t.Error()
	}

	job1 = executor.NewJobFromCommand("exit 1")

	_, err = e.Execute(context.Background(), job1)
	if err == nil {
		t.Error()
	}

	if _, ok := executor.IsExitStatus(err); !ok {
		t.Error()
	}

	job2 := executor.NewJobFromCommand("echo {{ .Fail }}")
	_, err = e.Execute(context.Background(), job2)
	if err == nil {
		t.Error()
	}

	job3 := executor.NewJobFromCommand("printf '%s\\nLine-2\\n' '=========== Line 1 ==================' ")
	_, err = e.Execute(context.Background(), job3)
	if err != nil {
		t.Error()
	}
}
