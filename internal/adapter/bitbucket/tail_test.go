package bitbucket

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
)

// tailSite: run 1 on api, pending: Build, then Lint and Test in parallel; Lint fails.
func tailSite(t *testing.T) (*bbtest.Server, *session, string, *[]time.Duration) {
	t.Helper()
	srv := bbtest.New()
	t.Cleanup(srv.Close)
	srv.AddRepo("api")
	uuid := srv.AddRun("api", bbtest.Run{State: "PENDING", Steps: []*bbtest.Step{
		{Name: "Build", Image: "golang:1.25"},
		{Name: "Lint", Image: "par:golangci", Result: "FAILED"},
		{Name: "Test", Image: "par:golang"}}})
	srv.Queue("api", 1, 0, "go build ./...\n", "done")
	srv.Queue("api", 1, 1, "lint: src/a.go:1 bad\n")
	srv.Queue("api", 1, 2, "ok  ./a 0.4s\n")
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	var sleeps []time.Duration
	s.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sleeps = append(sleeps, d)
		clock = clock.Add(d)
		srv.Advance("api", 1)
		return nil
	}
	return srv, s, runID("api", uuid), &sleeps
}

func TestTailRun(t *testing.T) {
	_, s, id, _ := tailSite(t)
	var out bytes.Buffer
	res, err := s.Tail(bg, id, adapter.TailOpts{Interval: time.Second}, &out)
	if err != nil || res.Result != "failed" || res.RunID != id {
		t.Fatal(res, err, out.String())
	}
	got := out.String()
	for _, w := range []string{
		"── #1 branches:main main a1b2c3d ──\n── Build (golang:1.25) ──\ngo build ./...\n── Lint (golangci) ──\n── Test (golang) ──\n",
		"[Build] done\n── Build: successful in 1m00s ──\n", // a partial line flushed once others run is prefixed too
		"[Lint] lint: src/a.go:1 bad\n", "[Test] ok  ./a 0.4s\n",
		"── Lint: failed in ", "── Test: successful in ",
		"── #1 failed in ",
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %q:\n%s", w, got)
		}
	}
	if strings.Count(got, "go build") != 1 {
		t.Fatalf("each byte once:\n%s", got)
	}
}

func TestTailFinishedRun(t *testing.T) {
	srv, s, _, sleeps := tailSite(t)
	uuid := srv.AddRun("api", bbtest.Run{Result: "SUCCESSFUL", Steps: []*bbtest.Step{{Name: "Build", State: "COMPLETED", Result: "SUCCESSFUL", Log: []byte("all\nof it")}}})
	var out bytes.Buffer
	res, err := s.Tail(bg, runID("api", uuid), adapter.TailOpts{Interval: time.Second}, &out)
	if err != nil || res.Result != "successful" || len(*sleeps) != 0 || !strings.Contains(out.String(), "── Build ──\nall\nof it\n── Build: successful in 0s ──\n── #2 successful in 5m00s ──\n") {
		t.Fatal(res, err, *sleeps, out.String())
	}
}

func TestTailPRWaitsForRun(t *testing.T) {
	srv := bbtest.New()
	defer srv.Close()
	srv.AddRepo("api")
	srv.AddPR("api", bbtest.PR{Title: "x", Source: "feat", SourceCommit: "abc123"})
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	clock := s.now()
	s.now = func() time.Time { return clock }
	ticks := 0
	s.sleep = func(ctx context.Context, d time.Duration) error {
		clock = clock.Add(d)
		if ticks++; ticks == 2 {
			srv.AddRun("api", bbtest.Run{Result: "SUCCESSFUL", Commit: "abc123", TargetType: "pipeline_pullrequest_target", RefName: "feat", Selector: "pull-requests:**", PR: 1})
		}
		return nil
	}
	var out bytes.Buffer
	res, err := s.Tail(bg, "api/pr/1", adapter.TailOpts{Interval: time.Second, Wait: time.Minute}, &out)
	if err != nil || res.Result != "successful" || !strings.HasPrefix(out.String(), "── #1 pull-requests:** feat abc123 ──") {
		t.Fatal(res, err, out.String())
	}
	srv.AddPR("api", bbtest.PR{Title: "y", Source: "feat2", SourceCommit: "def456"})
	_, err = s.Tail(bg, "api/pr/2", adapter.TailOpts{Interval: time.Second, Wait: 3 * time.Second}, &out)
	if err == nil || !strings.Contains(err.Error(), "no pipeline started for def456") {
		t.Fatal(err)
	}
}

func TestTailBackOff(t *testing.T) {
	srv, s, id, sleeps := tailSite(t)
	for i := 0; i < 40; i++ {
		srv.Queue("api", 1, 0, "") // Build prints nothing for a long while
	}
	var out bytes.Buffer
	if _, err := s.Tail(bg, id, adapter.TailOpts{Interval: 5 * time.Second}, &out); err != nil {
		t.Fatal(err)
	}
	seen := map[time.Duration]int{}
	prev := time.Duration(0)
	for _, d := range (*sleeps)[:30] {
		if d < prev {
			t.Fatalf("no new bytes: the interval only grows: %v", *sleeps)
		}
		prev = d
		seen[d]++
	}
	if seen[5*time.Second] == 0 || seen[10*time.Second] == 0 || seen[20*time.Second] == 0 || seen[30*time.Second] == 0 {
		t.Fatalf("a quiet minute doubles the interval up to 30s: %v", *sleeps)
	}
}

func TestTailNearLimit(t *testing.T) {
	srv, s, id, sleeps := tailSite(t)
	srv.NearLimit(true)
	var out bytes.Buffer
	if _, err := s.Tail(bg, id, adapter.TailOpts{Interval: time.Second}, &out); err != nil {
		t.Fatal(err)
	}
	for _, d := range *sleeps {
		if d != 30*time.Second {
			t.Fatalf("near the limit every wait is 30s: %v", *sleeps)
		}
	}
}

func TestTailCancel(t *testing.T) {
	_, s, id, _ := tailSite(t)
	ctx, cancel := context.WithCancel(bg)
	n := 0
	inner := s.sleep
	s.sleep = func(c context.Context, d time.Duration) error {
		if n++; n == 2 {
			cancel()
		}
		return inner(c, d)
	}
	var out bytes.Buffer
	res, err := s.Tail(ctx, id, adapter.TailOpts{Interval: time.Second}, &out)
	if err != nil || res.RunID != id || res.Result != "" {
		t.Fatal(res, err)
	}
}
