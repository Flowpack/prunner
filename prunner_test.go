package prunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"networkteam.com/lab/prunner/definition"
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
					Name:      "b",
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
					Name:      "site_export",
					TaskDef: definition.TaskDef{DependsOn: []string{"prepare_directory"}},
				},
				{
					Name:      "build_archive",
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
					Name:      "b",
					TaskDef: definition.TaskDef{DependsOn: []string{"a", "e"}},
				},
				{
					Name:      "c",
					TaskDef: definition.TaskDef{DependsOn: []string{"d", "b"}},
				},
				{
					Name:      "d",
					TaskDef: definition.TaskDef{DependsOn: []string{"a"}},
				},
				{
					Name:      "e",
					TaskDef: definition.TaskDef{DependsOn: []string{"a"}},
				},
				{
					Name:      "f",
					TaskDef: definition.TaskDef{DependsOn: []string{"b", "e"}},
				},
				{
					Name:      "g",
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
