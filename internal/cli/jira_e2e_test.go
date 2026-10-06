package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

// jiraSite is a fake Jira with one project GEN (Task) holding GEN-1.
func jiraSite(t *testing.T) *jtest.Server {
	t.Helper()
	srv := jtest.New()
	t.Cleanup(srv.Close)
	srv.AddProject(jtest.Project{Key: "GEN", ID: "10017"}, jtest.IssueType{ID: "1", Name: "Task"})
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Migrate auth", Reporter: "me", Updated: time.Now().Add(-time.Hour)})
	t.Setenv("GFS_JIRA_TOKEN", "t")
	t.Setenv("GFS_JIRA_EMAIL", "me@x.com")
	return srv
}

func TestJiraCloneEditCommit(t *testing.T) {
	srv := jiraSite(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustContain(t, mustRun(t, 0, "clone", "jira://acme.atlassian.net/GEN?base="+srv.URL), "Cloned 3 resources")
	t.Chdir(filepath.Join(dir, "gen"))
	for _, p := range []string{"gen/GEN-1 Migrate auth.xml", ".people.xml", ".workflows.xml", ".gfs/cache/jira/meta.json"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	file := "gen/GEN-1 Migrate auth.xml"
	replaceIn(t, file, "<summary>Migrate auth</summary>", "<summary>Migrate auth to OIDC</summary>")
	replaceIn(t, file, "<status>To Do</status>", "<status>In Progress</status>")
	mustContain(t, mustRun(t, 0, "status"), "update summary", "transition to In Progress")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "would run  transition To Do -> In Progress (Start)")
	mustRun(t, 0, "commit")
	if _, err := os.Stat("gen/GEN-1 Migrate auth to OIDC.xml"); err != nil {
		t.Fatalf("the file follows the summary: %v", err)
	}
	if is := srv.Issue(srv.ID("GEN-1")); is.Status != "In Progress" || is.Summary != "Migrate auth to OIDC" {
		t.Fatalf("%+v", is)
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
}

// A new issue and new children naming it by file commit together; the
// children's files sort before the parent's.
func TestJiraNewParentAndChildren(t *testing.T) {
	srv := jiraSite(t)
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink"})
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "jira://acme.atlassian.net/GEN?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "gen"))
	issue := func(name, extra string) {
		os.WriteFile("gen/"+name+".xml", []byte(`<issue><summary>`+name+`</summary><type>Task</type>`+extra+`</issue>`), 0o644)
	}
	key := func(summary string) string {
		m, _ := filepath.Glob("gen/GEN-* " + summary + ".xml")
		if len(m) != 1 {
			t.Fatalf("%s: %v", summary, m)
		}
		return strings.Fields(filepath.Base(m[0]))[0]
	}
	parentOf := func(k string) any {
		p, _ := srv.Issue(srv.ID(k)).Fields["parent"].(map[string]any)
		return p["key"]
	}

	issue("Z epic", "")
	issue("A login page", `<parent file="Z epic.xml"/>`)
	issue("B button", `<parent file="A login page.xml"/>`)
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "parent: Z epic.xml (created in this commit)", "0 failed")
	mustRun(t, 0, "commit")
	epic, page, button := key("Z epic"), key("A login page"), key("B button")
	if parentOf(page) != epic || parentOf(button) != page {
		t.Fatalf("%s<-%v %s<-%v", page, parentOf(page), button, parentOf(button))
	}
	if b, _ := os.ReadFile("gen/" + button + " B button.xml"); !strings.Contains(string(b), "<parent>"+page+"</parent>") {
		t.Fatalf("%s", b)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// a parent that fails leaves its child new; both go next time
	issue("C child", `<parent file="D bad.xml"/>`)
	os.WriteFile("gen/D bad.xml", []byte(`<issue><summary>D bad</summary><type>Nope</type></issue>`), 0o644)
	mustContain(t, mustRun(t, 1, "commit"), "parent gen/D bad.xml was not created")
	replaceIn(t, "gen/D bad.xml", "<type>Nope</type>", "<type>Task</type>")
	mustRun(t, 0, "commit")
	if parentOf(key("C child")) != key("D bad") {
		t.Fatal(parentOf(key("C child")))
	}

	// an existing issue re-parents onto an issue created in the same commit
	issue("E epic", "")
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "</summary>", `</summary><parent file="E epic.xml"/>`)
	mustRun(t, 0, "commit")
	if parentOf("GEN-1") != key("E epic") {
		t.Fatal(parentOf("GEN-1"))
	}
}
