package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// editSite is site() plus options for GEN's Team field, a raw field and two
// users who share a name.
func editSite(t *testing.T) (*jtest.Server, *session) {
	t.Helper()
	srv, now := site(t)
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "description", Name: "Description", Type: "string"},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "assignee", Name: "Assignee", Type: "user"},
		jtest.Field{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "customfield_10050", Name: "Team", Type: "option", Custom: typeSelect,
			Options: []jtest.Option{{ID: "7", Value: "Platform"}, {ID: "8", Value: "Core"}}},
		jtest.Field{ID: "customfield_10060", Name: "Squad", Type: "team", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:atlassian-team"})
	srv.EditHidden = []string{"resolution"}
	srv.AddUser(jtest.User{Account: "u:s1", Name: "Sam"})
	srv.AddUser(jtest.User{Account: "u:s2", Name: "Sam"})
	srv.AddUser(jtest.User{Account: "u:b", Name: "Bea", Email: "bea@x.com"})
	srv.AddUser(jtest.User{Account: "u:h", Name: "Hidden Email"}) // email not exposed
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Fields["customfield_10060"] = map[string]any{"id": "t1", "name": "Core"} })
	return srv, open(t, srv, now, selection{keys: []string{"GEN"}}, "")
}

// change fetches the issue, lets edit change a copy, and returns the request
// for the given update groups, with results primed for applyEdit.
func change(t *testing.T, s *session, id string, edit func(root *xmltree.Node), groups ...string) (adapter.ApplyRequest, []adapter.Result) {
	t.Helper()
	base, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	local := *base
	local.Root = base.Root.Clone()
	edit(local.Root)
	req := adapter.ApplyRequest{Local: &local, Base: base}
	for _, g := range groups {
		req.Actions = append(req.Actions, adapter.Action{Verb: "update", Group: g})
	}
	out := make([]adapter.Result, len(req.Actions))
	for i, a := range req.Actions {
		out[i].Action = a
	}
	return req, out
}

func setText(root *xmltree.Node, name, text string) {
	n := root.Child(name)
	if n == nil {
		n = el(name)
		root.Children = append(root.Children, n)
	}
	n.Attrs = nil
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

func remove(root *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range root.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	root.Children = kept
}

func apply(t *testing.T, s *session, req adapter.ApplyRequest, out []adapter.Result) []adapter.Result {
	t.Helper()
	ic, err := s.issueCtx(bg, req.Local)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planEdit(bg, ic, req)
	if err != nil {
		t.Fatal(err)
	}
	s.applyEdit(bg, ic, plan, out)
	return out
}

func TestEditFields(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "summary", "Migrate auth to OIDC")
		setText(r, "priority", "Low")
		r.Child("labels").Children = []*xmltree.Node{textEl("label", "backend"), textEl("label", "security")}
		setText(r, "assignee", "bea@x.com")
		team := fieldsByID(r)["customfield_10050"]
		team.Children = []*xmltree.Node{textEl("option", "Core")}
		desc := r.Child("description")
		desc.Children = []*xmltree.Node{textEl("paragraph", "Use Keycloak")}
	}, "summary", "priority", "assignee", "labels", "field", "description")
	for _, r := range apply(t, s, req, out) {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if srv.Count("PUT", "/rest/api/3/issue/") != 1 {
		t.Fatal("one PUT for all fields")
	}
	is := srv.Issue(id)
	if is.Summary != "Migrate auth to OIDC" || is.Assignee != "u:b" || is.Fields["customfield_10050"].(map[string]any)["id"] != "8" {
		t.Fatalf("%+v", is)
	}
	r, _ := s.Fetch(bg, id)
	got := xmltree.Print(r.Root, 0)
	for _, want := range []string{"<priority>Low</priority>", "<label>security</label>", "<paragraph>Use Keycloak</paragraph>", `<assignee account="u:b">Bea</assignee>`} {
		if !strings.Contains(got, want) {
			t.Errorf("after edit, missing %s", want)
		}
	}
}

func TestEditRefusals(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "type", "Bug")
		setText(r, "resolution", "Done")
		setText(r, "assignee", "Sam")
		fieldsByID(r)["customfield_10060"].Children[0].Text = `{"id":"t2"}`
		setText(r, "summary", "Still sent")
	}, "type", "resolution", "assignee", "field", "summary")
	apply(t, s, req, out)
	want := []string{
		"<type> is read-only: Jira cannot change it in place",
		"<resolution> can only be set together with a status change on this issue",
		`<assignee>: "Sam" matches 2 users: Sam (u:s1), Sam (u:s2); write account="…" (see .people.xml)`,
		`<field id="customfield_10060"> (Squad) is read-only in gfs`,
		"",
	}
	for i, r := range out {
		got := ""
		if r.Err != nil {
			got = r.Err.Error()
		}
		if got != want[i] {
			t.Errorf("%s: %q, want %q", r.Action.Group, got, want[i])
		}
	}
	if srv.Issue(id).Summary != "Still sent" {
		t.Fatal("the valid field must still be sent")
	}
}

func TestEditPeopleRules(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		r.Child("assignee").Children[0].Text = "Someone else" // account kept
	}, "assignee")
	apply(t, s, req, out)
	if out[0].Err == nil || !strings.Contains(out[0].Err.Error(), `to reassign, remove account="712020:a"`) {
		t.Fatalf("%v", out[0].Err)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) { remove(r, "assignee") }, "assignee")
	if apply(t, s, req, out)[0].Err != nil || srv.Issue(id).Assignee != "" {
		t.Fatalf("unassign: %v %q", out[0].Err, srv.Issue(id).Assignee)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) { setText(r, "assignee", "hidden@x.com") }, "assignee")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `<assignee>: no user matches "hidden@x.com"` {
		t.Fatalf("%v", out[0].Err)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) {
		a := el("assignee", "account", "u:s2")
		a.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: "Sam"}}
		remove(r, "assignee")
		r.Children = append(r.Children, a)
	}, "assignee")
	if apply(t, s, req, out)[0].Err != nil || srv.Issue(id).Assignee != "u:s2" {
		t.Fatalf("by account: %v", out[0].Err)
	}
}

func TestEditJiraFieldError(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		fieldsByID(r)["customfield_10050"].Children = []*xmltree.Node{textEl("option", "Nope")}
		setText(r, "summary", "Not sent")
	}, "summary", "field")
	apply(t, s, req, out)
	if out[1].Err == nil || out[1].Err.Error() != `<field id="customfield_10050">: Option value 'Nope' is not valid` || out[1].Code != "400" {
		t.Fatalf("%v %s", out[1].Err, out[1].Code)
	}
	if out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), "not sent: another field was refused") {
		t.Fatalf("summary: %v", out[0].Err)
	}
	if srv.Issue(id).Summary == "Not sent" {
		t.Fatal("Jira applies an edit all or nothing")
	}
}
