package definition_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Flowpack/prunner/definition"
)

func TestPipelinesDef_Equals(t *testing.T) {
	def1 := definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"pipeline1": {
				Concurrency:                      1,
				QueueLimit:                       nil,
				QueueStrategy:                    definition.QueueStrategyReplace,
				StartDelay:                       2 * time.Minute,
				ContinueRunningTasksAfterFailure: false,
				RetentionPeriod:                  48 * time.Hour,
				RetentionCount:                   5,
				Env: map[string]string{
					"FOO": "BAR",
				},
				Tasks: map[string]definition.TaskDef{
					"task1": {
						Script: []string{"echo 'Hello World'"},
					},
					"task2": {
						Script:    []string{"echo 'Done'"},
						DependsOn: []string{"task1"},
					},
				},
				SourcePath: "",
			},
		},
	}

	assert.True(t, def1.Equals(def1), "Pipelines definition should be equal to itself")

	def1_a := definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"pipeline1": {
				Concurrency:                      1,
				QueueLimit:                       nil,
				QueueStrategy:                    definition.QueueStrategyReplace,
				StartDelay:                       2 * time.Minute,
				ContinueRunningTasksAfterFailure: false,
				RetentionPeriod:                  48 * time.Hour,
				RetentionCount:                   5,
				Env: map[string]string{
					"FOO": "BAR",
				},
				Tasks: map[string]definition.TaskDef{
					"task1": {
						Script: []string{"echo 'Hello Change'"},
					},
					"task2": {
						Script:    []string{"echo 'Done'"},
						DependsOn: []string{"task1"},
					},
				},
				SourcePath: "",
			},
		},
	}

	assert.False(t, def1.Equals(def1_a), "Pipelines definition should not be equal if task script has changed")

	def1_copy := definition.PipelinesDef{
		Pipelines: map[string]definition.PipelineDef{
			"pipeline1": {
				Concurrency:                      1,
				QueueLimit:                       nil,
				QueueStrategy:                    definition.QueueStrategyReplace,
				StartDelay:                       2 * time.Minute,
				ContinueRunningTasksAfterFailure: false,
				RetentionPeriod:                  48 * time.Hour,
				RetentionCount:                   5,
				Env: map[string]string{
					"FOO": "BAR",
				},
				Tasks: map[string]definition.TaskDef{
					"task1": {
						Script: []string{"echo 'Hello World'"},
					},
					"task2": {
						Script:    []string{"echo 'Done'"},
						DependsOn: []string{"task1"},
					},
				},
				SourcePath: "",
			},
		},
	}

	assert.True(t, def1.Equals(def1_copy), "Pipelines definition should be equal to copy")
}
