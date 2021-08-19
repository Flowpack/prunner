package prunner

import (
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestSortJobsByCreationDate_ShouldSortDescending(t *testing.T) {
	var t1 = time.Date(2020, 05, 03, 0, 0, 0, 0, time.UTC)
	var u1 = uuid.FromStringOrNil("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	var t2 = time.Date(2020, 05, 04, 0, 0, 0, 0, time.UTC)
	var u2 = uuid.FromStringOrNil("7ba7b810-9dad-11d1-80b4-00c04fd430c9")
	var t3 = time.Date(2020, 05, 05, 0, 0, 0, 0, time.UTC)
	var u3 = uuid.FromStringOrNil("8ba7b810-9dad-11d1-80b4-00c04fd430c0")
	var jobs = []*PipelineJob{
		&PipelineJob{
			ID:      u1,
			Created: t1,
		},
		&PipelineJob{
			ID:      u2,
			Created: t2,
		},
		&PipelineJob{
			ID:      u3,
			Created: t3,
		},
	}

	pipelineJobBy(byCreationTimeDesc).Sort(jobs)

	assert.Equal(t, u3, jobs[0].ID, "Jobs[0] mismatch")
	assert.Equal(t, u2, jobs[1].ID, "Jobs[1] mismatch")
	assert.Equal(t, u1, jobs[2].ID, "Jobs[2] mismatch")
}
