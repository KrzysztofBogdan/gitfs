package ovh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// API shapes (OVH /1.0/domain.json).
type apiRecord struct {
	ID        int64  `json:"id"`
	FieldType string `json:"fieldType"`
	SubDomain string `json:"subDomain"`
	Target    string `json:"target"`
	TTL       int64  `json:"ttl"`
}

type apiRedirect struct {
	ID          int64   `json:"id"`
	SubDomain   string  `json:"subDomain"`
	Target      string  `json:"target"`
	Type        string  `json:"type"`
	Title       *string `json:"title"`
	Keywords    *string `json:"keywords"`
	Description *string `json:"description"`
}

type apiDynHost struct {
	ID        int64  `json:"id"`
	SubDomain string `json:"subDomain"`
	IP        string `json:"ip"`
	TTL       int64  `json:"ttl"`
}

type apiLogin struct {
	Login     string `json:"login"`
	SubDomain string `json:"subDomain"`
}

type apiSOA struct {
	Server      string `json:"server"`
	Email       string `json:"email"`
	Serial      int64  `json:"serial"`
	Refresh     int64  `json:"refresh"`
	Expire      int64  `json:"expire"`
	NxDomainTTL int64  `json:"nxDomainTtl"`
	TTL         int64  `json:"ttl"`
}

func zonePath(z string, rest ...string) string {
	p := "/domain/zone/" + url.PathEscape(z)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// zoneNames lists the account's zones inside the selection.
func (s *session) zoneNames(ctx context.Context) ([]string, error) {
	var all []string
	if err := s.c.Do(ctx, http.MethodGet, "/domain/zone", nil, &all); err != nil {
		return nil, err
	}
	var out []string
	for _, z := range all {
		if s.t.sel.wants(z) {
			out = append(out, z)
		}
	}
	return out, nil
}

// zoneVersion is a hash of the zone's BIND export and DNSSEC state (DNS
// spec §6.1: lastUpdate does not move with edits).
func (s *session) zoneVersion(ctx context.Context, z string) (string, error) {
	var export string
	if err := s.c.Do(ctx, http.MethodGet, zonePath(z, "export"), nil, &export); err != nil {
		return "", err
	}
	dnssec, err := s.dnssec(ctx, z)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(export + "\x00" + dnssec))
	return hex.EncodeToString(sum[:8]), nil
}

// dnssec is the zone's DNSSEC status, "" when the zone cannot use it.
func (s *session) dnssec(ctx context.Context, z string) (string, error) {
	var d struct{ Status string }
	err := s.c.Do(ctx, http.MethodGet, zonePath(z, "dnssec"), nil, &d)
	if errors.Is(err, adapter.ErrNotFound) {
		return "", nil
	}
	return d.Status, err
}

// getAll fetches path/{id} for every id listed at path, concurrently.
func getAll[T any](ctx context.Context, s *session, path string) ([]T, error) {
	var ids []int64
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &ids); err != nil {
		return nil, err
	}
	out := make([]T, len(ids))
	err := parallel(ctx, len(ids), func(ctx context.Context, i int) error {
		return s.c.Do(ctx, http.MethodGet, path+"/"+strconv.FormatInt(ids[i], 10), nil, &out[i])
	})
	return out, err
}

var ownedTXT = regexp.MustCompile(`^[0-9]+\|`)

// zoneData is everything gfs reads of one zone.
type zoneData struct {
	soa       apiSOA
	dnssec    string
	records   []apiRecord
	redirects []apiRedirect
	dynhosts  []apiDynHost
	logins    []apiLogin
}

