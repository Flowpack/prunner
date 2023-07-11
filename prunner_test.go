package prunner

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/gofrs/uuid"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/taskctl"
	"github.com/Flowpack/prunner/test"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

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

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil, test.NewMockOutputStore())
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("empty_script", ScheduleOpts{})
	require.NoError(t, err)

	waitForCompletedJob(t, pRunner, job.ID)
}

func TestPipelineRunner_ScheduleAsync_WithEnvVars(t *testing.T) {
	// Make sure process env vars are used up as well
	err := os.Setenv("GLOBAL_VAR", "from process")
	require.NoError(t, err)

	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"env_vars": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Env: map[string]string{
					"MY_VAR":    "from pipeline",
					"OTHER_VAR": "from pipeline",
				},
				Tasks: map[string]definition.TaskDef{
					"pipeline_var": {
						Script: []string{"echo -n \"$MY_VAR,$OTHER_VAR,$GLOBAL_VAR\""},
					},
					"task_var": {
						Env: map[string]string{
							"MY_VAR": "from task",
						},
						Script: []string{"echo -n \"$MY_VAR,$OTHER_VAR,$GLOBAL_VAR\""},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockOutputStore()
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(store, taskctl.WithEnv(variables.FromMap(j.Env)))
		return taskRunner
	}, nil, store)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("env_vars", ScheduleOpts{
		Variables: map[string]interface{}{
			"test_var": "test",
		},
	})
	require.NoError(t, err)

	waitForCompletedJob(t, pRunner, job.ID)

	pipelineVarTaskOutput := store.GetBytes(job.ID.String(), "pipeline_var", "stdout")
	assert.Equal(t, "from pipeline,from pipeline,from process", string(pipelineVarTaskOutput), "output of pipeline_var")

	taskVarTaskOutput := store.GetBytes(job.ID.String(), "task_var", "stdout")
	assert.Equal(t, "from task,from pipeline,from process", string(taskVarTaskOutput), "output of task_var")
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

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, nil, test.NewMockOutputStore())
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForStartedJobTask(t, pRunner, jobID, "sleep")

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

func TestPipelineRunner_CancelJob_WithQueuedJob(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"sleep": {
						Script: []string{"# that takes long"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		startedJobsIDs []string
		wait           = make(chan struct{})
	)

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				jobID := t.Variables.Get(taskctl.JobIDVariableName)
				startedJobsIDs = append(startedJobsIDs, jobID.(string))

				// Wait until the job should proceed (wait channel is closed)
				<-wait

				return nil
			},
		}
	}, nil, test.NewMockOutputStore())
	require.NoError(t, err)

	job1, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	job2, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	waitForStartedJobTask(t, pRunner, job1.ID, "sleep")

	// Make sure the queued job can be canceled
	err = pRunner.CancelJob(job2.ID)
	require.NoError(t, err)

	// Close the channel to let the first job proceed
	close(wait)

	waitForCompletedJob(t, pRunner, job1.ID)
	waitForCanceledJob(t, pRunner, job2.ID)

	assert.Equal(t, true, job2.Tasks.ByName("sleep").Canceled, "job task was marked as canceled")

	assert.Equal(t, []string{job1.ID.String()}, startedJobsIDs, "only job1 was started")

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
	store.Set([]byte(`{"Jobs":[{"ID":"72b01fe2-c090-499f-a7b4-a4ff530bf11b","Pipeline":"long_running","Created":"2021-08-23T15:41:09.37212+02:00","Start":"2021-08-23T15:41:09.372149+02:00","Tasks":[{"Name":"sleep","Script":["sleep 10"],"Status":"running","Start":"2021-08-23T15:41:09.372455+02:00","ExitCode":-1}]}]}`))

	jobID := uuid.FromStringOrNil("72b01fe2-c090-499f-a7b4-a4ff530bf11b")

	// now, we start a NEW prunner instance with the same store,
	// to ensure we reach an inconsistent state (no taskRunner set).
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)

	err = pRunner.CancelJob(jobID)
	require.NoError(t, err)

	// Wait until the cancel operation is done
	pRunner.wg.Wait()
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
						Script: []string{"exit 1"},
					},
					"ok": {
						Script: []string{"do_something_long"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				if t.Name == "err" {
					t.Errored = true
					t.Error = errors.New("exit 1")
				}

				if t.Name == "ok" {
					// Wait until cancel occurs and simulate a canceled error from this task
					<-ctx.Done()

					t.Errored = true
					t.Error = context.Canceled
				}

				return t.Error
			},
			OnCancel: func() {
				// We need to actively cancel the context here
				cancel()
			},
		}
	}, nil, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running_with_error", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForCompletedJob(t, pRunner, jobID)

	assert.True(t, job.Tasks.ByName("err").Errored, "err task was errored")
	assert.True(t, job.Tasks.ByName("ok").Canceled, "ok task should be cancelled")
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
						Script: []string{"exit 1"},
					},
					"ok": {
						Script: []string{"do_something_longer"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var canceled bool

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				if t.Name == "err" {
					t.Errored = true
					t.Error = errors.New("exit 1")
					return t.Error
				}

				return nil
			},
			OnCancel: func() {
				canceled = true
			},
		}
	}, nil, nil)
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running_with_error", ScheduleOpts{})
	require.NoError(t, err)

	waitForCompletedJob(t, pRunner, job.ID)

	require.False(t, canceled, "task runner should not be canceled")

	assert.True(t, job.Tasks.ByName("err").Errored, "err task was errored")
	assert.Equal(t, "done", job.Tasks.ByName("ok").Status, "ok task should be done until the end")
	assert.True(t, job.Completed, "job should be marked as completed")
	assert.False(t, job.Canceled, "job should not be marked as canceled")
	assert.NotNil(t, job.LastError, "job should have last error")
}

