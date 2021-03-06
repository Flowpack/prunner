package test

import (
	"testing"
	"time"
)

func WaitForCondition(t *testing.T, f func() bool, wait time.Duration, msg string) {
	t.Helper()
	var d = 0 * time.Second
	for {
		if d > 2*time.Second {
			t.Fatalf("Timed out waiting for condition: %s", msg)
		}
		if f() {
			break
		}
		time.Sleep(wait)
		d += wait
	}
}
