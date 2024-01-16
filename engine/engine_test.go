package engine_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/Flowpack/prunner/engine"
)

func TestGraph_Run_SerialDependencies(t *testing.T) {
	defer goleak.VerifyNone(t)

	var results results

	var nodes []*engine.ExecutionNode

	var prevExecutionNode *engine.ExecutionNode
	for i := 0; i < 10; i++ {
		resultID := i

		executionNode := &engine.ExecutionNode{
			Run: func(ctx context.Context) error {
				results.add(resultID)
				return nil
			},
		}
		if prevExecutionNode != nil {
			executionNode.DependsOn = []*engine.ExecutionNode{prevExecutionNode}
		}

		nodes = append(nodes, executionNode)

		prevExecutionNode = executionNode
	}

	g, err := engine.BuildGraph(nodes)
	require.NoError(t, err)

	ctx := context.Background()

	err = g.Run(ctx)

	require.NoError(t, err, "graph execution should not return an error")

	resultIDs := results.get()
	require.Len(t, resultIDs, 10, "all results should be returned")
	// Copy and sort results to make sure they are in the correct order
	sortedResultIDs := append([]int{}, resultIDs...)
	sort.Ints(sortedResultIDs)

	assert.Equal(t, sortedResultIDs, resultIDs, "results should be returned in the correct order")
}

func TestGraph_Run_FanIn(t *testing.T) {
	defer goleak.VerifyNone(t)

	var results results

	startNode := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			results.add(0)
			return nil
		},
	}

	var middleNodes []*engine.ExecutionNode

	for i := 0; i < 10; i++ {
		resultID := i + 1

		executionNode := &engine.ExecutionNode{
			Run: func(ctx context.Context) error {
				results.add(resultID)
				return nil
			},
			DependsOn: []*engine.ExecutionNode{startNode},
		}

		middleNodes = append(middleNodes, executionNode)
	}

	endNode := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			results.add(100)
			return nil
		},
		DependsOn: middleNodes,
	}

	g, err := engine.BuildGraph(append(append([]*engine.ExecutionNode{startNode}, middleNodes...), endNode))
	require.NoError(t, err)

	ctx := context.Background()

	err = g.Run(ctx)

	require.NoError(t, err, "graph execution should not return an error")

	resultIDs := results.get()
	require.Len(t, resultIDs, 12, "all results should be returned")

	assert.Equal(t, 0, resultIDs[0], "start node should be executed first")
	assert.Equal(t, 100, resultIDs[11], "end node should be executed last")
}

func TestGraph_Run_Cancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	var results results

	wait := make(chan struct{})

	node := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
				results.add(0)
				return nil
			}
		},
	}

	g, err := engine.BuildGraph([]*engine.ExecutionNode{node})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		cancel()
	}()

	err = g.Run(ctx)

	require.Error(t, err, "graph execution should return an error")
	assert.ErrorIs(t, err, context.Canceled)

	resultIDs := results.get()
	require.Empty(t, resultIDs, "no result should be returned")
}

func TestGraph_Run_FailCancels(t *testing.T) {
	defer goleak.VerifyNone(t)

	var results results

	var failErr = errors.New("some failure")

	node1 := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			return failErr
		},
	}
	node2 := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			results.add(2)
			return nil
		},
		DependsOn: []*engine.ExecutionNode{node1},
	}
	node3 := &engine.ExecutionNode{
		Run: func(ctx context.Context) error {
			select {
			case <-time.After(5 * time.Second):
				results.add(3)
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
	}

	g, err := engine.BuildGraph([]*engine.ExecutionNode{node2, node1, node3})
	require.NoError(t, err)

	ctx := context.Background()

	err = g.Run(ctx)

	require.Error(t, err, "graph execution should return an error")
	assert.ErrorIs(t, err, engine.NodeError{Err: failErr, Node: node1})

	resultIDs := results.get()
	require.Empty(t, resultIDs, "no result should be returned")

	assert.ErrorIs(t, node1.Error(), failErr)
	assert.Equal(t, false, node1.Canceled())

	assert.ErrorIs(t, node2.Error(), engine.NodeError{Err: failErr, Node: node1})
	assert.Equal(t, true, node2.Canceled())

	assert.ErrorIs(t, node3.Error(), context.Canceled)
	assert.Equal(t, true, node3.Canceled())
}

func TestGraph_BuildGraph_CyclicDependencies(t *testing.T) {
	defer goleak.VerifyNone(t)

	node1 := &engine.ExecutionNode{}
	node2 := &engine.ExecutionNode{}
	node1.DependsOn = []*engine.ExecutionNode{node2}
	node2.DependsOn = []*engine.ExecutionNode{node1}

	_, err := engine.BuildGraph([]*engine.ExecutionNode{node1, node2})
	require.ErrorIs(t, err, engine.ErrGraphIsCyclic)
}

type results struct {
	values []int
	mx     sync.Mutex
}

func (r *results) add(i int) {
	r.mx.Lock()
	defer r.mx.Unlock()

	r.values = append(r.values, i)
}

func (r *results) get() []int {
	r.mx.Lock()
	defer r.mx.Unlock()

	return r.values
}
