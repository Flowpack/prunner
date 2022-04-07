//go:build windows

package app

import (
	"os"
)

func notifyReloadSignal(notifyReload chan os.Signal) {
	// No op, windows does not support user signals
}
