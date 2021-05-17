package definition

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

type PipelinesDef struct {
	Pipelines map[string]PipelineDef `yaml:"pipelines"`
}
