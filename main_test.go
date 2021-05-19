package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_sortTaskResultsTopological(t *testing.T) {
	tests := []struct {
		name     string
		input    []taskResult
		expected []string
	}{
		{
			name: "no dependencies",
			input: []taskResult{
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
			input: []taskResult{
				{
					Name:      "b",
					EdgesFrom: []string{"a"},
				},
				{
					Name:    "a",
					EdgesTo: []string{"b"},
				},
			},
			expected: []string{"a", "b"},
		},
		{
			name: "complex dep",
			input: []taskResult{
				{
					Name:    "a",
					EdgesTo: []string{"d", "b", "e"},
				},
				{
					Name:      "b",
					EdgesFrom: []string{"a", "e"},
					EdgesTo:   []string{"c", "f"},
				},
				{
					Name:      "c",
					EdgesFrom: []string{"d", "b"},
					EdgesTo:   []string{"g"},
				},
				{
					Name:      "d",
					EdgesFrom: []string{"a"},
					EdgesTo:   []string{"c"},
				},
				{
					Name:      "e",
					EdgesFrom: []string{"a"},
					EdgesTo:   []string{"b", "f"},
				},
				{
					Name:      "f",
					EdgesFrom: []string{"b", "e"},
					EdgesTo:   []string{"g"},
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
			sortTaskResultsTopological(tt.input)

			var order []string
			for _, t := range tt.input {
				order = append(order, t.Name)
			}

			assert.Equal(t, tt.expected, order)
		})
	}
}
