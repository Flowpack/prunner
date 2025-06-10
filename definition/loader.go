package definition

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/mattn/go-zglob"
	"gopkg.in/yaml.v2"
)

func LoadRecursively(pattern string) (*PipelinesDef, error) {
	matches, err := zglob.GlobFollowSymlinks(pattern)
	if err != nil {
		return nil, fmt.Errorf("finding files with glob: %w", err)
	}

	// Make globbing deterministic
	sort.Strings(matches)

	pipelinesDef := &PipelinesDef{
		Pipelines: make(map[string]PipelineDef),
	}

	for _, path := range matches {
		err = pipelinesDef.Load(path)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", path, err)
		}
	}

	return pipelinesDef, nil
}

func (d *PipelinesDef) Load(path string) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer func(f *os.File) {
		err = errors.Join(err, f.Close())
	}(f)

	var localDef PipelinesDef

	err = yaml.NewDecoder(f).Decode(&localDef)
	if err != nil {
		return fmt.Errorf("decoding YAML: %w", err)
	}
	localDef.setDefaults()

	for pipelineName, pipelineDef := range localDef.Pipelines {
		if p, exists := d.Pipelines[pipelineName]; exists {
			return fmt.Errorf("pipeline %q was already declared in %s", pipelineName, p.SourcePath)
		}

		err = pipelineDef.validate()
		if err != nil {
			return fmt.Errorf("invalid pipeline definition %q: %w", pipelineName, err)
		}

		pipelineDef.SourcePath = path
		d.Pipelines[pipelineName] = pipelineDef
	}

	return nil
}
