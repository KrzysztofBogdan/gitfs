package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var bg = context.Background()

const (
	typeSelect = "com.atlassian.jira.plugin.system.customfieldtypes:select"
	typeText   = "com.atlassian.jira.plugin.system.customfieldtypes:textfield"
)

// site is a fake Jira with a software project GEN (Task, Sub-task) and a
// service project SUP (Support), at a fixed clock the test can move.
func site(t *testing.T) (*jtest.Server, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	srv := jtest.New()
	t.Cleanup(srv.Close)
	srv.Clock = func() time.Time { return now }
	srv.AddProject(jtest.Project{Key: "GEN", ID: "10017"}, jtest.IssueType{ID: "1", Name: "Task"}, jtest.IssueType{ID: "2", Name: "Sub-task", Subtask: true})
	srv.AddProject(jtest.Project{Key: "SUP", ID: "10001", Type: "service_desk"}, jtest.IssueType{ID: "3", Name: "Support"})
	common := []jtest.Field{
		{ID: "summary", Name: "Summary", Type: "string", Required: true},
		{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		{ID: "description", Name: "Description", Type: "string"},
		{ID: "priority", Name: "Priority", Type: "priority"},
		{ID: "assignee", Name: "Assignee", Type: "user"},
		{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
	}
	srv.SetFields("GEN", "1", append(common, jtest.Field{ID: "customfield_10050", Name: "Team", Type: "option", Custom: typeSelect})...)
	srv.SetFields("GEN", "2", append(common, jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})...)
	srv.SetFields("SUP", "3", append(common, jtest.Field{ID: "customfield_10040", Name: "Users", Type: "string", Custom: typeText})...)
	srv.AddUser(jtest.User{Account: "712020:a", Name: "Adam"})
	srv.AddUser(jtest.User{Account: "qm:h", Name: "hadas@example.com", Email: "hadas@example.com", Customer: true})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Migrate auth", Assignee: "712020:a", Reporter: "me",
		Created: now.Add(-48 * time.Hour), Updated: now.Add(-3 * time.Hour),
		Fields: map[string]any{"priority": map[string]any{"name": "High"}, "labels": []string{"backend"},
			"customfield_10050": map[string]any{"id": "7", "value": "Platform"}, "customfield_10019": "0|i06csf:",
			"description": json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"Move to OIDC"}]}`))}})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "2", Summary: "Write script", Reporter: "me",
		Created: now.Add(-24 * time.Hour), Updated: now.Add(-2 * time.Hour), Fields: map[string]any{"parent": map[string]any{"key": "GEN-1"}}})
	srv.AddIssue(jtest.Issue{Project: "SUP", Type: "3", Summary: "Promo code", Reporter: "qm:h",
		Created: now.Add(-5 * time.Hour), Updated: now.Add(-time.Hour), Fields: map[string]any{"customfield_10040": "1-10"}})
	return srv, &now
}

func open(t *testing.T, srv *jtest.Server, now *time.Time, sel selection, cache string) *session {
	t.Helper()
	s, err := openSession(bg, target{base: srv.URL, email: "me@x.com", token: "t", sel: sel})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	if cache != "" {
		s.UseCache(cache)
	}
	return s
}

// paths lists the resource paths, sorted.
func paths(l adapter.Listing) []string {
	var out []string
	for _, r := range l.Resources {
		out = append(out, r.Path)
	}
	sort.Strings(out)
	return out
}

func find(t *testing.T, l adapter.Listing, path string) *adapter.Resource {
	t.Helper()
	for i := range l.Resources {
		if l.Resources[i].Path == path {
			return &l.Resources[i]
		}
	}
	t.Fatalf("%s not listed: %v", path, paths(l))
	return nil
}

func TestOpenSelection(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{exclude: []string{"SUP"}}, "")
	if len(s.projects) != 1 || s.projects["GEN"] == nil || s.projects["GEN"].JSM {
		t.Fatalf("%+v", s.projects)
	}
	s = open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	if !s.projects["SUP"].JSM {
		t.Fatal("SUP is a service project")
	}
	if _, err := openSession(bg, target{base: srv.URL, sel: selection{keys: []string{"GEN", "NOPE"}}}); err == nil || err.Error() != "project NOPE not found or not visible" {
		t.Fatalf("err %v", err)
	}
	srv.Fail["GET /rest/api/3/project/search"] = 401
	_, err := openSession(bg, target{base: srv.URL, host: "acme.atlassian.net", email: "me@x.com"})
	if err == nil || !strings.HasSuffix(err.Error(), " (run gfs auth login jira://acme.atlassian.net)") {
		t.Fatalf("err %v", err)
	}
}

func TestListFullThenIncremental(t *testing.T) {
	srv, now := site(t)
	cache := t.TempDir()
	s := open(t, srv, now, selection{}, cache)
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".people.xml", ".workflows.xml", "gen/GEN-1 Migrate auth.xml", "gen/GEN-2 Write script.xml", "sup/SUP-1 Promo code.xml"}
	if !l.Full || strings.Join(paths(l), "|") != strings.Join(want, "|") {
		t.Fatalf("full listing: %v %v", l.Full, paths(l))
	}
	if l.Cursor != "GEN=2026-09-29T12:00:00Z,SUP=2026-09-29T12:00:00Z" {
		t.Fatal(l.Cursor)
	}
	gen1 := xmltree.Print(find(t, l, "gen/GEN-1 Migrate auth.xml").Root, 0)
	for _, part := range []string{`<field id="customfield_10050" name="Team">`, `<option id="7">Platform</option>`,
		`<assignee account="712020:a">Adam</assignee>`, `<paragraph>Move to OIDC</paragraph>`} {
		if !strings.Contains(gen1, part) {
			t.Errorf("GEN-1 lacks %s:\n%s", part, gen1)
		}
	}
	if strings.Contains(gen1, "customfield_10019") {
		t.Error("Rank is noise and must not be shown")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// pull --full rereads the field metadata even with a warm cache
	srv.Requests = nil
	if _, err := s.List(bg, ""); err != nil || srv.Count("GET", "/rest/api/3/issue/createmeta/GEN/issuetypes") == 0 {
		t.Fatalf("a full listing must refresh metadata: %v", err)
	}

	// a new process, 20 minutes later: one search, no metadata requests
	*now = now.Add(20 * time.Minute)
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Migrate auth to OIDC" })
	s = open(t, srv, now, selection{}, cache)
	srv.Requests = nil
	l, err = s.List(bg, l.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if l.Full || len(l.FullDirs) != 0 || strings.Join(paths(l), "|") != ".people.xml|.workflows.xml|gen/GEN-1 Migrate auth to OIDC.xml" {
		t.Fatalf("incremental: %v %v %v", l.Full, l.FullDirs, paths(l))
	}
	if srv.Count("POST", "/rest/api/3/search/jql") != 1 || srv.Count("GET", "/rest/api/3/issue/createmeta") != 0 || srv.Count("GET", "/rest/api/3/mypermissions") != 0 {
		t.Fatalf("requests: %v", srv.Requests)
	}
}

func TestListProjectsJoinAndLeave(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{}, "")
	l, err := s.List(bg, "GEN=2026-09-29T11:00:00Z,OLD=2026-09-29T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if l.Full || strings.Join(l.FullDirs, ",") != "old,sup" {
		t.Fatalf("FullDirs %v", l.FullDirs)
	}
	find(t, l, "sup/SUP-1 Promo code.xml")
	if !strings.Contains(l.Cursor, "SUP=") || strings.Contains(l.Cursor, "OLD=") {
		t.Fatal(l.Cursor)
	}
}

func TestListTopUps(t *testing.T) {
	srv, now := site(t)
	srv.WorklogLimit, srv.CommentLimit = 2, 1
	gen2 := srv.ID("GEN-2")
	for i := 0; i < 3; i++ {
		srv.AddWorklog(gen2, jtest.Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
		srv.AddComment(gen2, jtest.Comment{Author: "712020:a", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"c"}]}`))})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	root := find(t, l, "gen/GEN-2 Write script.xml").Root
	if len(root.ChildrenNamed("worklog")) != 3 || len(root.ChildrenNamed("comment")) != 3 {
		t.Fatalf("worklogs %d comments %d", len(root.ChildrenNamed("worklog")), len(root.ChildrenNamed("comment")))
	}
	if srv.Count("GET", "/rest/api/3/issue/"+gen2+"/worklog") != 1 || srv.Count("GET", "/rest/api/3/issue/"+gen2+"/comment") != 1 {
		t.Fatal(srv.Requests)
	}
}

