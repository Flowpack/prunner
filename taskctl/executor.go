package taskctl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/apex/log"
	"github.com/taskctl/taskctl/pkg/executor"
	"mvdan.cc/sh/v3/expand"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/taskctl/taskctl/pkg/utils"
)

// PgidExecutor is an executor that starts commands in a new process group
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
		interp.ExecHandler(func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
			if err != nil {
				_, _ = fmt.Fprintln(hc.Stderr, err)
				return interp.NewExitStatus(127)
			}
			cmd := exec.Cmd{
				Path:   path,
				Args:   args,
				Env:    execEnv(hc.Env),
				Dir:    hc.Dir,
				Stdin:  hc.Stdin,
				Stdout: hc.Stdout,
				Stderr: hc.Stderr,
				SysProcAttr: &syscall.SysProcAttr{
					// Added for prunner: Using a process group for the new child process fixes 2 things:
					// 1. We can handle signals like SIGINT in the prunner main process and gracefully shutdown running tasks
					// 2. Bash scripts will not forward signals by default, so using process groups can be used to send signals to all children in the group
					//    (https://unix.stackexchange.com/questions/146756/forward-sigterm-to-child-in-bash)
					Setpgid: true,
				},
			}

			err = cmd.Start()
			if err == nil {
				if done := ctx.Done(); done != nil {
					go func() {
						<-done

						if killTimeout <= 0 {
							_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
							return
						}

						// TODO: don't temporarily leak this goroutine if the program stops itself with the interrupt. (from github.com/mvdan/sh)
						go func() {
							time.Sleep(killTimeout)
							_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						}()
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
					}()
				}

				err = cmd.Wait()
			}

			switch x := err.(type) {
			case *exec.ExitError:
				// started, but errored - default to 1 if OS doesn't have exit statuses
				if status, ok := x.Sys().(syscall.WaitStatus); ok {
					if status.Signaled() {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						return interp.NewExitStatus(uint8(128 + status.Signal()))
					}
					return interp.NewExitStatus(uint8(status.ExitStatus()))
				}
				return interp.NewExitStatus(1)
			case *exec.Error:
				// did not start
				_, _ = fmt.Fprintf(hc.Stderr, "%v\n", err)
				return interp.NewExitStatus(127)
			default:
				return err
			}
		}),
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
		Debugf("Executing \"%s\"", command)

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
