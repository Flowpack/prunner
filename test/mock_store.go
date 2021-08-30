package test

import (
	"bytes"
	"sync"

	"github.com/friendsofgo/errors"
	jsoniter "github.com/json-iterator/go"

	"github.com/Flowpack/prunner/store"
)

var json = jsoniter.ConfigFastest

type mockStore struct {
	storedBytes []byte
	mx          sync.Mutex
}

var _ store.DataStore = &mockStore{}

func NewMockStore() *mockStore {
	return &mockStore{}
}

func (m *mockStore) Set(data []byte) {
	m.mx.Lock()
	m.storedBytes = data
	m.mx.Unlock()
}

func (m *mockStore) Load() (*store.PersistedData, error) {
	m.mx.Lock()
	defer m.mx.Unlock()

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
	// Normally we do not need to lock, since the Save method would be called sequentially in prunner by requestPersist,
	// but tests call SaveToStore directly which can lead to data races.
	m.mx.Lock()
	defer m.mx.Unlock()

	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(data)
	if err != nil {
		return errors.Wrap(err, "encoding JSON")
	}
	m.storedBytes = buf.Bytes()

	return nil
}
