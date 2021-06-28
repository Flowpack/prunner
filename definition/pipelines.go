package definition

import (
	"strings"
)

type TaskDef struct {
	Script       []string `yaml:"script"`
	DependsOn    []string `yaml:"depends_on"`
	AllowFailure bool     `yaml:"allow_failure"`
}

type PipelineDef struct {
	Concurrent bool `yaml:"concurrent"`

	Tasks map[string]TaskDef `yaml:"tasks"`

	// SourcePath stores the source path where the pipeline was defined
	SourcePath string
}

type PipelinesMap map[string]PipelineDef

type PipelinesDef struct {
	Pipelines PipelinesMap `yaml:"pipelines"`
}

type KeyValue map[string]string

func (m PipelinesMap) NamesWithSourcePath() KeyValue {
	result := make(map[string]string, len(m))
	for name, def := range m {
		result[name] = def.SourcePath
	}
	return result
}

func (kv KeyValue) String() string {
	result := make([]string, 0, len(kv))
	for k, v := range kv {
		result = append(result, k+": "+v)
	}

	return strings.Join(result, ", ")
}