func (s *session) fetchZone(ctx context.Context, z string) (*zoneData, error) {
	d := &zoneData{}
	jobs := []func(context.Context) error{
		func(ctx context.Context) error { return s.c.Do(ctx, http.MethodGet, zonePath(z, "soa"), nil, &d.soa) },
		func(ctx context.Context) (err error) { d.dnssec, err = s.dnssec(ctx, z); return },
		func(ctx context.Context) (err error) {
			d.records, err = getAll[apiRecord](ctx, s, zonePath(z, "record"))
			return
		},
		func(ctx context.Context) (err error) {
			d.redirects, err = getAll[apiRedirect](ctx, s, zonePath(z, "redirection"))
			return
		},
		func(ctx context.Context) (err error) {
			d.dynhosts, err = getAll[apiDynHost](ctx, s, zonePath(z, "dynHost/record"))
			return
		},
		func(ctx context.Context) error {
			var names []string
			if err := s.c.Do(ctx, http.MethodGet, zonePath(z, "dynHost/login"), nil, &names); err != nil {
				return err
			}
			d.logins = make([]apiLogin, len(names))
			return parallel(ctx, len(names), func(ctx context.Context, i int) error {
				return s.c.Do(ctx, http.MethodGet, zonePath(z, "dynHost/login", url.PathEscape(names[i])), nil, &d.logins[i])
			})
		},
	}
	if err := parallel(ctx, len(jobs), func(ctx context.Context, i int) error { return jobs[i](ctx) }); err != nil {
		return nil, fmt.Errorf("zone %s: %w", z, err)
	}
	return d, nil
}

// readZone is the zone file's content (DNS spec §5.1).
func (s *session) readZone(ctx context.Context, z string) (*xmltree.Node, error) {
	d, err := s.fetchZone(ctx, z)
	if err != nil {
		return nil, err
	}
	return d.toXML(z), nil
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], dnsx.XMLText(attrs[i+1]))
		}
	}
	return n
}

func withText(n *xmltree.Node, text string) *xmltree.Node {
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: dnsx.XMLText(text)}}
	}
	return n
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func ttlAttr(ttl int64) string {
	if ttl == 0 {
		return "" // the zone default
	}
	return itoa(ttl)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (d *zoneData) toXML(z string) *xmltree.Node {
	root := el("zone", "name", z)
	root.Children = append(root.Children, el("soa", "ttl", itoa(d.soa.TTL), "refresh", itoa(d.soa.Refresh), "expire", itoa(d.soa.Expire),
		"nx-domain-ttl", itoa(d.soa.NxDomainTTL), "email", d.soa.Email, "server", d.soa.Server, "serial", itoa(d.soa.Serial)))
	if d.dnssec != "" {
		root.Children = append(root.Children, withText(el("dnssec"), d.dnssec))
	}
	owned := map[int64]bool{}
	redirectAt := map[string]bool{}
	for _, r := range d.redirects {
		owned[r.ID] = true
		redirectAt[r.SubDomain] = true
	}
	for _, h := range d.dynhosts {
		owned[h.ID] = true
	}
	for _, r := range d.records {
		if owned[r.ID] || (r.FieldType == "TXT" && redirectAt[r.SubDomain] && ownedTXT.MatchString(r.Target)) {
			continue
		}
		root.Children = append(root.Children, withText(el("record", "id", itoa(r.ID), "name", dnsx.FileName(r.SubDomain),
			"type", r.FieldType, "ttl", ttlAttr(r.TTL)), r.Target))
	}
	for _, r := range d.redirects {
		root.Children = append(root.Children, withText(el("redirect", "id", itoa(r.ID), "name", dnsx.FileName(r.SubDomain), "type", r.Type,
			"title", deref(r.Title), "keywords", deref(r.Keywords), "description", deref(r.Description)), r.Target))
	}
	for _, h := range d.dynhosts {
		root.Children = append(root.Children, withText(el("dynhost", "id", itoa(h.ID), "name", dnsx.FileName(h.SubDomain), "ttl", ttlAttr(h.TTL)), h.IP))
	}
	for _, l := range d.logins {
		root.Children = append(root.Children, el("dynhost-login", "login", l.Login, "name", dnsx.FileName(l.SubDomain)))
	}
	return root
}