func TestFullListingLimitMidPage(t *testing.T) {
	srv, now := site(t)
	for i := 0; i < 250; i++ {
		srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: fmt.Sprint("bulk ", i), Updated: now.Add(-time.Duration(i+10) * time.Minute)})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}, limit: 150}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(l.Resources) - 2; n != 150 {
		t.Fatalf("%d issues listed", n)
	}
	if srv.Count("POST", "/rest/api/3/search/jql") != 2 {
		t.Fatalf("searches: %d", srv.Count("POST", "/rest/api/3/search/jql"))
	}
	if !strings.HasPrefix(l.Resources[0].Path, "gen/GEN-") || strings.Contains(strings.Join(paths(l), "|"), "Migrate auth") {
		t.Fatal("limit keeps the most recently updated issues") // GEN-1 was updated 3h ago, bulk ones within 2.7h
	}
}

func TestListRateLimit(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	srv.RateLimit = 3 // fewer than the retries: the listing succeeds
	if _, err := s.List(bg, ""); err != nil {
		t.Fatal(err)
	}
	srv.RateLimit = 100
	_, err := s.List(bg, "GEN=2026-09-29T11:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429: rate limited") || !strings.Contains(err.Error(), "gfs pull --full") {
		t.Fatalf("err %v", err)
	}
}

