package prunner

import (
	"context"
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

	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(job.ID, func(j *PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 50*time.Millisecond, "job completed")
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

	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			started = j.Tasks.ByName("sleep").Start != nil
		})
		return started
	}, 1*time.Millisecond, "task started")

	err = pRunner.CancelJob(jobID)
	require.NoError(t, err)

	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 1*time.Millisecond, "job completed")

	assert.True(t, job.Canceled, "job was marked as canceled")
	jt := job.Tasks.ByName("sleep")
	if assert.NotNil(t, jt) {
		assert.True(t, jt.Canceled, "task was marked as canceled")
		assert.False(t, jt.Errored, "task was not marked as errored")
		assert.Equal(t, "canceled", jt.Status, "task has status canceled")
		assert.Nil(t, jt.Error, "task has no error set")
	}
}

func TestPipelineRunner_CancelJob_WithStoppedJob(t *testing.T) {
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

	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			started = j.Tasks.ByName("sleep").Start != nil
		})
		return started
	}, 1*time.Millisecond, "task started")

	pRunner.SaveToStore()

	// now, we start a NEW prunner instance with the same store,
	// to ensure we reach an inconsistent state (no taskRunner set).
	pRunner, err = NewPipelineRunner(ctx, defs, func() taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store)
	require.NoError(t, err)

	pRunner.CancelJob(jobID)
	// cancelJob triggers a goroutine to do the actual cancel; so we need to wait a bit to see the goroutine fail with a FATAL
	time.Sleep(1 * time.Second)
}
