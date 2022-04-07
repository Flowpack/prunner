//go:build !windows

package taskctl

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/interp"
)

func createExecHandler(killTimeout time.Duration) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
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
	}
}