func TestFetch(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	gen1, gen2 := srv.ID("GEN-1"), srv.ID("GEN-2")
	r, err := s.Fetch(bg, gen1)
	if err != nil || r.Path != "gen/GEN-1 Migrate auth.xml" || r.Version != "2026-09-29T09:00:00.000+0000" {
		t.Fatalf("%+v %v", r, err)
	}
	srv.Move(gen1, "SUP")
	if _, err := s.Fetch(bg, gen1); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("moved out of the selection: %v", err)
	}
	srv.Delete(gen2)
	if _, err := s.Fetch(bg, gen2); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	p, err := s.Fetch(bg, peopleID)
	if err != nil || p.Path != ".people.xml" || p.Root.Name != "people" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestIssuePathsAndRename(t *testing.T) {
	srv, now := site(t)
	for _, summary := range []string{"a/b", ".hidden", "notes.files", "   "} {
		srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: summary})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gen/GEN-3 a-b.xml", "gen/GEN-4 .hidden.xml", "gen/GEN-5 notes.files_.xml", "gen/GEN-6.xml"} {
		find(t, l, p)
	}
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Renamed" })
	r, _ := s.Fetch(bg, srv.ID("GEN-1"))
	if r.Path != "gen/GEN-1 Renamed.xml" {
		t.Fatal(r.Path)
	}
}

func TestPeopleResource(t *testing.T) {
	srv, now := site(t)
	srv.AddComment(srv.ID("SUP-1"), jtest.Comment{Author: "qm:h", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"x:9","text":"@Zed"}}]}`))})
	s := open(t, srv, now, selection{}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	got := xmltree.Print(find(t, l, ".people.xml").Root, 0)
	want := `<people id="people">
  <person account="712020:a" type="atlassian" active="true">Adam</person>
  <person account="me" type="atlassian" active="true" email="me@x.com">Me</person>
  <person account="x:9" active="true">Zed</person>
  <person account="qm:h" type="customer" active="true" email="hadas@example.com">hadas@example.com</person>
</people>`
	if got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestWorkflowsFile(t *testing.T) {
	srv, now := site(t)
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "11", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{{ID: "5", Name: "Resolve", To: "Closed", From: []string{"Open"}},
			{ID: "6", Name: "Reopen", To: "Open"}}})
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	wf, err := s.workflows(bg, true)
	if err != nil {
		t.Fatal(err)
	}
	got := xmltree.Print(wf.Root, 0)
	want := `<workflows id="workflows">
  <note>transitions need the Administer Jira permission; run gfs actions with an issue file to see what you can do to it</note>
  <workflow name="SUP Support">
    <scheme project="SUP" type="Support"/>
    <status name="Open" category="new"/>
    <status name="Closed" category="done"/>
  </workflow>
</workflows>`
	if got != want {
		t.Fatalf("non-admin:\n%s", got)
	}
	srv.Admin = true
	wf, _ = s.workflows(bg, true)
	got = xmltree.Print(wf.Root, 0)
	want = `<workflows id="workflows">
  <workflow name="Support flow">
    <scheme project="SUP" type="Support"/>
    <status name="Open" category="new"/>
    <status name="Closed" category="done"/>
    <transition name="Reopen" to="Open"/>
    <transition name="Resolve" to="Closed">
      <from>Open</from>
    </transition>
  </workflow>
</workflows>`
	if got != want {
		t.Fatalf("admin:\n%s", got)
	}
}

func TestDownload(t *testing.T) {
	srv, now := site(t)
	srv.AddAttachment(srv.ID("GEN-1"), jtest.Attachment{ID: "900", Filename: "log.txt", Mime: "text/plain", Data: []byte("hello"), Author: "me"})
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	var b strings.Builder
	info, err := s.Download(bg, srv.ID("GEN-1"), "900", &b)
	if err != nil || b.String() != "hello" || info.Version != "-" || info.Size != 5 {
		t.Fatalf("%+v %v %q", info, err, b.String())
	}
}

func TestCursorCodec(t *testing.T) {
	m := parseCursor("SUP=2026-09-29T12:00:00Z,GEN=2026-09-29T11:00:00Z")
	if len(m) != 2 || formatCursor(m) != "GEN=2026-09-29T11:00:00Z,SUP=2026-09-29T12:00:00Z" {
		t.Fatal(m)
	}
	for _, bad := range []string{"", "2026-09-29T12:00:00Z", "GEN=yesterday", "g e n=2026-09-29T12:00:00Z"} {
		if parseCursor(bad) != nil {
			t.Errorf("%q must mean a full listing", bad)
		}
	}
	since := time.Date(2026, 9, 29, 11, 0, 30, 0, time.UTC)
	if m := windowMinutes(since, since.Add(19*time.Minute)); m != 29 {
		t.Fatal(m)
	}
}
