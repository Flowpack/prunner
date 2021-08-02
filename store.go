package prunner

import (
	"os"
	"path"
	"time"

	"github.com/friendsofgo/errors"
	"github.com/gofrs/uuid"
)

type persistedJob struct {
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

	Tasks []persistedTask
}

type persistedTask struct {
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

type persistedData struct {
	Jobs []persistedJob
}

type dataStore interface {
	Load() (*persistedData, error)
	Save(data *persistedData) error
}

type jsonDataStore struct {
	path string
}

var _ dataStore = &jsonDataStore{}

func newJSONDataStore(path string) (*jsonDataStore, error) {
	// Make sure directory for store file exists
	err := os.MkdirAll(path, 0777)
	if err != nil {
		return nil, errors.Wrap(err, "creating directory")
	}

	return &jsonDataStore{
		path: path,
	}, nil
}

func (j *jsonDataStore) Load() (*persistedData, error) {
	f, err := os.Open(path.Join(j.path, "data.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &persistedData{}, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "opening file")
	}
	defer f.Close()

	var result persistedData

	err = json.NewDecoder(f).Decode(&result)
	if err != nil {
		return nil, errors.Wrap(err, "decoding JSON")
	}

	return &result, nil
}

func (j *jsonDataStore) Save(data *persistedData) error {
	// Use a temporary file for writing data to be crash resistant
	f, err := os.CreateTemp(j.path, "data.*.tmp")
	if err != nil {
		return errors.Wrap(err, "creating temporary file")
	}
	tmpFilename := f.Name()

	err = json.NewEncoder(f).Encode(data)
	// In any case close the file
	f.Close()
	if err != nil {
		return errors.Wrap(err, "encoding JSON")
	}

	// Rename the tmp file to the data file to have something more atomic than writing directly to the data file
	err = os.Rename(tmpFilename, path.Join(j.path, "data.json"))
	if err != nil {
		return errors.Wrap(err, "replacing data file by rename")
	}

	return nil
}
