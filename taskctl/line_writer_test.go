package taskctl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineWriter_Write(t *testing.T) {
	lw := &LineWriter{}

	_, err := lw.Write([]byte("Line 1\nLine 2\nStart of l"))
	assert.NoError(t, err)

	assert.Equal(t, [][]byte{
		[]byte("Line 1"),
		[]byte("Line 2"),
	}, lw.lines)

	_, err = lw.Write([]byte("ine 3\nLine 4\n"))
	assert.NoError(t, err)

	assert.Equal(t, [][]byte{
		[]byte("Line 1"),
		[]byte("Line 2"),
		[]byte("Start of line 3"),
		[]byte("Line 4"),
	}, lw.lines)

	_, err = lw.Write([]byte("\nLast line"))
	assert.NoError(t, err)

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
