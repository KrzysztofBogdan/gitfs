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
