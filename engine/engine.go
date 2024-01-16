package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type ExecutionNode struct {
	// DependsOn is a list of nodes that need to be finished before this node can be executed.
	DependsOn []*ExecutionNode
	// Run is the function that is executed when all dependencies are finished.
	Run func(ctx context.Context) error

	// Done is closed when the node has finished (either successfully or with an error)
	done chan struct{}
	// Err is set if Run returned an error
	err error
	// Canceled is set if the node execution was canceled from a failed dependency or a canceled context of Run.
	canceled bool

	// Track visit state for cycle detection
	visited int8
}

// Error returns the error of the node, blocking until the node is done.
func (n *ExecutionNode) Error() error {
	<-n.done
	return n.err
}

// Canceled returns true if the node was canceled, blocking until the node is done.
func (n *ExecutionNode) Canceled() bool {
	<-n.done
	return n.canceled
}

type Graph struct {
	nodes []*ExecutionNode

	started bool
}

var ErrGraphAlreadyStarted = errors.New("graph already started")
var ErrGraphIsCyclic = errors.New("graph is cyclic")

// Run executes the graph, blocking until all nodes have finished.
// If any node returns an error, some error of a node is returned.
func (g *Graph) Run(ctx context.Context) error {
	// There is no good way to re-execute the nodes with closing the done channels, so we just prevent it here.
	if g.started {
		return ErrGraphAlreadyStarted
	}
	g.started = true

	var firstErr error
	var setErr sync.Once

	nestedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, node := range g.nodes {
		go func(node *ExecutionNode) {
			defer close(node.done)

			// Wait for each dependent node to be done
			for _, dependentNode := range node.DependsOn {
				<-dependentNode.done
				if dependentNode.err != nil {
					node.err = NodeError{Err: dependentNode.err, Node: dependentNode}
					node.canceled = true
					return
				}
			}

			err := node.Run(nestedCtx)
			if err != nil {
				// Make sure we set the first occurring error once
				setErr.Do(func() {
					firstErr = NodeError{Err: err, Node: node}
				})
				node.err = err

				// Check if the nested context was canceled, so we mark the node as canceled
				select {
				case <-nestedCtx.Done():
					node.canceled = true
				default:
					// Cancel the context to signal to other nodes that they should stop
					cancel()
				}
			}
		}(node)
	}

	// Wait until all nodes have finished (regardless of cancellation, since each node already handles that)
	for _, node := range g.nodes {
		select {
		case <-node.done:
		}
	}

	return firstErr
}

func BuildGraph(nodes []*ExecutionNode) (*Graph, error) {
	// Detect cycles in the graph
	if isCyclic(nodes) {
		return nil, ErrGraphIsCyclic
	}

	// Create a done channel for each node
	for _, node := range nodes {
		node.done = make(chan struct{})
	}

	return &Graph{
		nodes: nodes,
	}, nil
}

func isCyclic(nodes []*ExecutionNode) bool {
	for _, node := range nodes {
		if node.visited == visitNotVisited {
			if isCyclicUtil(node) {
				return true
			}
		}
	}

	return false
}

func isCyclicUtil(node *ExecutionNode) bool {
	node.visited = visitVisiting

	for _, child := range node.DependsOn {
		if child.visited == visitNotVisited {
			if isCyclicUtil(child) {
				return true
			}
		} else if child.visited == visitVisiting {
			return true
		}
	}

	node.visited = visitProcessed
	return false
}

const (
	visitNotVisited = int8(0)
	visitVisiting   = int8(1)
	visitProcessed  = int8(2)
)

// NodeError is returned from Graph.Run when a node failed with an error or set as a node error when a dependent node failed and the node run was not started.
type NodeError struct {
	Err  error
	Node *ExecutionNode
}

func (e NodeError) Error() string {
	return fmt.Sprintf("node failed: %v", e.Err)
}

func (e NodeError) Unwrap() error {
	return e.Err
}
