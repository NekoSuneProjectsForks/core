package users

import (
	gojson "encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/datarhei/core/v16/encoding/json"
	"github.com/datarhei/core/v16/io/fs"
)

// storeData is the on-disk shape, versioned the same way restream/store
// versions its db.json so a future schema change has somewhere to hook a
// migration.
type storeData struct {
	Version uint64 `json:"version"`
	Users   []User `json:"users"`
}

const storeVersion uint64 = 1

// store persists users to a single JSON file, guarded by a mutex - the
// same pattern restream/store/json.go uses for its own db.json.
type store struct {
	fs       fs.Filesystem
	filepath string

	lock sync.RWMutex
}

func newStore(filesystem fs.Filesystem, filepath string) (*store, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("no valid filesystem provided")
	}

	if len(filepath) == 0 {
		filepath = "/users.json"
	}

	return &store{
		fs:       filesystem,
		filepath: filepath,
	}, nil
}

func (s *store) Load() ([]User, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	data := storeData{Version: storeVersion}

	_, err := s.fs.Stat(s.filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	jsondata, err := s.fs.ReadFile(s.filepath)
	if err != nil {
		return nil, err
	}

	if err := gojson.Unmarshal(jsondata, &data); err != nil {
		return nil, json.FormatError(jsondata, err)
	}

	if data.Version != storeVersion {
		return nil, fmt.Errorf("unsupported version of the users DB file (want: %d, have: %d)", storeVersion, data.Version)
	}

	return data.Users, nil
}

func (s *store) Store(list []User) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	data := storeData{
		Version: storeVersion,
		Users:   list,
	}

	jsondata, err := gojson.MarshalIndent(&data, "", "    ")
	if err != nil {
		return err
	}

	if _, _, err := s.fs.WriteFileSafe(s.filepath, jsondata); err != nil {
		return fmt.Errorf("failed to store users: %w", err)
	}

	return nil
}
