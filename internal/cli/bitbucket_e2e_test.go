package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
)

// bitbucketSite: workspace acme with repos api and web; api has two open PRs,
// a finished run with a big log and a pending run.
func bitbucketSite(t *testing.T) *bbtest.Server {
	t.Helper()
	srv := bbtest.New()
	t.Cleanup(srv.Close)
	srv.AddMember("712020:bo", "Bo Smith", "bo")
	srv.AddMember("712020:ana", "Ana Nowak", "ana")
	srv.AddRepo("api", "feat3")
	srv.AddRepo("web")
	srv.AddPR("api", bbtest.PR{Title: "Add rate limiting", Source: "feat", Description: "first", CloseSource: true})
	srv.AddPR("api", bbtest.PR{Title: "Drop v1", Source: "feat2"})
	srv.AddComment("api", 1, bbtest.Comment{Account: "712020:bo", Raw: "Bo asks"})
	srv.AddRun("api", bbtest.Run{Result: "SUCCESSFUL", Steps: []*bbtest.Step{{Name: "Build", State: "COMPLETED", Result: "SUCCESSFUL",
		Log: []byte(strings.Repeat("compiling\n", 200))}}})
	srv.AddRun("api", bbtest.Run{State: "PENDING", Steps: []*bbtest.Step{{Name: "Test", Result: "FAILED"}}})
	srv.Queue("api", 2, 0, "running tests\n", "FAIL x\n")
	t.Setenv("GFS_BITBUCKET_EMAIL", "me@x.com")
	t.Setenv("GFS_BITBUCKET_TOKEN", "tok")
	return srv
}

func TestBitbucketEndToEnd(t *testing.T) {
	srv := bitbucketSite(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustContain(t, mustRun(t, 0, "clone", "bitbucket://bitbucket.org/acme?base="+url.QueryEscape(srv.URL)), "Cloned 4 resources")
	t.Chdir(filepath.Join(dir, "acme"))
	pr1 := "api/pr/1 Add rate limiting.xml"
	for _, f := range []string{pr1, "api/pr/2 Drop v1.xml", "api/pipelines/1.xml", "api/pipelines/2.xml"} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// edit fields and comments
	replaceIn(t, pr1, "<title>Add rate limiting</title>", "<title>Rate limiting</title>")
	replaceIn(t, pr1, "</pullrequest>", `<reviewer>Bo Smith</reviewer><comment>top-level</comment><comment path="src/a.go" line="3">inline</comment></pullrequest>`)
	b, _ := os.ReadFile(pr1)
	boID := regexp.MustCompile(`<comment id="(\d+)"`).FindSubmatch(b)[1]
	replaceIn(t, pr1, "</pullrequest>", `<comment parent="`+string(boID)+`">reply</comment></pullrequest>`)
	mustContain(t, mustRun(t, 0, "status"), "update title", "update reviewer", "comment on src/a.go:3", "reply to comment "+string(boID))
	mustContain(t, mustRun(t, 0, "commit"), "0 failed")
	pr1 = "api/pr/1 Rate limiting.xml"
	b, err := os.ReadFile(pr1)
	if err != nil {
		t.Fatal("renamed after the title:", err)
	}
	if regexp.MustCompile(`<comment>`).Match(b) || !strings.Contains(string(b), `parent="`+string(boID)+`"`) || !strings.Contains(string(b), `account="712020:bo"`) {
		t.Fatalf("comments and reviewer come back with ids:\n%s", b)
	}
	if p := srv.PR("api", 1); p.Title != "Rate limiting" || len(p.Comments) != 4 {
		t.Fatalf("%+v", p)
	}

	// both sides change: pull merges
	srv.EditPR("api", 1, func(p *bbtest.PR) { p.Title = "Rate limiting v2" })
	replaceIn(t, pr1, "<description>first</description>", "<description>local</description>")
	mustContain(t, mustRun(t, 0, "pull"), "merged")
	pr1 = "api/pr/1 Rate limiting v2.xml"
	mustContain(t, mustRun(t, 0, "commit"), "0 failed")
	if p := srv.PR("api", 1); p.Description != "local" || p.Title != "Rate limiting v2" {
		t.Fatalf("%+v", p)
	}

	// review, then merge (asks), decline
	replaceIn(t, pr1, "<my-review>none</my-review>", "<my-review>changes_requested</my-review>")
	mustContain(t, mustRun(t, 0, "status"), "request changes")
	mustContain(t, mustRun(t, 0, "commit"), "review  "+pr1+"   ok")
	if ps := srv.PR("api", 1).Participants; len(ps) != 1 || ps[0].State != "changes_requested" {
		t.Fatalf("%+v", ps)
	}
	replaceIn(t, pr1, "<state>open</state>", `<state strategy="squash">merged</state>`)
	mustContain(t, mustRun(t, 0, "actions", pr1), "merge", "squash", "note: commit will merge with squash")
	mustContain(t, mustRun(t, 1, "commit"), "needs confirmation: rerun with --allow merge")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run", "--allow", "merge"), "merge feat → main (squash), close source branch")
	mustContain(t, mustRun(t, 0, "commit", "--allow", "merge"), "merge   "+pr1+"   ok")
	if p := srv.PR("api", 1); p.State != "MERGED" || p.MergeUsed != "squash" {
		t.Fatalf("%+v", p)
	}
	if b, _ := os.ReadFile(pr1); !strings.Contains(string(b), "<state>merged</state>") {
		t.Fatalf("strategy dropped after the merge:\n%s", b)
	}
	replaceIn(t, "api/pr/2 Drop v1.xml", "<state>open</state>", "<state>declined</state>")
	mustRun(t, 0, "commit", "--allow", "decline")
	if srv.PR("api", 2).State != "DECLINED" {
		t.Fatal("declined")
	}

	// a new PR from a new file
	os.WriteFile("api/pr/new.xml", []byte(`<pullrequest><title>Third</title><source branch="feat3"/><description>d</description></pullrequest>`), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "create PR feat3 → main branch in api")
	mustRun(t, 0, "commit")
	if _, err := os.Stat("api/pr/3 Third.xml"); err != nil {
		t.Fatal("created file renamed:", err)
	}

	// pipeline files: big logs on demand, read-only
	run1 := "api/pipelines/1.xml"
	mustContain(t, mustRun(t, 0, "get", run1), "1-build.log")
	if b, _ := os.ReadFile("api/pipelines/1.files/1-build.log"); len(b) != 2000 {
		t.Fatal(len(b))
	}
	saved, _ := os.ReadFile(run1)
	replaceIn(t, run1, "<result>successful</result>", "<result>failed</result>")
	mustContain(t, mustRun(t, 1, "commit", run1), "pipeline runs are read-only")
	os.WriteFile(run1, saved, 0o644)

	// tail the pending run until it fails; its file is written back
	srv.AdvanceOnLog = true
	srv.Advance("api", 2) // the step starts
	out := mustRun(t, 1, "tail", "--interval", "1ms", "api/pipelines/2.xml")
	mustContain(t, out, "── Test ──", "running tests", "FAIL x", "── #2 failed in")
	if b, _ := os.ReadFile("api/pipelines/2.xml"); !strings.Contains(string(b), "<state>completed</state>") || !strings.Contains(string(b), "FAIL x") {
		t.Fatalf("written back:\n%s", b)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// docs and schema
	mustContain(t, mustRun(t, 0, "help", "bitbucket"), "gfs tail")
	mustContain(t, mustRun(t, 0, "schema", "bitbucket"), `<element name="pipeline">`)
}