func waitForStartedJobTask(t *testing.T, pRunner *PipelineRunner, jobID uuid.UUID, taskName string) {
	t.Helper()

	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			tsk := j.Tasks.ByName(taskName)
			if tsk != nil {
				started = tsk.Start != nil
			}
		})
		return started
	}, 1*time.Millisecond, "task started")
}

func waitForCompletedJob(t *testing.T, pRunner *PipelineRunner, jobID uuid.UUID) {
	t.Helper()

	test.WaitForCondition(t, func() bool {
		var completed bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			completed = j.Completed
		})
		return completed
	}, 1*time.Millisecond, "job completed")
}

func waitForCanceledJob(t *testing.T, pRunner *PipelineRunner, jobID uuid.UUID) {
	t.Helper()

	test.WaitForCondition(t, func() bool {
		var canceled bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			canceled = j.Canceled
		})
		return canceled
	}, 1*time.Millisecond, "job canceled")
}

func TestPipelineRunner_ShouldRemoveOldJobsWhenRetentionPeriodIsConfigured(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"jobWithRetentionCount": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency:    1,
				QueueLimit:     nil,
				RetentionCount: 1,
				Tasks: map[string]definition.TaskDef{
					"echo": {
						Script: []string{"echo a"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockStore()
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("jobWithRetentionCount", ScheduleOpts{})
	require.NoError(t, err)

	assert.Len(t, pRunner.jobsByID, 1, "jobsById internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline, 1, "jobsByPipeline internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline["jobWithRetentionCount"], 1, "jobsByPipeline[jobWithRetentionCount] internal count mismatch")

	waitForCompletedJob(t, pRunner, job.ID)

	job2, err := pRunner.ScheduleAsync("jobWithRetentionCount", ScheduleOpts{})
	require.NoError(t, err)

	// this triggers the compraction
	pRunner.SaveToStore()

	assert.Contains(t, pRunner.jobsByID, job2.ID)
	// Job 1 has been cleaned up
	assert.NotContains(t, pRunner.jobsByID, job.ID)

	// we still have one job in the system only (job2)
	assert.Len(t, pRunner.jobsByID, 1, "jobsById internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline, 1, "jobsByPipeline internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline["jobWithRetentionCount"], 1, "jobsByPipeline[jobWithRetentionCount] internal count mismatch")

	// now, we instantiate a NEW prunner instance from the same store, ensuring we have again only the right job in there (the latest one). This
	// tests that we compacted not only the in-memory representation, but also the on-disk one.
	pRunner2, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)
	assert.Len(t, pRunner2.jobsByID, 1, "jobsById internal count mismatch")
	assert.Len(t, pRunner2.jobsByPipeline, 1, "jobsByPipeline internal count mismatch")
	assert.Len(t, pRunner2.jobsByPipeline["jobWithRetentionCount"], 1, "jobsByPipeline[jobWithRetentionCount] internal count mismatch")
}

func TestPipelineRunner_ShouldNotRemoveStillRunningJobsEvenIfRetentionPeriodIsViolated(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"jobWithRetentionCount": {
				RetentionCount: 1,
				Concurrency:    100,
				Tasks: map[string]definition.TaskDef{
					"echo": {
						Script: []string{"sleep 1"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	store := test.NewMockStore()
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(t *task.Task) error {
				// Wait until wait group is marked as done
				wg.Wait()
				return nil
			},
		}
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)

	// Increment wait count
	wg.Add(1)

	job, err := pRunner.ScheduleAsync("jobWithRetentionCount", ScheduleOpts{})
	require.NoError(t, err)
	job2, err := pRunner.ScheduleAsync("jobWithRetentionCount", ScheduleOpts{})
	require.NoError(t, err)

	// This triggers the compaction. For running jobs, this should not do anything.
	pRunner.SaveToStore()

	assert.Len(t, pRunner.jobsByID, 2, "jobsById internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline, 1, "jobsByPipeline internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline["jobWithRetentionCount"], 2, "jobsByPipeline[jobWithRetentionCount] internal count mismatch")

	// Mark jobs as finished - finishes tasks in mock runner
	wg.Done()

	// Wait until jobs are seen as finished
	waitForCompletedJob(t, pRunner, job.ID)
	waitForCompletedJob(t, pRunner, job2.ID)

	// This triggers the compaction. As our jobs are finished now, only the job2 should be kept.
	pRunner.SaveToStore()

	assert.Len(t, pRunner.jobsByID, 1, "jobsById internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline, 1, "jobsByPipeline internal count mismatch")
	assert.Len(t, pRunner.jobsByPipeline["jobWithRetentionCount"], 1, "jobsByPipeline[jobWithRetentionCount] internal count mismatch")

	assert.Contains(t, pRunner.jobsByID, job2.ID)
	// Job 1 has been cleaned up
	assert.NotContains(t, pRunner.jobsByID, job.ID)
}

func TestPipelineRunner_TimeBasedRetentionPolicyCalculatesCorrectly(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"jobWithRetentionCount": {
				RetentionPeriod: 1 * time.Hour,
				Concurrency:     100,
				Tasks: map[string]definition.TaskDef{
					"echo": {
						Script: []string{"echo Test"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{}
	}, nil, nil)
	require.NoError(t, err)

	///////////////////
	// Testcases Follow Here
	///////////////////
	ti := time.Now()
	shouldRemove, reason := pRunner.determineIfJobShouldBeRemoved(0, &PipelineJob{
		Pipeline: "jobWithRetentionCount",
		Created:  time.Now(),
		Start:    &ti,
		Canceled: true,
	})
	require.Equal(t, "", reason)
	require.False(t, shouldRemove)

	shouldRemove, reason = pRunner.determineIfJobShouldBeRemoved(0, &PipelineJob{
		Pipeline: "jobWithRetentionCount",
		Created:  time.Now().Add(-2 * time.Hour),
		Start:    &ti,
		Canceled: true,
	})
	require.Equal(t, "Retention period of 1h0m0s reached", reason)
	require.True(t, shouldRemove)
}

func TestPipelineRunner_ScheduleAsync_WithStartDelayNoQueueAndReplaceWillQueueSingleJob(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"jobWithStartDelay": {
				Concurrency:   1,
				StartDelay:    50 * time.Millisecond,
				QueueLimit:    intPtr(1),
				QueueStrategy: definition.QueueStrategyReplace,
				Tasks: map[string]definition.TaskDef{
					"echo": {
						Script: []string{"echo Test"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}
	require.NoError(t, defs.Validate())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockStore()
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(tsk *task.Task) error {
				log.Debugf("Run task %s on job %s", tsk.Name, j.ID.String())
				return nil
			},
		}
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("jobWithStartDelay", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID
	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			started = j.Start != nil
		})
		return !started
	}, 1*time.Millisecond, "job is not started (queued)")

	// This job should replace the first job
	job2, err := pRunner.ScheduleAsync("jobWithStartDelay", ScheduleOpts{})
	require.NoError(t, err)

	test.WaitForCondition(t, func() bool {
		var canceled bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			canceled = j.Canceled
		})
		return canceled
	}, 1*time.Millisecond, "job is canceled")

	job2ID := job2.ID
	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(job2ID, func(j *PipelineJob) {
			started = j.Start != nil
		})
		return !started
	}, 1*time.Millisecond, "job2 is not started (queued)")

	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(job2ID, func(j *PipelineJob) {
			started = j.Start != nil
		})
		return started
	}, 1*time.Millisecond, "job2 is started")

	// Wait until all jobs are finished
	test.WaitForCondition(t, func() bool {
		var running bool
		_ = pRunner.ReadJob(job2ID, func(j *PipelineJob) {
			running = j.isRunning()
		})
		return !running
	}, 1*time.Millisecond, "job2 is finished")
}

