package bitbucket

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
)

var bg = context.Background()

// testSession talks to srv as me@x.com for remote (bitbucket://bitbucket.org/acme…).
func testSession(t *testing.T, srv *bbtest.Server, remote string) *session {
	t.Helper()
	u, err := url.Parse(remote)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("base", srv.URL)
	u.RawQuery = q.Encode()
	env := map[string]string{"GFS_BITBUCKET_EMAIL": "me@x.com", "GFS_BITBUCKET_TOKEN": "tok"}
	tg, err := parseTarget(u, nil, func(k string) string { return env[k] }, lookup{}, noEntries{})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(tg)
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	s.sleep = s.c.Sleep
	s.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	s.UseCache(t.TempDir())
	return s
}

// workspace: api and web, each with two PRs (one merged) and two runs (one running).
func workspace(t *testing.T) *bbtest.Server {
	t.Helper()
	srv := bbtest.New()
	t.Cleanup(srv.Close)
	srv.AddMember("712020:bo", "Bo Smith", "bo")
	for _, repo := range []string{"api", "web"} {
		srv.AddRepo(repo)
		srv.AddPR(repo, bbtest.PR{Title: "Old work", Source: "old", State: "MERGED"})
		srv.AddPR(repo, bbtest.PR{Title: "Add rate limiting", Source: "feat", Reviewers: []string{"712020:bo"}})
		srv.AddComment(repo, 2, bbtest.Comment{Raw: "looks good"})
		srv.AddRun(repo, bbtest.Run{Result: "SUCCESSFUL", Steps: []*bbtest.Step{{Name: "Build", State: "COMPLETED", Result: "SUCCESSFUL", Log: []byte("ok\n")}}})
		srv.AddRun(repo, bbtest.Run{State: "PENDING", Steps: []*bbtest.Step{{Name: "Build"}}})
	}
	return srv
}

func paths(l adapter.Listing) []string {
	var out []string
	for _, r := range l.Resources {
		out = append(out, r.Path)
	}
	slices.Sort(out)
	return out
}

func TestCloneListing(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api/pipelines/1.xml", "api/pipelines/2.xml", "api/pr/1 Old work.xml", "api/pr/2 Add rate limiting.xml",
		"web/pipelines/1.xml", "web/pipelines/2.xml", "web/pr/1 Old work.xml", "web/pr/2 Add rate limiting.xml"}
	if got := paths(l); !slices.Equal(got, want) || !slices.Equal(l.FullDirs, []string{"api", "web"}) || l.Full {
		t.Fatalf("%v %v", got, l.FullDirs)
	}
	versions := map[string]string{}
	for _, r := range l.Resources {
		versions[r.Path] = r.Version
		if r.Root != nil {
			t.Fatal("listings are stubs")
		}
	}
	if versions["api/pr/2 Add rate limiting.xml"] != "" || versions["api/pipelines/2.xml"] != "" ||
		versions["api/pr/1 Old work.xml"] == "" || !strings.HasPrefix(versions["api/pipelines/1.xml"], "completed:successful:") {
		t.Fatalf("open PRs and unfinished runs have no version: %v", versions)
	}
	// Fetch right after List reuses the listed JSON and matches its version
	srv.ResetCalls()
	for _, r := range l.Resources {
		res, err := s.Fetch(bg, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "" && res.Version != r.Version || res.Path != r.Path {
			t.Fatalf("%s: %s vs %s", r.ID, res.Version, r.Version)
		}
	}
	for _, c := range srv.Calls() {
		if strings.HasSuffix(strings.SplitN(c, "?", 2)[0], "/pullrequests/1") {
			t.Fatalf("fetched again: %s", c)
		}
	}
}

func TestSinceAndLimit(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme/api?limit=1")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(l); !slices.Equal(got, []string{"api/pipelines/2.xml", "api/pr/2 Add rate limiting.xml"}) {
		t.Fatal(got)
	}
	s = testSession(t, srv, "bitbucket://bitbucket.org/acme/api?since=2026-10-02")
	srv.ResetCalls()
	if _, err := s.List(bg, ""); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(srv.Calls(), func(c string) bool { return strings.Contains(c, "updated_on+%3E%3D+%222026-10-02T00%3A00%3A00Z%22") }) {
		t.Fatalf("%v", srv.Calls())
	}
}

