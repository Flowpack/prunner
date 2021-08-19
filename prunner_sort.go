package prunner

import "sort"

// pipelineJobBy is the type of a "less" function that defines the ordering of its PipelineJob arguments.
type pipelineJobBy func(p1, p2 *PipelineJob) bool

// pipelineJobsSorter joins a By function and a slice of Planets to be sorted.
type pipelineJobsSorter struct {
	pipelineJobs []*PipelineJob
	by           pipelineJobBy
}

// Sort is a method on the function type, pipelineJobBy, that sorts the argument slice according to the function.
func (by pipelineJobBy) Sort(jobs []*PipelineJob) {
	ps := &pipelineJobsSorter{
		pipelineJobs: jobs,
		by:           by, // The Sort method's receiver is the function (closure) that defines the sort order.
	}
	sort.Sort(ps)
}

// Len is part of sort.Interface.
func (s *pipelineJobsSorter) Len() int {
	return len(s.pipelineJobs)
}

// Swap is part of sort.Interface.
func (s *pipelineJobsSorter) Swap(i, j int) {
	s.pipelineJobs[i], s.pipelineJobs[j] = s.pipelineJobs[j], s.pipelineJobs[i]
}

// Less is part of sort.Interface. It is implemented by calling the "by" closure in the sorter.
func (s *pipelineJobsSorter) Less(i, j int) bool {
	return s.by(s.pipelineJobs[i], s.pipelineJobs[j])
}

func byCreationTimeDesc(p1, p2 *PipelineJob) bool {
	return p2.Created.Before(p1.Created)
}
