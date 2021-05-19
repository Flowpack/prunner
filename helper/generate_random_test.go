package helper_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"networkteam.com/lab/prunner/helper"
)

func TestGenerateRandomString(t *testing.T) {
	_, err := helper.GenerateRandomString(0)
	require.Error(t, err, "zero length not supported")

	s, err := helper.GenerateRandomString(8)
	require.NoError(t, err)
	assert.Len(t, s, 8)

	s, err = helper.GenerateRandomString(24)
	require.NoError(t, err)
	assert.Len(t, s, 24)
}
