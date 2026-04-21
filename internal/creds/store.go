// Package creds persists adapter credentials to the OS keyring.
package creds

// Store is a keyring-or-nothing credential store. Env-var overrides are
// the adapter's responsibility — this interface is keyring-only (spec §5).
type Store interface {
	Get(key string) (blob []byte, ok bool, err error)
	Put(key string, blob []byte) error
	Delete(key string) error
}

// Service name used for every keyring entry.
const Service = "gitfs"
