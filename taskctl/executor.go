package taskctl

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/apex/log"
	"github.com/taskctl/taskctl/pkg/executor"
	"mvdan.cc/sh/v3/expand"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/taskctl/taskctl/pkg/utils"
)

// PgidExecutor is an executor that starts commands in a new process group (if supported by OS)
//
// It is based on executor.DefaultExecutor but uses an exec handler that sets setpgid to
// start child processes in a new process group (with negative pid). It is used to prevent
// signals from propagating to children and to kill all descendant processes e.g. when forked by a shell process.
type PgidExecutor struct {
	dir    string
	env    []string
	interp *interp.Runner
	buf    bytes.Buffer
}

// NewPgidExecutor creates new pgid executor
func NewPgidExecutor(stdin io.Reader, stdout, stderr io.Writer, killTimeout time.Duration) (*PgidExecutor, error) {
	var err error
	e := &PgidExecutor{
		env: os.Environ(),
	}

	e.dir, err = os.Getwd()
	if err != nil {
		return nil, err
	}

	if stdout == nil {
		stdout = io.Discard
	}

	if stderr == nil {
		stderr = io.Discard
	}

	e.interp, err = interp.New(
		interp.StdIO(stdin, io.MultiWriter(&e.buf, stdout), io.MultiWriter(&e.buf, stderr)),
		interp.ExecHandler(createExecHandler(killTimeout)),
	)
	if err != nil {
		return nil, err
	}

	return e, nil
}

// Execute executes given job with provided context
// Returns job output
func (e *PgidExecutor) Execute(ctx context.Context, job *executor.Job) ([]byte, error) {
	command, err := utils.RenderString(job.Command, job.Vars.Map())
	if err != nil {
		return nil, err
	}

	cmd, err := syntax.NewParser(syntax.KeepComments(true)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, err
	}

	env := e.env
	env = append(env, utils.ConvertEnv(utils.ConvertToMapOfStrings(job.Env.Map()))...)

	if job.Dir == "" {
		job.Dir = e.dir
	}

	jobID := job.Vars.Get(JobIDVariableName).(string)

	log.
		WithField("component", "executor").
		WithField("jobID", jobID).
		Debugf("Executing %q", command)

	e.interp.Dir = job.Dir
	e.interp.Env = expand.ListEnviron(env...)

	var cancelFn context.CancelFunc
	if job.Timeout != nil {
		ctx, cancelFn = context.WithTimeout(ctx, *job.Timeout)
	}
	defer func() {
		if cancelFn != nil {
			cancelFn()
		}
	}()

	offset := e.buf.Len()
	err = e.interp.Run(ctx, cmd)
	if err != nil {
		return e.buf.Bytes()[offset:], err
	}

	return e.buf.Bytes()[offset:], nil
}

func execEnv(env expand.Environ) []string {
	list := make([]string, 0, 64)
	env.Each(func(name string, vr expand.Variable) bool {
		if !vr.IsSet() {
			// If a variable is set globally but unset in the
			// runner, we need to ensure it's not part of the final
			// list. Seems like zeroing the element is enough.
			// This is a linear search, but this scenario should be
			// rare, and the number of variables shouldn't be large.
			for i, kv := range list {
				if strings.HasPrefix(kv, name+"=") {
					list[i] = ""
				}
			}
		}
		if vr.Exported && vr.Kind == expand.String {
			list = append(list, name+"="+vr.String())
		}
		return true
	})
	return list
}
