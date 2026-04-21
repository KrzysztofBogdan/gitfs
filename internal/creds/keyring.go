package creds

import (
	"errors"

	"github.com/zalando/go-keyring"
)

type keyringStore struct{}

// NewKeyring returns a Store backed by the OS keyring.
func NewKeyring() Store { return keyringStore{} }

func (keyringStore) Get(key string) ([]byte, bool, error) {
	s, err := keyring.Get(Service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return []byte(s), true, nil
}

func (keyringStore) Put(key string, blob []byte) error {
	return keyring.Set(Service, key, string(blob))
}

func (keyringStore) Delete(key string) error {
	return keyring.Delete(Service, key)
}

// NewKeyringOrNoop probes the keyring; if it is unavailable, returns a
// Noop store that just warns on Put.
func NewKeyringOrNoop() Store {
	// Cheap probe: attempt a Get on a canary account. If the daemon is
	// missing the keyring library typically returns an error that is not
	// ErrNotFound — treat that as "unavailable".
	_, err := keyring.Get(Service, "__gitfs_probe__")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return NewKeyring()
	}
	return NewNoop()
}
