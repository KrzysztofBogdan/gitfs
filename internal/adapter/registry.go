package adapter

import (
	"fmt"
	"net/url"
	"sync"
)

var (
	mu       sync.Mutex
	byScheme = map[string]Adapter{}
	all      []Adapter
)

func Register(a Adapter) {
	mu.Lock()
	defer mu.Unlock()
	all = append(all, a)
	for _, s := range a.Schemes() {
		byScheme[s] = a
	}
}

func ForURL(raw string) (Adapter, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	mu.Lock()
	a, ok := byScheme[u.Scheme]
	mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("no adapter for scheme %q", u.Scheme)
	}
	return a, u, nil
}

func All() []Adapter {
	mu.Lock()
	defer mu.Unlock()
	return append([]Adapter(nil), all...)
}
