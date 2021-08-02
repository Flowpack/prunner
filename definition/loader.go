package definition

import (
	"os"
	"sort"

	"github.com/friendsofgo/errors"
	"github.com/mattn/go-zglob"
	"gopkg.in/yaml.v2"
)

func LoadRecursively(pattern string) (*PipelinesDef, error) {
	matches, err := zglob.GlobFollowSymlinks(pattern)
	if err != nil {
		return nil, errors.Wrap(err, "finding files with glob")
	}

	// Make globbing deterministic
	sort.Strings(matches)

	pipelinesDef := &PipelinesDef{
		Pipelines: make(map[string]PipelineDef),
	}

	for _, path := range matches {
		err = pipelinesDef.Load(path)
		if err != nil {
			return nil, errors.Wrapf(err, "loading %s", path)
		}
	}

	return pipelinesDef, nil
}

func (d *PipelinesDef) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.Wrap(err, "opening file")
	}
	defer f.Close()

	var localDef PipelinesDef

	err = yaml.NewDecoder(f).Decode(&localDef)
	if err != nil {
		return errors.Wrap(err, "decoding YAML")
	}
	localDef.setDefaults()

	for pipelineName, pipelineDef := range localDef.Pipelines {
		if p, exists := d.Pipelines[pipelineName]; exists {
			return errors.Errorf("pipeline %q was already declared in %s", pipelineName, p.SourcePath)
		}

		for taskName, taskDef := range pipelineDef.Tasks {
			for _, dependentTask := range taskDef.DependsOn {
				_, exists := pipelineDef.Tasks[dependentTask]
				if !exists {
					return errors.Errorf("missing task %q in pipeline %q referenced in depends_on of task %q", dependentTask, pipelineName, taskName)
				}
			}
		}

		pipelineDef.SourcePath = path
		d.Pipelines[pipelineName] = pipelineDef
	}

	return nil
}
