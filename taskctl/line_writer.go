package taskctl

import (
	"bufio"
	"sync"
)

// LineWriter splits written data into lines
// This is not used yet but prepared for streaming output later.
type LineWriter struct {
	mx        sync.RWMutex
	lines     [][]byte
	remainder []byte
}

func (l *LineWriter) Write(p []byte) (n int, err error) {
	l.mx.Lock()
	defer l.mx.Unlock()

	var (
		i     int
		adv   int
		token []byte
	)
	for {
		adv, token, err = bufio.ScanLines(p[i:], false)
		if err != nil {
			return n, nil
		}

		if adv == 0 {
			break
		}

		line := append(l.remainder, token...)
		l.remainder = nil
		l.lines = append(l.lines, line)

		i += adv
	}

	l.remainder = append(l.remainder, p[i:]...)

	return len(p), nil
}

func (l *LineWriter) Lines() [][]byte {
	l.mx.RLock()
	defer l.mx.RUnlock()

	return l.lines
}

func (l *LineWriter) Finish() {
	l.mx.Lock()
	defer l.mx.Unlock()

	if l.remainder != nil {
		l.lines = append(l.lines, l.remainder)
		l.remainder = nil
	}
}
