package prunner

import (
	"sync"
	"testing"
)

func TestServer_Pipelines(t *testing.T) {
	pRunner := newPipelineRunner(defs, taskRunner)
	newServer(pRunner, nil)
}
