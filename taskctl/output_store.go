package taskctl

import (
	"fmt"
	"io"
	"os"
	"path"

	"github.com/friendsofgo/errors"
)

type OutputStore interface {
	Writer(jobID string, taskName string, outputName string) (io.WriteCloser, error)
	Reader(jobID string, taskName string, outputName string) (io.ReadCloser, error)
	Remove(jobID string) error
}

type FileOutputStore struct {
	path string
}

func NewOutputStore(path string) (*FileOutputStore, error) {
	err := os.MkdirAll(path, 0777)
	if err != nil {
		return nil, errors.Wrap(err, "creating base directory")
	}

	return &FileOutputStore{
		path: path,
	}, nil
}

func (s *FileOutputStore) Writer(jobID string, taskName string, outputName string) (io.WriteCloser, error) {
	err := os.MkdirAll(path.Join(s.path, jobID), 0777)
	if err != nil {
		return nil, errors.Wrap(err, "creating job logs directory")
	}

	filename := s.buildPath(jobID, taskName, outputName)
	f, err := os.Create(filename)
	if err != nil {
		return nil, errors.Wrap(err, "creating task output log file")
	}
	return f, nil
}

func (s *FileOutputStore) Reader(jobID string, taskName string, outputName string) (io.ReadCloser, error) {
	filename := s.buildPath(jobID, taskName, outputName)
	f, err := os.Open(filename)
	if err != nil {
		return nil, errors.Wrap(err, "opening task output log file")
	}
	return f, nil
}

func (s *FileOutputStore) buildPath(jobID string, taskName string, outputName string) string {
	return path.Join(s.path, jobID, fmt.Sprintf("%s-%s.log", taskName, outputName))
}

func (s *FileOutputStore) Remove(jobID string) error {
	return os.RemoveAll(path.Join(s.path, jobID))
}
