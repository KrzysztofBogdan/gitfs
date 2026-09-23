package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseTarget(t *testing.T) {
	creds := env(map[string]string{"GFS_CONFLUENCE_TOKEN": "tok", "GFS_CONFLUENCE_EMAIL": "me@x.com"})
	cases := []struct {
		raw     string
		cfg     map[string]string
		getenv  func(string) string
		base    string
		space   string
		email   string
		wantErr string
	}{
		{"confluence://acme.atlassian.net/ENG", nil, creds, "https://acme.atlassian.net", "ENG", "me@x.com", ""},
		{"confluence://acme.atlassian.net/ENG?base=http://127.0.0.1:9", nil, creds, "http://127.0.0.1:9", "ENG", "me@x.com", ""},
		{"confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"base": "http://b"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), "http://b", "ENG", "u@x.com", ""},
		{"confluence://acme.atlassian.net/", nil, creds, "", "", "", "want confluence://<host>/<SPACEKEY>"},
		{"confluence://acme.atlassian.net/ENG", nil, env(nil), "", "", "", "GFS_CONFLUENCE_TOKEN"},
		{"confluence://acme.atlassian.net/ENG", nil, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), "", "", "", "GFS_CONFLUENCE_EMAIL"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		got, err := parseTarget(u, c.cfg, c.getenv)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.raw, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.base != c.base || got.space != c.space || got.email != c.email {
			t.Errorf("%s: got %+v %v", c.raw, got, err)
		}
	}
}

func testClient(s *cftest.Server) *client {
	return newClient(target{base: s.URL, space: "ENG", email: "me@x.com", token: "t"})
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
