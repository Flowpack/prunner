package prunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_sortTaskNodesTopological(t *testing.T) {
	tests := []struct {
		name     string
		input    []taskNode
		expected []string
	}{
		{
			name: "no dependencies",
			input: []taskNode{
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
			input: []taskNode{
				{
					Name:      "b",
					EdgesFrom: []string{"a"},
				},
				{
					Name: "a",
				},
			},
			expected: []string{"a", "b"},
		},
		{
			name: "chain",
			input: []taskNode{
				{
					Name:      "site_export",
					EdgesFrom: []string{"prepare_directory"},
				},
				{
					Name:      "build_archive",
					EdgesFrom: []string{"site_export"},
				},
				{
					Name: "prepare_directory",
				},
			},
			expected: []string{"prepare_directory", "site_export", "build_archive"},
		},
		{
			name: "complex dep",
			input: []taskNode{
				{
					Name: "a",
				},
				{
					Name:      "b",
					EdgesFrom: []string{"a", "e"},
				},
				{
					Name:      "c",
					EdgesFrom: []string{"d", "b"},
				},
				{
					Name:      "d",
					EdgesFrom: []string{"a"},
				},
				{
					Name:      "e",
					EdgesFrom: []string{"a"},
				},
				{
					Name:      "f",
					EdgesFrom: []string{"b", "e"},
				},
				{
					Name:      "g",
					EdgesFrom: []string{"c", "f"},
				},
			},
			expected: []string{"a", "d", "e", "b", "c", "f", "g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortTaskNodesTopological(tt.input)

			var order []string
			for _, t := range tt.input {
				order = append(order, t.Name)
			}

			assert.Equal(t, tt.expected, order)
		})
	}
}