func TestPipelineRunner_ScheduleAsync_WithStartDelayNoQueueAndReplaceWillNotRunConcurrently(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"jobWithStartDelay": {
				Concurrency:   1,
				StartDelay:    50 * time.Millisecond,
				QueueLimit:    intPtr(1),
				QueueStrategy: definition.QueueStrategyReplace,
				Tasks: map[string]definition.TaskDef{
					"echo": {
						Script: []string{"echo Test"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}
	require.NoError(t, defs.Validate())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockStore()
	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(tsk *task.Task) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		}
	}, store, test.NewMockOutputStore())
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("jobWithStartDelay", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID
	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			started = j.Start != nil
		})
		return started
	}, 1*time.Millisecond, "job is started")

	// This job should queue and not start until the first one is finished!
	job2, err := pRunner.ScheduleAsync("jobWithStartDelay", ScheduleOpts{})
	require.NoError(t, err)

	job2ID := job2.ID
	test.WaitForCondition(t, func() bool {
		var started bool
		_ = pRunner.ReadJob(job2ID, func(j *PipelineJob) {
			started = j.Start != nil
		})
		return started
	}, 1*time.Millisecond, "job2 is started")

	{
		var running bool
		_ = pRunner.ReadJob(jobID, func(j *PipelineJob) {
			running = j.isRunning()
		})
		require.False(t, running, "job1 must not be running after job2 started")
	}
}

