package ksef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/ksefpdf"
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
	// pdfWorkers is the renderers while listing (ksef pdf spec §5.2).
	pdfWorkers int
}

func newSession(t target) *session {
	s := &session{c: NewClient(t.base, t.nip, t.token), t: t, report: func(adapter.Progress) {}, now: time.Now,
		pdfWorkers: min(runtime.GOMAXPROCS(0), 4)}
	s.c.LoginHint = loginHint(t.host, t.nip)
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *session) Identity() string                     { return "NIP " + s.t.nip }
func (s *session) Close() error                         { return nil }

// List exports every selected role from its cursor, or from since for a role
// the cursor lacks (ksef spec §6). Roles outside the selection are listed as
// full and empty by id, so their files are removed. A failed role fails the listing.
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
	var found []roleInvoice
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
			found = append(found, roleInvoice{role, inv})
		}
		next[role] = utc(n)
	}
	pdfs, err := s.renderAll(ctx, found)
	if err != nil {
		return adapter.Listing{}, err
	}
	for _, f := range found {
		l.Resources = append(l.Resources, resourceOf(f.role, f.inv, pdfs[f.inv.meta.KsefNumber]))
	}
	for _, role := range roles {
		if !slices.Contains(s.t.roles, role) {
			l.FullIDPrefixes = append(l.FullIDPrefixes, role+"/")
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

// Download reads an original again (gfs get), checked against its hash;
// for the PDF it renders that original (ksef pdf spec §5.3).
func (s *session) Download(ctx context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	_, k, ok := strings.Cut(resID, "/")
	if !ok || (attID != "fa" && (attID != "pdf" || !s.t.pdf)) {
		return adapter.AttachmentInfo{}, adapter.ErrNotFound
	}
	resp, data, err := s.c.send(ctx, http.MethodGet, "/invoices/ksef/"+url.PathEscape(k), true, nil, true)
	if err != nil {
		return adapter.AttachmentInfo{}, err
	}
	if h := resp.Header.Get("X-Ms-Meta-Hash"); h != "" && h != b64sha(data) {
		return adapter.AttachmentInfo{}, fmt.Errorf("invoice %s: content does not match KSeF's hash", k)
	}
	if attID == "pdf" {
		pool := ksefpdf.NewPool(1)
		defer pool.Close()
		if data, err = pool.Render(ctx, ksefpdf.Input{XML: data, KsefNumber: k, QRBase: qrBase[s.t.host]}); err != nil {
			return adapter.AttachmentInfo{}, fmt.Errorf("PDF of %s: %w", k, err)
		}
	}
	if _, err := w.Write(data); err != nil {
		return adapter.AttachmentInfo{}, err
	}
	return adapter.AttachmentInfo{Version: "-", Size: int64(len(data))}, nil
}
