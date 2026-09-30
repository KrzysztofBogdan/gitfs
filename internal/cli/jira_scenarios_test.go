package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

// fullSite is jiraSite plus a service project SUP whose workflow needs a
// resolution to close, link types, and a few more issues.
func fullSite(t *testing.T) *jtest.Server {
	t.Helper()
	srv := jiraSite(t)
	srv.AddProject(jtest.Project{Key: "SUP", ID: "10001", Type: "service_desk"}, jtest.IssueType{ID: "3", Name: "Support"})
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "assignee", Name: "Assignee", Type: "user"},
		jtest.Field{ID: "description", Name: "Description", Type: "string"},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink"})
	srv.SetFields("SUP", "3",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "description", Name: "Description", Type: "string"})
	srv.EditHidden = []string{"resolution"}
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "12", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{
			{ID: "31", Name: "Resolve this issue", To: "Closed", From: []string{"Open"}, Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
			{ID: "41", Name: "Close as duplicate", To: "Closed", From: []string{"Open"}, Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
		}})
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	srv.AddUser(jtest.User{Account: "u:b", Name: "Bea", Email: "bea@x.com"})
	srv.AddUser(jtest.User{Account: "qm:h", Name: "hadas@example.com", Email: "hadas@example.com", Customer: true})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Write script", Reporter: "me", Updated: time.Now().Add(-time.Hour),
		Fields: map[string]any{"parent": map[string]any{"key": "GEN-1"}}})
	srv.AddIssue(jtest.Issue{Project: "SUP", Type: "3", Summary: "Promo code", Reporter: "qm:h", Updated: time.Now().Add(-time.Hour)})
	return srv
}

