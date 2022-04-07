//go:build windows

package taskctl

import (
	"time"

	"mvdan.cc/sh/v3/interp"
)

func createExecHandler(killTimeout time.Duration) interp.ExecHandlerFunc {
	return interp.DefaultExecHandler(killTimeout)
}