func TestPipelineRunner_Shutdown_WithRunningJob_Graceful(t *testing.T) {
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

	var (
		jobFinished bool
		jobCanceled bool
	)

	store := test.NewMockStore()

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		return &test.MockRunner{
			OnRun: func(tsk *task.Task) error {
				time.Sleep(100 * time.Millisecond)
				jobFinished = true
				return nil
			},
			OnCancel: func() {
				jobCanceled = true
			},
		}
	}, store, test.NewMockOutputStore())
	pRunner.ShutdownPollInterval = 100 * time.Millisecond
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForStartedJobTask(t, pRunner, jobID, "sleep")

	shutdownCtx := context.Background()

	err = pRunner.Shutdown(shutdownCtx)
	require.NoError(t, err)

	assert.False(t, job.Canceled, "job was not marked as canceled")
	assert.True(t, jobFinished, "job was finished by runner")
	assert.False(t, jobCanceled, "job was not canceled by runner")
	assert.True(t, job.Completed, "job was marked as completed")
}

func TestPipelineRunner_Shutdown_WithRunningJob_Forced(t *testing.T) {
	var defs = &definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"long_running": {
				// Concurrency of 1 is the default for a single concurrent execution
				Concurrency: 1,
				QueueLimit:  nil,
				Tasks: map[string]definition.TaskDef{
					"sleep": {
						Script: []string{"sleep 1"},
					},
				},
				SourcePath: "fixtures",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := test.NewMockStore()

	pRunner, err := NewPipelineRunner(ctx, defs, func(j *PipelineJob) taskctl.Runner {
		// Use a real runner here to test the actual processing of a task.Task
		taskRunner, _ := taskctl.NewTaskRunner(test.NewMockOutputStore())
		return taskRunner
	}, store, test.NewMockOutputStore())
	pRunner.ShutdownPollInterval = 100 * time.Millisecond
	require.NoError(t, err)

	job, err := pRunner.ScheduleAsync("long_running", ScheduleOpts{})
	require.NoError(t, err)

	jobID := job.ID

	waitForStartedJobTask(t, pRunner, jobID, "sleep")

	// Force cancel of job after 100ms
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shutdownCancel()

	err = pRunner.Shutdown(shutdownCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	assert.True(t, job.Canceled, "job was marked as canceled")
}

func intPtr(i int) *int {
	return &i
}
