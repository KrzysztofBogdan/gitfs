package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type fakeLookup struct {
	tokens map[string]string
	hosts  map[string]string
	sole   string
}

func (f fakeLookup) Token(e string) (string, error) {
	if t, ok := f.tokens[e]; ok {
		return t, nil
	}
	return "", creds.ErrNotFound
}
func (f fakeLookup) HostEmail(h string) (string, error) { return f.hosts[h], nil }
func (f fakeLookup) SoleIdentity() string               { return f.sole }

func TestParseTarget(t *testing.T) {
	both := env(map[string]string{"GFS_CONFLUENCE_TOKEN": "tok", "GFS_CONFLUENCE_EMAIL": "me@x.com"})
	none := env(nil)
	const acme = "acme.atlassian.net"
	cases := []struct {
		name    string
		raw     string
		cfg     map[string]string
		getenv  func(string) string
		lk      fakeLookup
		base    string
		email   string
		token   string
		wantErr string
	}{
		{"env", "confluence://acme.atlassian.net/ENG", nil, both, fakeLookup{}, "https://acme.atlassian.net", "me@x.com", "tok", ""},
		{"base query", "confluence://acme.atlassian.net/ENG?base=http://127.0.0.1:9", nil, both, fakeLookup{}, "http://127.0.0.1:9", "me@x.com", "tok", ""},
		{"env email beats URL user", "confluence://u%40x.com@acme.atlassian.net/ENG", nil, both, fakeLookup{}, "https://acme.atlassian.net", "me@x.com", "tok", ""},
		{"URL user, cfg base, env token", "confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"base": "http://b"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), fakeLookup{}, "http://b", "u@x.com", "t", ""},
		{"URL user beats remote email", "confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none,
			fakeLookup{tokens: map[string]string{"u@x.com": "ut"}}, "https://acme.atlassian.net", "u@x.com", "ut", ""},
		{"remote email beats host default", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none,
			fakeLookup{tokens: map[string]string{"c@x.com": "ct"}, hosts: map[string]string{acme: "h@x.com"}}, "https://acme.atlassian.net", "c@x.com", "ct", ""},
		{"host default beats sole identity", "confluence://acme.atlassian.net/ENG", nil, none,
			fakeLookup{tokens: map[string]string{"h@x.com": "ht"}, hosts: map[string]string{acme: "h@x.com"}, sole: "s@x.com"}, "https://acme.atlassian.net", "h@x.com", "ht", ""},
		{"sole identity", "confluence://acme.atlassian.net/ENG", nil, none,
			fakeLookup{tokens: map[string]string{"s@x.com": "st"}, sole: "s@x.com"}, "https://acme.atlassian.net", "s@x.com", "st", ""},
		{"env token beats store", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "envtok"}),
			fakeLookup{tokens: map[string]string{"c@x.com": "ct"}}, "https://acme.atlassian.net", "c@x.com", "envtok", ""},
		{"bad path", "confluence://acme.atlassian.net/a/b", nil, both, fakeLookup{}, "", "", "", "want confluence://<site>"},
		{"no identity", "confluence://acme.atlassian.net/ENG", nil, none, fakeLookup{}, "", "", "",
			"no identity for acme.atlassian.net: run gfs auth set <email> --host acme.atlassian.net, or put the email in the URL (confluence://me%40x.com@acme.atlassian.net)"},
		{"no token", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none, fakeLookup{}, "", "", "",
			"no token for c@x.com: run gfs auth set c@x.com"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		got, err := parseTarget(u, c.cfg, c.getenv, c.lk)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.base != c.base || !reflect.DeepEqual(got.sel, selection{keys: []string{"ENG"}}) || got.email != c.email || got.token != c.token {
			t.Errorf("%s: got %+v %v", c.name, got, err)
		}
	}
}

func TestVerifyToken(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.Accounts = map[string]string{"me@x.com": "good"}
	name, err := atlassian.VerifyToken(context.Background(), s.URL, "me@x.com", "good")
	if err != nil || name != "Me" {
		t.Fatalf("got %q %v", name, err)
	}
	_, err = atlassian.VerifyToken(context.Background(), s.URL, "me@x.com", "bad")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("bad token: %v", err)
	}
}

func testClient(s *cftest.Server) *client {
	return newClient(target{base: s.URL, email: "me@x.com", token: "t"})
}

func TestPaginate(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.PageLimit = 2
	s.AddSpace("ENG", "100")
	for _, title := range []string{"A", "B", "C", "D", "E"} {
		s.AddPage(cftest.Page{Title: title, SpaceID: "100"})
	}
	var titles []string
	err := testClient(s).paginate(context.Background(), "/wiki/api/v2/spaces/100/pages?limit=250", func(raw json.RawMessage) error {
		var p struct{ Title string }
		json.Unmarshal(raw, &p)
		titles = append(titles, p.Title)
		return nil
	})
	if err != nil || strings.Join(titles, "") != "ABCDE" {
		t.Fatalf("%v %v", titles, err)
	}
}

func TestErrorMapping(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.AddSpace("ENG", "100")
	p := s.AddPage(cftest.Page{Title: "A", SpaceID: "100", Storage: "<p>x</p>"})
	c := testClient(s)
	ctx := context.Background()

	err := c.do(ctx, "GET", "/wiki/api/v2/pages/999?body-format=storage", nil, nil)
	if !errors.Is(err, adapter.ErrNotFound) || codeOf(err) != "404" {
		t.Fatalf("404: %v", err)
	}
	stale := map[string]any{"id": p.ID, "status": "current", "title": "A",
		"body": map[string]any{"representation": "storage", "value": "<p>y</p>"}, "version": map[string]any{"number": 1}}
	err = c.do(ctx, "PUT", "/wiki/api/v2/pages/"+p.ID, stale, nil)
	if !errors.Is(err, adapter.ErrLock) || codeOf(err) != "409" {
		t.Fatalf("409: %v", err)
	}
	anon := newClient(target{base: s.URL})
	if err := anon.do(ctx, "GET", "/wiki/api/v2/spaces?keys=ENG", nil, nil); codeOf(err) != "401" {
		t.Fatalf("401: %v", err)
	}
}

func TestPaginateV1Links(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.Clock = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	s.PageLimit = 1
	s.AddSpace("ENG", "100")
	s.AddPage(cftest.Page{ID: "1", Title: "A", SpaceID: "100"})
	s.AddComment(cftest.Comment{ID: "c1", PageID: "1", CreatedAt: "2026-09-29T11:59:00.000Z"})
	s.AddComment(cftest.Comment{ID: "c2", PageID: "1", CreatedAt: "2026-09-29T11:59:00.000Z"})
	c := newClient(target{base: s.URL, email: "me@x.com", token: "t"})
	n := 0
	cql := url.QueryEscape(`type = comment AND lastmodified >= now("-5m")`)
	err := c.paginate(bg, "/wiki/rest/api/content/search?limit=1&expand=container&cql="+cql, func(json.RawMessage) error { n++; return nil })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
