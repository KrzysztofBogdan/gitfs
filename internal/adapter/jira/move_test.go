package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// moveSite is site() plus OPS, a twin of GEN, and GEN-3: a Task with a
// component OPS lacks, In Progress.
func moveSite(t *testing.T) (*jtest.Server, *session) {
	t.Helper()
	srv, now := site(t)
	srv.AddProject(jtest.Project{Key: "OPS", ID: "10020"}, jtest.IssueType{ID: "1", Name: "Task"}, jtest.IssueType{ID: "2", Name: "Sub-task", Subtask: true})
	common := []jtest.Field{
		{ID: "summary", Name: "Summary", Type: "string", Required: true},
		{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		{ID: "description", Name: "Description", Type: "string"},
		{ID: "priority", Name: "Priority", Type: "priority"},
		{ID: "assignee", Name: "Assignee", Type: "user"},
		{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
	}
	team := jtest.Field{ID: "customfield_10050", Name: "Team", Type: "option", Custom: typeSelect,
		Options: []jtest.Option{{ID: "7", Value: "Platform"}}}
	srv.SetFields("GEN", "1", append(common, team,
		jtest.Field{ID: "components", Name: "Components", Type: "array", Items: "component", Options: []jtest.Option{{ID: "100", Value: "billing"}}})...)
	srv.SetFields("OPS", "1", append(common, team,
		jtest.Field{ID: "components", Name: "Components", Type: "array", Items: "component", Options: []jtest.Option{{ID: "200", Value: "api"}}})...)
	srv.SetFields("OPS", "2", append(common, jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})...)
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Rotate keys", Status: "In Progress", Reporter: "me",
		Fields: map[string]any{"components": []any{map[string]any{"id": "100", "name": "billing"}}}})
	return srv, open(t, srv, now, selection{keys: []string{"GEN", "OPS", "SUP"}}, "")
}

// moved is the request for moving the issue at key to folder dir, after
// edit changed the local copy.
func moved(t *testing.T, srv *jtest.Server, s *session, key, dir string, edit func(*xmltree.Node)) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, srv.ID(key))
	if err != nil {
		t.Fatal(err)
	}
	local := *base
	local.Root = base.Root.Clone()
	local.Path = dir + "/" + base.Path[strings.Index(base.Path, "/")+1:]
	if edit != nil {
		edit(local.Root)
	}
	return adapter.ApplyRequest{Local: &local, Base: base,
		Actions: []adapter.Action{{Verb: "move", From: base.Path, To: local.Path}}}
}

func TestPathModelAndDescribeMove(t *testing.T) {
	if !adapter.IsMove((&Adapter{}).PathModel(), "gen/a.xml", "ops/a.xml") || adapter.IsMove((&Adapter{}).PathModel(), "gen/a.xml", "gen/b.xml") {
		t.Fatal("folder change is a move, a rename in place is not")
	}
	if (&CustomerAdapter{}).PathModel() != adapter.Flat {
		t.Fatal("customer mode stays flat")
	}
	a := adapter.Action{Verb: "move", From: "gen/GEN-1 x.xml", To: "ops/GEN-1 x.xml"}
	(&Adapter{}).Describe(&a, nil)
	if !a.Preview || a.Class != "move" || a.Detail != "move to OPS" {
		t.Fatalf("%+v", a)
	}
}

func TestCheckMoveLossless(t *testing.T) {
	srv, s := moveSite(t)
	got := s.Check(bg, moved(t, srv, s, "GEN-1", "ops", nil))
	if got[0].Err != nil || got[0].Class != "move" || got[0].Detail != "move to OPS as Task, status To Do" || got[0].Warning != "" {
		t.Fatalf("%+v", got[0])
	}
	if writes(srv) != 0 {
		t.Fatal("Check must not write")
	}
}

func TestCheckMoveLossy(t *testing.T) {
	srv, s := moveSite(t)
	got := s.Check(bg, moved(t, srv, s, "GEN-3", "ops", nil))
	if got[0].Err != nil || got[0].Class != "delete" || got[0].Warning != "drops component billing" {
		t.Fatalf("%+v", got[0])
	}
	got = s.Check(bg, moved(t, srv, s, "GEN-1", "sup", nil))
	w := got[0].Warning
	for _, part := range []string{"type Task -> Support (SUP has no Task)", "drops <field Team>",
		"will not appear in the customer portal: Request Type is empty"} {
		if !strings.Contains(w, part) {
			t.Errorf("warning lacks %q: %s", part, w)
		}
	}
	if got[0].Class != "delete" {
		t.Fatal(got[0].Class)
	}
}

func TestCheckMoveRefusals(t *testing.T) {
	srv, s := moveSite(t)
	cases := []struct {
		key, dir string
		edit     func(*xmltree.Node)
		want     string
	}{
		{"GEN-2", "ops", nil, "sub-tasks move with their parent: move GEN-1 instead"},
		{"GEN-1", "ops", func(r *xmltree.Node) { setText(r, "type", "Bug") }, `OPS has no issue type "Bug" (types: Task)`},
		{"GEN-1", "ops", func(r *xmltree.Node) { setText(r, "status", "Blocked") }, `OPS's Task workflow has no status "Blocked" (statuses: To Do, In Progress, Done)`},
		{"GEN-1", "zzz", nil, `no project "zzz" in this tree; gfs does not create projects (projects: gen, ops, sup)`},
	}
	for _, c := range cases {
		got := s.Check(bg, moved(t, srv, s, c.key, c.dir, c.edit))
		if got[0].Err == nil || got[0].Err.Error() != c.want {
			t.Errorf("%s -> %s: %v, want %q", c.key, c.dir, got[0].Err, c.want)
		}
	}
}

func TestCheckMoveRequired(t *testing.T) {
	srv, s := moveSite(t)
	srv.SetFields("OPS", "1", append(srv.Fields("OPS", "1"),
		jtest.Field{ID: "customfield_10070", Name: "Customer", Type: "string", Custom: typeText, Required: true})...)
	got := s.Check(bg, moved(t, srv, s, "GEN-1", "ops", nil))
	if got[0].Err == nil || got[0].Err.Error() != `OPS requires <field id="customfield_10070"> (Customer) for Task issues; add it to the file` {
		t.Fatal(got[0].Err)
	}
	got = s.Check(bg, moved(t, srv, s, "GEN-1", "ops", func(r *xmltree.Node) {
		r.Children = append(r.Children, parse(t, `<field id="customfield_10070">Acme</field>`))
	}))
	if got[0].Err != nil {
		t.Fatal(got[0].Err)
	}
}
