package prunner

import (
	"context"
	"github.com/gofrs/uuid"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/taskctl"
	"github.com/Flowpack/prunner/test"
)

func TestJobTasks_sortTasksByDependencies(t *testing.T) {
	tests := []struct {
		name     string
		input    jobTasks
		expected []string
	}{
		{
			name: "no dependencies",
			input: jobTasks{
				{
					Name: "zeta",
				},
				{
					Name: "alpha",
				},
			},
			expected: []string{"alpha", "zeta"},
		},
		{
			name: "simple dep",
			input: jobTasks{
				{
					Name:    "b",
					TaskDef: definition.TaskDef{DependsOn: []string{"a"}},
				},
				{
					Name: "a",
				},
			},
			expected: []string{"a", "b"},
		},
		{
			name: "chain",
			input: jobTasks{
				{
					Name:    "site_export",
					TaskDef: definition.TaskDef{DependsOn: []string{"prepare_directory"}},
				},
				{
					Name:    "build_archive",
					TaskDef: definition.TaskDef{DependsOn: []string{"site_export"}},
				},
				{
					Name: "prepare_directory",
				},
			},
			expected: []string{"prepare_directory", "site_export", "build_archive"},
		},
		{
			name: "complex dep",
			input: jobTasks{
				{
					Name: "a",
				},
				{
					Name:    "b",
					TaskDef: definition.TaskDef{DependsOn: []string{"a", "e"}},
				},
				{
					Name:    "c",
					TaskDef: definition.TaskDef{DependsOn: []string{"d", "b"}},
				},
				{
					Name:    "d",
					TaskDef: definition.TaskDef{DependsOn: []string{"a"}},
				},
				{
					Name:    "e",
					TaskDef: definition.TaskDef{DependsOn: []string{"a"}},
				},
				{
					Name:    "f",
					TaskDef: definition.TaskDef{DependsOn: []string{"b", "e"}},
				},
				{
					Name:    "g",
					TaskDef: definition.TaskDef{DependsOn: []string{"c", "f"}},
				},
			},
			expected: []string{"a", "d", "e", "b", "c", "f", "g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.sortTasksByDependencies()

			var order []string
			for _, t := range tt.input {
				order = append(order, t.Name)
			}

			assert.Equal(t, tt.expected, order)
		})
	}
}

func TestPipelineRunner_ScheduleAsync_WithEmptyScriptTask(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"empty_script": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"a": {
						Script: []string{"echo A"},
					},
					"b": {
						Script: []string{"echo B"},
					},
					"c": {
						Script: []string{"echo C"},
					},
					"wait": {
						DependsOn: []string{"a", "b", "c"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pRunner, err := NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("empty_script", ScheduleOpts{})
	require.NoError(t, err)

	waitForCompletedJob(t, pRunner, job.ID)
}

func TestPipelineRunner_CancelJob_WithRunningJob(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"sleep": {
						Script: []string{"sleep 10"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pRunner, err := NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForStartedJob(t, pRunner, jobID)

	err = pRunner.CancelJob(jobID)
	require.NoError(t, err)

	waitForCompletedJob(t, pRunner, jobID)

	assert.True(t, job.Canceled, "job was marked as canceled")
	jt := job.Tasks.ByName("sleep")
	if assert.NotNil(t, jt) {
		assert.True(t, jt.Canceled, "task was marked as canceled")
		assert.False(t, jt.Errored, "task was not marked as errored")
		assert.Equal(t, "canceled", jt.Status, "task has status canceled")
		assert.Nil(t, jt.Error, "task has no error set")
	}
}

func TestPipelineRunner_CancelJob_WithStoppedJob_ShouldNotThrowFatalError(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"sleep": {
						Script: []string{"sleep 10"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockStore()

	pRunner, err := NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForStartedJob(t, pRunner, jobID)

	pRunner.SaveToStore()

	// now, we start a NEW prunner instance with the same store,
	// to ensure we reach an inconsistent state (no taskRunner set).
	pRunner, err = NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store)
	require.NoError(t, err)

	err = pRunner.CancelJob(jobID)
	require.NoError(t, err)
	// cancelJob triggers a goroutine to do the actual cancel; so we need to wait a bit to see the goroutine fail with a FATAL
	time.Sleep(1 * time.Second)
}

func TestPipelineRunner_FirstErroredTaskShouldCancelAllRunningTasks_ByDefault(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running_with_error": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"err": {
						Script: []string{"sleep 1; exit 1"},
					},
					"sleep": {
						Script: []string{"sleep 2"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pRunner, err := NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running_with_error", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID
	waitForStartedJob(t, pRunner, jobID)

	// we wait for the "err" task to fail.
	test.WaitForCondition(t, func() bool {
		var errored bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			errored = j.Tasks.ByName("err").Errored
		})
		return errored
	}, 50*time.Millisecond, "first task errored as expected")
	assert.True(t, job.Tasks.ByName("err").Errored, "err task was errored")
	assert.True(t, job.Tasks.ByName("sleep").Canceled, "sleep task should be cancelled")
}

func TestPipelineRunner_FirstErroredTaskShouldNotCancelAllOtherRunningTasks_IfConfigured(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running_with_error": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency:                      1,
				QueueLimit:                       nil,
				ContinueRunningTasksAfterFailure: true,
				Tasks: map[string]definition.TaskDef{
					"err": {
						Script: []string{"sleep 1; exit 1"},
					},
					"sleep": {
						Script: []string{"sleep 2"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pRunner, err := NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running_with_error", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID
	waitForStartedJob(t, pRunner, jobID)

	// we wait for the "err" task to fail.
	test.WaitForCondition(t, func() bool {
		var errored bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			errored = j.Tasks.ByName("err").Errored
		})
		return errored
	}, 50*time.Millisecond, "first task errored as expected")

	time.Sleep(3 * time.Second)
	assert.True(t, job.Tasks.ByName("err").Errored, "err task was errored")
	assert.Equal(t, "done", job.Tasks.ByName("sleep").Status, "sleep task should be done until the end")
	// TODO: I am not sure if the state here is correct
	assert.True(t, job.Completed, "job should be marked as completed")
	assert.False(t, job.Canceled, "job should not be marked as canceled")
}

func waitForStartedJob(t *testing.T, pRunner *PipelineRunner, jobID uuid.UUID) {
	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			started = j.Tasks.ByName("sleep").Start != nil
		})
		return started
	}, 1*time.Millisecond, "task started")
}

func waitForCompletedJob(t *testing.T, pRunner *PipelineRunner, jobID uuid.UUID) {
	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 1*time.Millisecond, "job completed")
}
