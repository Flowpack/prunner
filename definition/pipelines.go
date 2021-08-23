package definition

import (
	"strings"
	"time"

	"github.com/friendsofgo/errors"
)

type TaskDef struct {
	// Script is a list of shell commands that are executed for this task
	Script []string `yaml:"script"`
	// DependsOn is a list of task names this task depends on (must be finished before it can start)
	DependsOn []string `yaml:"depends_on"`
	// AllowFailure should be set, if the pipeline should continue event if this task had an error
	AllowFailure bool `yaml:"allow_failure"`
}

type PipelineDef struct {
	// Concurrency declares how many instances of this pipeline are allowed to execute concurrently (defaults to 1)
	Concurrency int `yaml:"concurrency"`
	// QueueLimit is the number of slots for queueing jobs if the allowed concurrency is exceeded, defaults to unbounded (nil)
	QueueLimit *int `yaml:"queue_limit"`
	// QueueStrategy to use when adding jobs to the queue (defaults to append)
	QueueStrategy QueueStrategy `yaml:"queue_strategy"`
	// StartDelay will delay the start of a job if the value is greater than zero (defaults to 0)
	StartDelay time.Duration `yaml:"start_delay"`

	// ContinueRunningTasksAfterFailure should be set to true if you want to continue working through all jobs whose
	// predecessors have not failed. false by default; so by default, if the first job aborts, all others are terminated as well.
	ContinueRunningTasksAfterFailure bool `yaml:"continue_running_tasks_after_failure"`

	RetentionPeriod time.Duration `yaml:"retention_period"`
	RetentionCount  int           `yaml:"retention_count"`

	Tasks map[string]TaskDef `yaml:"tasks"`

	// SourcePath stores the source path where the pipeline was defined
	SourcePath string
}

func (d PipelineDef) validate() error {
	if d.Concurrency <= 0 {
		return errors.New("concurrency must be greater than 0")
	}
	if d.QueueLimit != nil && *d.QueueLimit < 0 {
		return errors.New("queue_limit must not be negative")
	}
	if d.StartDelay < 0 {
		return errors.New("start_delay must not be negative")
	}
	if d.StartDelay > 0 && d.QueueLimit != nil && *d.QueueLimit == 0 {
		return errors.New("start_delay needs queue_limit > 0")
	}

	for taskName, taskDef := range d.Tasks {
		for _, dependentTask := range taskDef.DependsOn {
			_, exists := d.Tasks[dependentTask]
			if !exists {
				return errors.Errorf("missing task %q referenced in depends_on of task %q", dependentTask, taskName)
			}
		}
	}

	return nil
}

type QueueStrategy int

const (
	// QueueStrategyAppend appends jobs to the queue until queue limit is reached
	QueueStrategyAppend QueueStrategy = 0
	// QueueStrategyReplace replaces pending jobs (with same variables) instead of appending to the queue
	QueueStrategyReplace QueueStrategy = 1
)

func (s *QueueStrategy) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var strategyName string
	err := unmarshal(&strategyName)
	if err != nil {
		return err
	}

	switch strategyName {
	case "append":
		*s = QueueStrategyAppend
	case "replace":
		*s = QueueStrategyReplace
	default:
		return errors.Errorf("unknown queue strategy: %q", strategyName)
	}

	return nil
}

type PipelinesMap map[string]PipelineDef

type PipelinesDef struct {
	Pipelines PipelinesMap `yaml:"pipelines"`
}

func (d *PipelinesDef) setDefaults() {
	for pipeline, pipelineDef := range d.Pipelines {
		// Use concurrency of 1 by default (0 is zero value and makes no sense)
		if pipelineDef.Concurrency == 0 {
			pipelineDef.Concurrency = 1
			d.Pipelines[pipeline] = pipelineDef
		}
	}
}

func (d *PipelinesDef) Validate() error {
	for pipeline, pipelineDef := range d.Pipelines {
		err := pipelineDef.validate()
		if err != nil {
			return errors.Wrapf(err, "invalid pipeline definition %q", pipeline)
		}
	}
	return nil
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
