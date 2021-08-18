package test

import (
	"bytes"
	"github.com/Flowpack/prunner/store"
	"github.com/friendsofgo/errors"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigFastest

type mockStore struct {
	storedBytes []byte
}

var _ store.DataStore = &mockStore{}

func NewMockStore() *mockStore {
	return &mockStore{}
}

func (m *mockStore) Load() (*store.PersistedData, error) {
	var result store.PersistedData
	if len(m.storedBytes) == 0 {
		return &store.PersistedData{}, nil
	}
	err := json.NewDecoder(bytes.NewReader(m.storedBytes)).Decode(&result)
	if err != nil {
		return nil, errors.Wrap(err, "decoding JSON")
	}

	return &result, nil
}

func (m *mockStore) Save(data *store.PersistedData) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(data)
	if err != nil {
		return errors.Wrap(err, "encoding JSON")
	}
	m.storedBytes = buf.Bytes()

	return nil
}
