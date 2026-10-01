package ovh

import (
	"context"
	"sync"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

type session struct {
	c      *Client
	t      target
	report func(adapter.Progress)
}

func newSession(t target) *session {
	s := &session{c: NewClient(t.base, t.creds), t: t, report: func(adapter.Progress) {}}
	s.c.LoginHint = "gfs auth login ovh://" + t.endpoint
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

// parallel runs f(0..n-1) concurrently (the client bounds requests in
// flight) and returns the first error.
func parallel(ctx context.Context, n int, f func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}()
	}
	wg.Wait()
	return first
}