func cloneSite(t *testing.T, srv *jtest.Server, url string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", url, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	return filepath.Join(dir, "wt")
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJiraCloneForms(t *testing.T) {
	srv := fullSite(t)
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Ancient", Updated: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	base := "?base=" + srv.URL
	for _, c := range []struct {
		url  string
		want []string
		not  []string
	}{
		{"jira://acme.atlassian.net" + base, []string{"gen/GEN-1 Migrate auth.xml", "sup/SUP-1 Promo code.xml", "gen/GEN-3 Ancient.xml"}, nil},
		{"jira:https://acme.atlassian.net/browse/SUP-1" + base, []string{"sup/SUP-1 Promo code.xml"}, []string{"gen"}},
		{"jira://acme.atlassian.net" + base + "&exclude=GEN", []string{"sup/SUP-1 Promo code.xml"}, []string{"gen"}},
		{"jira://acme.atlassian.net/GEN" + base + "&since=2025-01-01", []string{"gen/GEN-1 Migrate auth.xml"}, []string{"gen/GEN-3 Ancient.xml"}},
		{"jira://acme.atlassian.net/GEN" + base + "&limit=1", []string{"gen/GEN-2 Write script.xml"}, []string{"gen/GEN-1 Migrate auth.xml"}}, // GEN-2 was updated last
	} {
		cloneSite(t, srv, c.url)
		for _, p := range c.want {
			if !exists(p) {
				t.Errorf("%s: %s missing", c.url, p)
			}
		}
		for _, p := range c.not {
			if exists(p) {
				t.Errorf("%s: %s must not exist", c.url, p)
			}
		}
	}
	mustContain(t, mustRun(t, 2, "clone", "jira://acme.atlassian.net?filter=GEN&since=soon"+strings.TrimPrefix(base, "?")), "since=soon")
}

func TestJiraPullPicksUpRemoteChanges(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	searches := srv.Count("POST", "/rest/api/3/search/jql")
	mustContain(t, mustRun(t, 0, "pull"), "Already up to date.")
	if n := srv.Count("POST", "/rest/api/3/search/jql") - searches; n != 1 {
		t.Fatalf("a pull with nothing new made %d searches", n)
	}
	gen1, sup1 := srv.ID("GEN-1"), srv.ID("SUP-1")
	srv.AddComment(gen1, jtest.Comment{Author: "u:b", Body: json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"looks good"}]}]}`)})
	for i := 0; i < 21; i++ {
		srv.AddWorklog(gen1, jtest.Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
	}
	srv.AddAttachment(gen1, jtest.Attachment{Filename: "log.txt", Mime: "text/plain", Data: []byte("log"), Author: "u:b"})
	srv.Edit(sup1, func(is *jtest.Issue) { is.Status = "Closed" })
	mustRun(t, 0, "pull")
	f := read(t, "gen/GEN-1 Migrate auth.xml")
	if !strings.Contains(f, "looks good") || strings.Count(f, "<worklog ") != 21 || !strings.Contains(f, `name="log.txt"`) {
		t.Fatalf("GEN-1 after pull:\n%s", f)
	}
	if !strings.Contains(read(t, "sup/SUP-1 Promo code.xml"), "<status>Closed</status>") {
		t.Fatal("transition not picked up")
	}
	srv.Move(srv.ID("GEN-2"), "SUP")
	mustContain(t, mustRun(t, 0, "pull"), "gen/GEN-2 Write script.xml -> sup/SUP-2 Write script.xml")
	srv.Delete(sup1)
	mustRun(t, 0, "pull")
	if !exists("sup/SUP-1 Promo code.xml") {
		t.Fatal("an incremental pull cannot see deletions")
	}
	mustContain(t, mustRun(t, 0, "pull", "--full"), "-  sup/SUP-1 Promo code.xml")
	cfg := read(t, ".gfs/config")
	os.WriteFile(".gfs/config", []byte(regexp.MustCompile(`url = (.*)`).ReplaceAllString(cfg, "url = $1&exclude=SUP")), 0o644)
	mustRun(t, 0, "pull")
	if exists("sup/SUP-2 Write script.xml") {
		t.Fatal("files of an excluded project must go")
	}
}

func TestJiraMoveWithLocalEdit(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "<summary>Migrate auth</summary>", "<summary>Migrate auth</summary>\n      <priority>High</priority>")
	srv.Move(srv.ID("GEN-1"), "SUP")
	mustRun(t, 0, "pull")
	f := read(t, "sup/SUP-2 Migrate auth.xml")
	if !strings.Contains(f, "<priority>High</priority>") || !strings.Contains(f, `key="SUP-2"`) || exists("gen/GEN-1 Migrate auth.xml") {
		t.Fatalf("the local edit must survive the move:\n%s", f)
	}
	mustContain(t, mustRun(t, 0, "status"), "update priority")
}

func TestJiraCommitScenarios(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)

	// create with a non-initial status, reassign by email
	os.WriteFile("gen/Rate limiter.xml", []byte(`<issue><summary>Rate limiter</summary><type>Task</type><status>Done</status>
  <assignee>bea@x.com</assignee><link type="blocks">GEN-1</link></issue>`), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "create Task in GEN")
	mustRun(t, 0, "commit")
	f := read(t, "gen/GEN-3 Rate limiter.xml")
	if !strings.Contains(f, "<status>Done</status>") || !strings.Contains(f, `<assignee account="u:b">Bea</assignee>`) || !strings.Contains(f, `<link id=`) {
		t.Fatalf("created file:\n%s", f)
	}

	// a transition that needs a resolution; two lead to Closed
	sup := "sup/SUP-1 Promo code.xml"
	replaceIn(t, sup, "<status>Open</status>", "<status>Closed</status>")
	mustContain(t, mustRun(t, 1, "commit", "--dry-run"), `several transitions lead to "Closed"`)
	replaceIn(t, sup, "<status>Closed</status>", "<status>Resolve this issue</status>")
	mustContain(t, mustRun(t, 1, "commit", "--dry-run"), `transition "Resolve this issue" requires <resolution>; add it`)
	replaceIn(t, sup, "<status>Resolve this issue</status>", "<status>Resolve this issue</status>\n      <resolution>Done</resolution>")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "would run  transition Open -> Closed (Resolve this issue) with <resolution>")
	mustRun(t, 0, "commit")
	if !strings.Contains(read(t, sup), "<status>Closed</status>") {
		t.Fatal("not closed")
	}

	// comments on a service project: internal is fine, public asks
	replaceIn(t, sup, "</issue>", `  <comment internal="true"><paragraph>checked the license</paragraph></comment>
    </issue>`)
	mustRun(t, 0, "commit")
	replaceIn(t, sup, "</issue>", `  <comment public="true"><paragraph>codes attached</paragraph></comment>
    </issue>`)
	mustContain(t, mustRun(t, 1, "commit"), "--allow reply")
	mustRun(t, 0, "commit", "--allow", "reply")
	if cs := srv.Issue(srv.ID("SUP-1")).Comments; len(cs) != 2 || *cs[0].Public || !*cs[1].Public {
		t.Fatal("visibility")
	}

	// worklog, attachment, link delete, sub-tasks
	gen1 := "gen/GEN-1 Migrate auth.xml"
	replaceIn(t, gen1, "</issue>", `  <worklog><started>2026-09-29T09:00:00.000+0200</started><spent>2h</spent></worklog>
    </issue>`)
	os.MkdirAll("gen/GEN-1 Migrate auth.files", 0o755)
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("trace"), 0o644)
	mustRun(t, 0, "commit")
	is := srv.Issue(srv.ID("GEN-1"))
	if len(is.Worklogs) != 1 || len(is.Attachments) != 1 {
		t.Fatalf("%+v", is)
	}
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("changed"), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "update of attachments is not supported")
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("trace"), 0o644)
	os.Remove(gen1)
	mustContain(t, mustRun(t, 1, "commit", "--allow", "delete"), "delete or re-parent its sub-tasks first (GEN-2)")
}

func TestJiraLockConflictMerged(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "<summary>Migrate auth</summary>", "<summary>Migrate auth</summary>\n      <priority>Low</priority>")
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Migrate auth now" })
	mustContain(t, mustRun(t, 0, "commit"), "merged")
	is := srv.Issue(srv.ID("GEN-1"))
	if is.Summary != "Migrate auth now" || is.Fields["priority"].(map[string]any)["name"] != "Low" {
		t.Fatalf("%+v", is)
	}
	if !exists("gen/GEN-1 Migrate auth now.xml") {
		t.Fatal("renamed after the merged summary")
	}
}

func TestJiraActionsAndGeneratedFiles(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	got := mustRun(t, 0, "actions", "sup/SUP-1 Promo code.xml")
	mustContain(t, got, "status: Open", "Resolve this issue", "-> Closed", "requires <resolution>")
	mustContain(t, read(t, ".workflows.xml"), "transitions need the Administer Jira permission")
	replaceIn(t, ".people.xml", ">Me</person>", ">Myself</person>")
	mustContain(t, mustRun(t, 1, "commit"), ".people.xml")
}

// A fetched attachment follows its issue when a new summary renames the file.
func TestJiraRenameMovesSidecar(t *testing.T) {
	srv := fullSite(t)
	srv.AddAttachment(srv.ID("GEN-1"), jtest.Attachment{Filename: "log.txt", Mime: "text/plain", Data: []byte("log"), Author: "me"})
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	mustRun(t, 0, "get", "gen/GEN-1 Migrate auth.files/log.txt")
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Auth: OIDC/SAML" })
	mustRun(t, 0, "pull")
	if !exists("gen/GEN-1 Auth: OIDC-SAML.files/log.txt") || exists("gen/GEN-1 Migrate auth.files") {
		t.Fatal("the sidecar must move with the file")
	}
	if got := read(t, "gen/GEN-1 Auth: OIDC-SAML.files/log.txt"); got != "log" {
		t.Fatal(got)
	}
}
