package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func tr(id, name, to string) apiTransition {
	t := apiTransition{ID: id, Name: name}
	t.To.Name = to
	return t
}

func TestChooseTransition(t *testing.T) {
	ts := []apiTransition{tr("1", "Start", "In Progress"), tr("2", "Resolve this issue", "Closed"), tr("3", "Close as duplicate", "Closed")}
	cases := []struct{ want, id, err string }{
		{"in progress", "1", ""},
		{"Resolve this issue", "2", ""},
		{"close AS duplicate", "3", ""},
		{"Closed", "", `several transitions lead to "Closed": "Close as duplicate", "Resolve this issue"; write the transition name in <status> instead`},
		{"Done", "", `"Done" is not reachable from the current status in one step; reachable: "Closed", "In Progress"`},
	}
	for _, c := range cases {
		got, err := chooseTransition(ts, c.want)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%s: err %v", c.want, err)
			}
			continue
		}
		if err != nil || got.ID != c.id {
			t.Errorf("%s: got %+v %v", c.want, got, err)
		}
	}
	if _, err := chooseTransition(nil, "Done"); err == nil || !strings.Contains(err.Error(), "no transitions are available") {
		t.Fatal(err)
	}
}

// supportFlow gives SUP a workflow with two ways into Closed; resolving needs a resolution.
func supportFlow(srv *jtest.Server) {
	srv.SetFields("SUP", "3",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "customfield_10040", Name: "Users", Type: "string", Custom: typeText})
	srv.EditHidden = []string{"resolution"}
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "11", Name: "In Progress", Category: "indeterminate"},
			{ID: "12", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{
			{ID: "21", Name: "Start", To: "In Progress", From: []string{"Open"}},
			{ID: "31", Name: "Resolve this issue", To: "Closed", From: []string{"In Progress"},
				Screen: []jtest.ScreenField{{ID: "resolution", Required: true}, {ID: "customfield_10040"}, {ID: "comment"}}},
			{ID: "41", Name: "Close as duplicate", To: "Closed", From: []string{"In Progress"},
				Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
		}})
	srv.Edit(srv.ID("SUP-1"), func(is *jtest.Issue) { is.Status = "In Progress" })
}

func TestTransitionWithScreenFields(t *testing.T) {
	srv, now := site(t)
	supportFlow(srv)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")

	req, out := change(t, s, id, func(r *xmltree.Node) { setText(r, "status", "Closed") }, "status")
	apply(t, s, req, out)
	if out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `several transitions lead to "Closed"`) {
		t.Fatalf("ambiguous: %v", out[0].Err)
	}

	req, out = change(t, s, id, func(r *xmltree.Node) { setText(r, "status", "Resolve this issue") }, "status")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `transition "Resolve this issue" requires <resolution>; add it` {
		t.Fatalf("required: %v", out[0].Err)
	}
	if srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") != 0 {
		t.Fatal("nothing may be sent when a required field is missing")
	}

	req, out = change(t, s, id, func(r *xmltree.Node) {
		setText(r, "status", "Resolve this issue")
		setText(r, "resolution", "Done")
		setText(r, "priority", "High")
		fieldsByID(r)["customfield_10040"].Children[0].Text = "11-50"
	}, "status", "resolution", "priority", "field")
	apply(t, s, req, out)
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if out[0].Detail != "In Progress -> Closed (Resolve this issue)" {
		t.Fatal(out[0].Detail)
	}
	is := srv.Issue(id)
	if is.Status != "Closed" || is.Fields["resolution"].(map[string]any)["name"] != "Done" || is.Fields["customfield_10040"] != "11-50" ||
		is.Fields["priority"].(map[string]any)["name"] != "High" {
		t.Fatalf("%+v", is)
	}
	if srv.Count("PUT", "/rest/api/3/issue/"+id) != 1 || srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") != 1 {
		t.Fatal(srv.Requests)
	}
	r, _ := s.Fetch(bg, id)
	if got := xmltree.Print(r.Root, 0); !strings.Contains(got, "<status>Closed</status>") || !strings.Contains(got, "<resolution>Done</resolution>") {
		t.Fatalf("write-back:\n%s", got)
	}
}

func TestTransitionUnreachable(t *testing.T) {
	srv, now := site(t)
	supportFlow(srv)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "status", "Open")
		setText(r, "priority", "Low")
	}, "status", "priority")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `"Open" is not reachable from the current status in one step; reachable: "Closed"` {
		t.Fatalf("%v", out[0].Err)
	}
	if out[1].Err != nil || srv.Issue(id).Fields["priority"].(map[string]any)["name"] != "Low" {
		t.Fatalf("the edit still runs: %v", out[1].Err)
	}
}
