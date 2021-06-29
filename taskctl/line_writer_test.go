package taskctl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineWriter_Write(t *testing.T) {
	lw := &LineWriter{}

	lw.Write([]byte("Line 1\nLine 2\nStart of l"))

	assert.Equal(t, [][]byte{
		[]byte("Line 1"),
		[]byte("Line 2"),
	}, lw.lines)

	lw.Write([]byte("ine 3\nLine 4\n"))

	assert.Equal(t, [][]byte{
		[]byte("Line 1"),
		[]byte("Line 2"),
		[]byte("Start of line 3"),
		[]byte("Line 4"),
	}, lw.lines)

	lw.Write([]byte("\nLast line"))

	lw.Finish()

	assert.Equal(t, [][]byte{
		[]byte("Line 1"),
		[]byte("Line 2"),
		[]byte("Start of line 3"),
		[]byte("Line 4"),
		nil,
		[]byte("Last line"),
	}, lw.lines)
}
