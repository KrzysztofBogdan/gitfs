package ksef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var (
	_ adapter.Session    = (*session)(nil)
	_ adapter.Reporter   = (*session)(nil)
	_ adapter.Identified = (*session)(nil)
)

var errReadOnly = errors.New(readOnlyMsg)

type session struct {
	c      *Client
	t      target
	report func(adapter.Progress)
	now    func() time.Time
}

func newSession(t target) *session {
	s := &session{c: NewClient(t.base, t.nip, t.token), t: t, report: func(adapter.Progress) {}, now: time.Now}
	s.c.LoginHint = loginHint(t.host, t.nip)
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *session) Identity() string                     { return "NIP " + s.t.nip }
func (s *session) Close() error                         { return nil }

// List exports every selected role from its cursor, or from since for a role
// the cursor lacks (ksef spec §6). Roles outside the selection are listed as
// full and empty, so their files are removed. A failed role fails the listing.
func (s *session) List(ctx context.Context, cur string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	from := map[string]string{}
	if cur != "" && json.Unmarshal([]byte(cur), &from) != nil {
		from = map[string]string{}
	}
	now := s.now()
	since, err := sinceTime(s.t.since, now)
	if err != nil {
		return adapter.Listing{}, err
	}
	var l adapter.Listing
	next := map[string]string{}
	for i, role := range s.t.roles {
		s.report(adapter.Progress{Phase: "pages", Done: i, Total: len(s.t.roles), Unit: "roles", Item: role})
		start := since
		if v, ok := from[role]; ok {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				start = t
			}
		}
		invs, n, err := s.c.exportRole(ctx, subjectOf[role], start, now)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("%s: %w", role, err)
		}
		for _, inv := range invs {
			l.Resources = append(l.Resources, resourceOf(role, inv, nil))
		}
		next[role] = utc(n)
	}
	for _, role := range roles {
		if !slices.Contains(s.t.roles, role) {
			l.FullDirs = append(l.FullDirs, roleRoot+"/"+role, role)
		}
	}
	b, _ := json.Marshal(next)
	l.Cursor = string(b)
	return l, nil
}

// Fetch is never needed: listings carry full content (ksef spec §6.4).
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	return nil, fmt.Errorf("%s: %w (KSeF invoices are only listed)", id, adapter.ErrNotFound)
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := make([]adapter.Result, len(req.Actions))
	for i, a := range req.Actions {
		out[i] = adapter.Result{Action: a, Err: errReadOnly}
	}
	return out
}

func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	return s.Check(ctx, req)
}

// Download reads an original again (gfs get), checked against its hash.
func (s *session) Download(ctx context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	_, k, ok := strings.Cut(resID, "/")
	if attID != "fa" || !ok {
		return adapter.AttachmentInfo{}, adapter.ErrNotFound
	}
	resp, data, err := s.c.send(ctx, http.MethodGet, "/invoices/ksef/"+url.PathEscape(k), true, nil, true)
	if err != nil {
		return adapter.AttachmentInfo{}, err
	}
	if h := resp.Header.Get("X-Ms-Meta-Hash"); h != "" && h != b64sha(data) {
		return adapter.AttachmentInfo{}, fmt.Errorf("invoice %s: content does not match KSeF's hash", k)
	}
	if _, err := w.Write(data); err != nil {
		return adapter.AttachmentInfo{}, err
	}
	return adapter.AttachmentInfo{Version: "-", Size: int64(len(data))}, nil
}
