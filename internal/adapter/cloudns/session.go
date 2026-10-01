package cloudns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type session struct {
	c      *Client
	t      target
	report func(adapter.Progress)
	geo    *geoTable // nil until needed
	ttls   []int
	types  []string
}

func newSession(t target) *session {
	s := &session{c: NewClient(t.base, t.auth), t: t, report: func(adapter.Progress) {}}
	s.c.LoginHint = "gfs auth login cloudns://" + t.host
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

const (
	zoneDir   = "zone"
	geodnsID  = ".geodns"
	geodnsRes = ".geodns.xml"
)

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

func hash(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// version is the zone's serial plus a hash of its mail forwards, which do
// not move the serial (DNS spec §6.2). Failover-only changes show up with
// the zone's next change or on pull --full.
func (s *session) version(ctx context.Context, z, serial string) (string, error) {
	mf, err := s.c.Raw(ctx, "mail-forwards", url.Values{"domain-name": {z}})
	if err != nil {
		return "", err
	}
	return serial + "-" + hash(mf), nil
}

// List is always full: every zone in the selection as a stub with its
// version, and .geodns.xml when some zone is a GeoDNS zone.
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	zs, err := s.zones(ctx)
	if err != nil {
		return adapter.Listing{}, err
	}
	l := adapter.Listing{Full: true}
	geo := false
	for i, z := range zs {
		v, err := s.version(ctx, z.Name, z.Serial)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("zone %s: %w", z.Name, err)
		}
		l.Resources = append(l.Resources, adapter.Resource{ID: z.Name, Path: zoneFile(z.Name), Version: v})
		geo = geo || z.Kind == "geodns"
		s.report(adapter.Progress{Phase: "pages", Done: i + 1, Total: len(zs), Item: z.Name})
	}
	if geo {
		g, err := s.geoFile(ctx)
		if err != nil {
			return adapter.Listing{}, err
		}
		l.Resources = append([]adapter.Resource{*g}, l.Resources...)
	}
	return l, nil
}

// Fetch reads one zone (or .geodns.xml); its version matches List's.
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if id == geodnsID {
		return s.geoFile(ctx)
	}
	if !s.t.sel.wants(id) {
		return nil, fmt.Errorf("zone %s: %w (outside the selection)", id, adapter.ErrNotFound)
	}
	root, err := s.readZone(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := s.version(ctx, id, attr(root.Child("soa"), "serial"))
	if err != nil {
		return nil, err
	}
	return &adapter.Resource{ID: id, Path: zoneFile(id), Version: v, Root: root}, nil
}

func (s *session) geoFile(ctx context.Context) (*adapter.Resource, error) {
	g, err := s.geoTable(ctx)
	if err != nil {
		return nil, err
	}
	root := el("geodns")
	for _, l := range g.list {
		root.Children = append(root.Children, el("location", "code", l.Code, "name", l.Name, "parent", g.codeOf[l.Parent]))
	}
	data := xmltree.Print(root, 0)
	return &adapter.Resource{ID: geodnsID, Path: geodnsRes, Version: hash([]byte(data)), Root: root}, nil
}

func (s *session) Download(ctx context.Context, resourceID, attachmentID string, w io.Writer) (adapter.AttachmentInfo, error) {
	return adapter.AttachmentInfo{}, errors.New("cloudns: zones have no attachments")
}

func (s *session) Close() error { return nil }
