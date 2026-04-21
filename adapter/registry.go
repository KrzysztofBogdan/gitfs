package adapter

import (
	"sort"
	"sync"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a Factory available under the given scheme. It is meant
// to be called from init() in each adapter package. Re-registering the
// same scheme overwrites the previous factory.
func Register(scheme string, f Factory) {
	if scheme == "" {
		panic("adapter.Register: empty scheme")
	}
	if f == nil {
		panic("adapter.Register: nil factory for " + scheme)
	}
	registryMu.Lock()
	registry[scheme] = f
	registryMu.Unlock()
}

// Lookup returns the Factory registered for scheme, if any.
func Lookup(scheme string) (Factory, bool) {
	registryMu.RLock()
	f, ok := registry[scheme]
	registryMu.RUnlock()
	return f, ok
}

// Installed returns the sorted list of registered schemes.
func Installed() []string {
	registryMu.RLock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	registryMu.RUnlock()
	sort.Strings(out)
	return out
}
