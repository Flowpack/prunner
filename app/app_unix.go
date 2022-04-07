//go:build !windows

package app

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyReloadSignal(notifyReload chan os.Signal) {
	signal.Notify(notifyReload, syscall.SIGUSR1)
}
