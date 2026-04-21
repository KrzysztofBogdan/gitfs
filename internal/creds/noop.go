package creds

import (
	"fmt"
	"os"
	"sync"
)

type noopStore struct {
	warned sync.Once
}

// NewNoop returns a Store that never persists and logs once on first Put.
func NewNoop() Store { return &noopStore{} }

func (n *noopStore) Get(string) ([]byte, bool, error) { return nil, false, nil }

func (n *noopStore) Put(string, []byte) error {
	n.warned.Do(func() {
		fmt.Fprintln(os.Stderr,
			"gitfs: OS keyring unavailable; credentials will not be saved.")
	})
	return nil
}

func (n *noopStore) Delete(string) error { return nil }
