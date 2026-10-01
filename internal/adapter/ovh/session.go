package ovh

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

type session struct {
	c      *Client
	t      target
	report func(adapter.Progress)
	mu     sync.Mutex // guards report calls from parallel work
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

const zoneDir = "domain/zone"

func zoneFile(z string) string { return zoneDir + "/" + z + ".xml" }

// zoneOf is the zone a path names, "" when it names none.
func zoneOf(p string) string {
	dir, file := path.Split(p)
	if strings.TrimSuffix(dir, "/") != zoneDir || !strings.HasSuffix(file, ".xml") {
		return ""
	}
	return strings.TrimSuffix(file, ".xml")
}

func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }

// List is always full: every zone in the selection, as a stub carrying its
// version, so pull fetches only zones that changed (DNS spec §6.1).
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	names, err := s.zoneNames(ctx)
	if err != nil {
		return adapter.Listing{}, err
	}
	l := adapter.Listing{Full: true, Resources: make([]adapter.Resource, len(names))}
	done := 0
	err = parallel(ctx, len(names), func(ctx context.Context, i int) error {
		v, err := s.zoneVersion(ctx, names[i])
		if err != nil {
			return fmt.Errorf("zone %s: %w", names[i], err)
		}
		l.Resources[i] = adapter.Resource{ID: names[i], Path: zoneFile(names[i]), Version: v}
		s.mu.Lock()
		done++
		s.report(adapter.Progress{Phase: "pages", Done: done, Total: len(names), Item: names[i]})
		s.mu.Unlock()
		return nil
	})
	if err != nil {
		return adapter.Listing{}, err
	}
	return l, nil
}

// Fetch reads one zone; its version matches List's.
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if !s.t.sel.wants(id) {
		return nil, fmt.Errorf("zone %s: %w (outside the selection)", id, adapter.ErrNotFound)
	}
	v, err := s.zoneVersion(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("zone %s: %w", id, err)
	}
	root, err := s.readZone(ctx, id)
	if err != nil {
		return nil, err
	}
	return &adapter.Resource{ID: id, Path: zoneFile(id), Version: v, Root: root}, nil
}

func (s *session) Download(ctx context.Context, resourceID, attachmentID string, w io.Writer) (adapter.AttachmentInfo, error) {
	return adapter.AttachmentInfo{}, fmt.Errorf("ovh: zones have no attachments")
}

func (s *session) Close() error { return nil }
