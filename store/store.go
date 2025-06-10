package store

import (
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/gofrs/uuid"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigFastest

type PersistedJob struct {
	ID       uuid.UUID
	Pipeline string

	Completed bool `json:",omitempty"`
	Canceled  bool `json:",omitempty"`
	// Created is the schedule / queue time of the job
	Created time.Time
	// Start is the actual start time of the job
	Start *time.Time `json:",omitempty"`
	// End is the actual end time of the job (can be nil if incomplete)
	End *time.Time `json:",omitempty"`

	Variables map[string]interface{} `json:",omitempty"`
	User      string                 `json:",omitempty"`

	Tasks []PersistedTask
}

type PersistedTask struct {
	Name         string
	Script       []string
	DependsOn    []string   `json:",omitempty"`
	AllowFailure bool       `json:",omitempty"`
	Status       string     `json:",omitempty"`
	Start        *time.Time `json:",omitempty"`
	End          *time.Time `json:",omitempty"`
	Skipped      bool       `json:",omitempty"`
	ExitCode     int16      `json:",omitempty"`
	Errored      bool       `json:",omitempty"`
	Error        *string    `json:",omitempty"`
}

type PersistedData struct {
	Jobs []PersistedJob
}

type DataStore interface {
	Load() (*PersistedData, error)
	Save(data *PersistedData) error
}

type JsonDataStore struct {
	path string
}

var _ DataStore = &JsonDataStore{}

func NewJSONDataStore(path string) (*JsonDataStore, error) {
	// Make sure directory for store file exists
	err := os.MkdirAll(path, 0777)
	if err != nil {
		return nil, fmt.Errorf("creating directory: %w", err)
	}

	return &JsonDataStore{
		path: path,
	}, nil
}

func (j *JsonDataStore) Load() (result *PersistedData, err error) {
	f, err := os.Open(path.Join(j.path, "data.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &PersistedData{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer func(f *os.File) {
		err = errors.Join(err, f.Close())
	}(f)

	result = new(PersistedData)
	err = json.NewDecoder(f).Decode(result)
	if err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}

	return result, nil
}

func (j *JsonDataStore) Save(data *PersistedData) (err error) {
	// Use a temporary file for writing data to be crash resistant
	f, err := os.CreateTemp(j.path, "data.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpFilename := f.Name()

	err = json.NewEncoder(f).Encode(data)
	// In any case close the file
	defer func(f *os.File) {
		err = errors.Join(err, f.Close())
	}(f)
	if err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	// Rename the tmp file to the data file to have something more atomic than writing directly to the data file
	err = os.Rename(tmpFilename, path.Join(j.path, "data.json"))
	if err != nil {
		return fmt.Errorf("replacing data file by rename: %w", err)
	}

	return nil
}
