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
	require.EqualError(t, err, `loading ../test/fixtures/missingDep.yml: invalid pipeline definition "test_it": missing task "not_existing" referenced in depends_on of task "test"`)
}

func TestPartitionedWaitlist_OK(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/partitioned_waitlist.yml")
	require.NoError(t, err)
}

func TestPartitionedWaitlist_err_0_partition_limit(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/partitioned_waitlist_err_0_partition_limit.yml")
	require.EqualError(t, err, `loading ../test/fixtures/partitioned_waitlist_err_0_partition_limit.yml: invalid pipeline definition "test_it": queue_partition_limit must be defined and >=1 if queue_strategy=partitioned_replace`)
}

func TestPartitionedWaitlist_err_no_partition_limit(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/partitioned_waitlist_err_no_partition_limit.yml")
	require.EqualError(t, err, `loading ../test/fixtures/partitioned_waitlist_err_no_partition_limit.yml: invalid pipeline definition "test_it": queue_partition_limit must be defined and >=1 if queue_strategy=partitioned_replace`)
}

func TestPartitionedWaitlist_err_queue_limit(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/partitioned_waitlist_err_queue_limit.yml")
	require.EqualError(t, err, `loading ../test/fixtures/partitioned_waitlist_err_queue_limit.yml: invalid pipeline definition "test_it": queue_limit is not allowed if queue_strategy=partitioned_replace, use queue_partition_limit instead`)
}

func TestWaitlist_err_partitioned_queue_limit(t *testing.T) {
	_, err := LoadRecursively("../test/fixtures/waitlist_err_partitioned_queue_limit.yml")
	require.EqualError(t, err, `loading ../test/fixtures/waitlist_err_partitioned_queue_limit.yml: invalid pipeline definition "test_it": queue_partition_limit is not allowed if queue_strategy=append|replace, use queue_limit instead`)
}
