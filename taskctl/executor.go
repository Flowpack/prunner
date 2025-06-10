package taskctl

import (
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
		// BEGIN MODIFICATION compared to taskctl/pkg/executor/executor.go

		// in the original TaskRunner code, stdout and stderr are also stored in a Buffer inside e; and then
		// returned as result of Execute(). This was needed to support the {{.Output}} variable in taskctl,
		// which contained the output of the previous steps.
		//
		// For prunner, this lead to a huge memory leak, where all output was always kept in RAM.
		//
		// As we do not need to support the {{.Output}} feature, we can directly send the content to
		// the stdout/stderr files.
		interp.StdIO(stdin, stdout, stderr),
		// we use a custom ExecHandler for overriding the process group handling
		interp.ExecHandler(createExecHandler(killTimeout)), //nolint:staticcheck

		// END MODIFICATION
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

	// BEGIN MODIFICATION compared to taskctl/pkg/executor/executor.go
	jobID := job.Vars.Get(JobIDVariableName).(string)

	log.
		WithField("component", "executor").
		WithField("jobID", jobID).
		Debugf("Executing %q", command)
	// END MODIFICATION

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

	// BEGIN MODIFICATION compared to taskctl/pkg/executor/executor.go
	// we disable returning the contents as byte array (needed for {{.Output}} support of
	// TaskCTL, which we removed because of Memory Leaks)
	err = e.interp.Run(ctx, cmd)
	if err != nil {
		return []byte{}, err
	}

	return []byte{}, nil
	// END MODIFICATION
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
