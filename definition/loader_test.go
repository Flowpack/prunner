package definition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRecursively(t *testing.T) {
	defs, err := LoadRecursively("../test/fixtures/**/pipelines.{yml,yaml}")
	require.NoError(t, err)
	require.NotNil(t, defs)

	require.Len(t, defs.Pipelines, 3)
}

func TestLoadRecursively_WithDuplicate(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/**/{pipelines,dup}.{yml,yaml}")
	require.EqualError(t, err, `loading ../test/fixtures/pipelines.yml: pipeline "test_it" was already declared in ../test/fixtures/dup.yml`)
}

func TestLoadRecursively_WithMissingDependency(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/missingDep.yml")
	require.EqualError(t, err, `loading ../test/fixtures/missingDep.yml: missing task "not_existing" in pipeline "test_it" referenced in depends_on of task "test"`)
}
