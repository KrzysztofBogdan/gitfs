package cftest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, s *Server, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.URL+path, nil)
	req.SetBasicAuth("me@x.com", "t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func keysOf(m map[string]any) string {
	var out []string
	for _, r := range m["results"].([]any) {
		out = append(out, r.(map[string]any)["key"].(string))
	}
	return strings.Join(out, ",")
}

func TestSpacesFilters(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddSpace("ENG", "100")
	s.PutSpace(Space{Key: "~jan", ID: "300", Type: "personal"})
	s.PutSpace(Space{Key: "OLD", ID: "400", Status: "archived"})
	cases := map[string]string{
		"/wiki/api/v2/spaces":                                     "ENG,OLD,~jan",
		"/wiki/api/v2/spaces?status=current":                      "ENG,~jan",
		"/wiki/api/v2/spaces?status=current&type=global":          "ENG",
		"/wiki/api/v2/spaces?type=personal":                       "~jan",
		"/wiki/api/v2/spaces?keys=OLD," + url.QueryEscape("~jan"): "OLD,~jan",
	}
	for path, want := range cases {
		if _, m := get(t, s, path); keysOf(m) != want {
			t.Errorf("%s: got %s, want %s", path, keysOf(m), want)
		}
	}
	s.PageLimit = 1
	_, m := get(t, s, "/wiki/api/v2/spaces")
	if next := m["_links"].(map[string]any)["next"].(string); !strings.HasPrefix(next, "/wiki/api/v2/spaces?") {
		t.Fatalf("v2 next link: %q", next)
	}
}

func TestContentSearch(t *testing.T) {
	s := New()
	defer s.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }
	s.AddSpace("ENG", "100")
	s.AddSpace("OPS", "200")
	s.AddPage(Page{ID: "1", Title: "E", SpaceID: "100"})
	s.AddPage(Page{ID: "2", Title: "O", SpaceID: "200"})
	s.AddComment(Comment{ID: "c-old", PageID: "1", CreatedAt: "2026-09-29T11:00:00.000Z"})
	s.AddComment(Comment{ID: "c-new", PageID: "1", CreatedAt: "2026-09-29T11:55:00.000Z"})
	s.AddComment(Comment{ID: "c-ops", PageID: "2", CreatedAt: "2026-09-29T11:58:00.000Z"})
	s.AddAttachment(Attachment{ID: "a1", PageID: "2", Title: "x.png", CreatedAt: "2026-09-29T11:59:00.000Z"})

	search := func(cql string) (int, map[string]any) {
		return get(t, s, "/wiki/rest/api/content/search?limit=250&expand=container&cql="+url.QueryEscape(cql))
	}
	containers := func(m map[string]any) string {
		var out []string
		for _, r := range m["results"].([]any) {
			out = append(out, r.(map[string]any)["container"].(map[string]any)["id"].(string))
		}
		return strings.Join(out, ",")
	}
	if _, m := search(`type = comment AND lastmodified >= now("-10m")`); containers(m) != "1,2" {
		t.Fatalf("comments in 10m: %v", m)
	}
	if _, m := search(`type = comment AND lastmodified >= now("-10m") AND space in ("OPS")`); containers(m) != "2" {
		t.Fatalf("scoped: %v", m)
	}
	if _, m := search(`type = attachment AND lastmodified >= now("-10m")`); containers(m) != "2" {
		t.Fatalf("attachments: %v", m)
	}
	if code, _ := search(`type = page`); code != 400 {
		t.Fatalf("unsupported CQL must be 400, got %d", code)
	}
	s.PageLimit = 1
	_, m := search(`type = comment AND lastmodified >= now("-10m")`)
	if next := m["_links"].(map[string]any)["next"].(string); !strings.HasPrefix(next, "/rest/api/content/search?") {
		t.Fatalf("v1 next link must be relative to /wiki: %q", next)
	}
}

func TestStampAndRequests(t *testing.T) {
	s := New()
	defer s.Close()
	s.Clock = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	if s.Stamp() != "2026-09-29T12:00:00.000Z" {
		t.Fatal(s.Stamp())
	}
	get(t, s, "/wiki/api/v2/spaces")
	if r := s.TakeRequests(); len(r) != 1 || r[0] != "GET /wiki/api/v2/spaces" {
		t.Fatalf("%v", r)
	}
	if r := s.TakeRequests(); len(r) != 0 {
		t.Fatalf("not cleared: %v", r)
	}
}