func TestIncrementalListing(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.Advance("api", 2)
	srv.Advance("api", 2) // step completes, run completes
	srv.AddRun("web", bbtest.Run{Result: "FAILED"})
	srv.ResetCalls()
	s = testSession(t, srv, "bitbucket://bitbucket.org/acme")
	l2, err := s.List(bg, l.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(l2)
	want := []string{"api/pipelines/2.xml", "api/pr/2 Add rate limiting.xml", "web/pipelines/2.xml", "web/pipelines/3.xml", "web/pr/2 Add rate limiting.xml"}
	if !slices.Equal(got, want) || len(l2.FullDirs) != 0 {
		t.Fatalf("%v %v", got, l2.FullDirs)
	}
	for _, r := range l2.Resources {
		if r.Path == "api/pipelines/2.xml" && !strings.HasPrefix(r.Version, "completed:") {
			t.Fatal("a run finished since last pull gets its final version", r.Version)
		}
	}
	for _, c := range srv.Calls() {
		if strings.Contains(c, "/steps/") {
			t.Fatalf("listing reads no steps: %s", c)
		}
	}
	// a repository leaving the selection: listed with nothing in it
	s = testSession(t, srv, "bitbucket://bitbucket.org/acme/api")
	l3, err := s.List(bg, l2.Cursor)
	if err != nil || !slices.Contains(l3.FullDirs, "web") {
		t.Fatal(l3.FullDirs, err)
	}
}

func TestFetchRunLogs(t *testing.T) {
	srv := bbtest.New()
	defer srv.Close()
	srv.AddRepo("api")
	big := strings.Repeat("x", 1500)
	uuid := srv.AddRun("api", bbtest.Run{Result: "FAILED", Steps: []*bbtest.Step{
		{Name: "Build", State: "COMPLETED", Result: "SUCCESSFUL", Log: []byte(big)},
		{Name: "Lint", State: "COMPLETED", Result: "FAILED", Log: []byte(strings.Repeat("y", 1024))},
		{Name: "Deploy"}}})
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	res, err := s.Fetch(bg, runID("api", uuid))
	if err != nil {
		t.Fatal(err)
	}
	steps := res.Root.ChildrenNamed("step")
	if attr(steps[0], "log-size") != "1500" || steps[0].Child("log") != nil || attr(res.Root.Child("steplog"), "size") != "1500" {
		t.Fatal(canonical(t, res.Root))
	}
	if attr(steps[1], "log-size") != "1024" || len(text(steps[1].Child("log"))) != 1024 {
		t.Fatal("a log of exactly the limit is inline")
	}
	if attr(steps[2], "log-size") != "" {
		t.Fatal("a pending step has no log")
	}
	var b bytes.Buffer
	info, err := s.Download(bg, res.ID, attr(res.Root.Child("steplog"), "step"), &b)
	if err != nil || b.String() != big || info.Size != 1500 || info.Version != "-" {
		t.Fatal(info, err, b.Len())
	}
}

func TestRateLimitWait(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	var waits []string
	s.SetProgress(func(p adapter.Progress) {
		if p.Phase == "wait" {
			waits = append(waits, p.Item)
		}
	})
	srv.RateLimit(1)
	if _, err := s.List(bg, ""); err != nil || len(waits) != 1 {
		t.Fatal(waits, err)
	}
}

func TestFetchOutside(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme/api")
	for _, id := range []string{"web/pr/1", "api/pr/99", "nonsense"} {
		if _, err := s.Fetch(bg, id); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatal(id, err)
		}
	}
}

func TestListingProgress(t *testing.T) {
	srv := workspace(t)
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme/api")
	var items []string
	s.SetProgress(func(p adapter.Progress) {
		if p.Phase == "pages" && p.Unit == "repositories" {
			items = append(items, fmt.Sprintf("%d/%d %s", p.Done, p.Total, p.Item))
		}
	})
	if _, err := s.List(bg, ""); err != nil {
		t.Fatal(err)
	}
	if len(items) < 4 || items[0] != "0/1 api (0 PRs, 0 runs)" || items[len(items)-1] != "1/1 api (2/2 PRs, 2/2 runs)" {
		t.Fatal(items)
	}
	if !slices.Contains(items, "0/1 api (1/2 PRs, 0 runs)") {
		t.Fatal("the PR total shows as soon as the first page is read:", items)
	}
	if thousands(1231) != "1,231" || thousands(1234567) != "1,234,567" || thousands(12) != "12" {
		t.Fatal(thousands(1231))
	}
}
