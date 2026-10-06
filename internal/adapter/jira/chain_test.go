package jira

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestChainOf(t *testing.T) {
	wrap := func(steps string) string {
		return `<issue><status>Closed</status><transitions>` + steps + `</transitions></issue>`
	}
	for x, want := range map[string]string{
		wrap(`<transition to="In Progress"/><transition to="Done"/>`): `<status> Closed does not match the last <transition to="Done">`,
		wrap(`<transition/>`): `<transition> needs to="<status>"`,
		wrap(`<transition to="In Progress"/><transition to="in progress"/><transition to="Closed"/>`): `"in progress" appears twice in <transitions>; split the round trip over two commits`,
		wrap(`<transition to="Closed"><comment>x</comment></transition>`):                             `<comment> cannot go in a <transition>; add it to the issue`,
		wrap(`<transition to="Closed"><foo/></transition>`):                                           `<transition to="Closed">: <foo> is not an issue field`,
		wrap(``): `<transitions> needs at least one <transition to="…">`,
	} {
		if _, err := chainOf(parse(t, x)); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", x, err, want)
		}
	}
	steps, err := chainOf(parse(t, wrap(`<transition to="In Progress"/><transition to="closed" name="Resolve this issue"><resolution>Done</resolution></transition>`)))
	if err != nil || len(steps) != 2 || steps[1].name != "Resolve this issue" || steps[1].node.Child("resolution") == nil {
		t.Fatalf("%+v %v", steps, err)
	}
	if s, err := chainOf(parse(t, `<issue><status>Closed</status></issue>`)); s != nil || err != nil {
		t.Fatal(s, err)
	}
	for cur, want := range map[string]int{"Open": 0, "in progress": 1, "Closed": 2} {
		if got := resumeAt(steps, cur); got != want {
			t.Errorf("resumeAt %s = %d", cur, got)
		}
	}
	root := parse(t, wrap(`<transition to="In Progress"/><transition to="Closed"/>`))
	if got := describeChain(root); got != "transition via In Progress to Closed" {
		t.Fatal(got)
	}
}

func chainSite(t *testing.T) (*jtest.Server, *session, string) {
	srv, now := site(t)
	supportFlow(srv)
	id := srv.ID("SUP-1")
	srv.Edit(id, func(is *jtest.Issue) { is.Status = "Open" })
	return srv, open(t, srv, now, selection{keys: []string{"SUP"}}, ""), id
}

// withChain sets <status>, a body <priority> and a block.
func withChain(t *testing.T, status, block string) func(*xmltree.Node) {
	return func(r *xmltree.Node) {
		setText(r, "status", status)
		setText(r, "priority", "Low")
		r.Children = append(r.Children, parse(t, "<transitions>"+block+"</transitions>"))
	}
}

func priorityOf(is *jtest.Issue) any {
	p, _ := is.Fields["priority"].(map[string]any)
	return p["name"]
}

func TestTransitionChain(t *testing.T) {
	srv, s, id := chainSite(t)
	posts := func() int { return srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") }
	steps := `<transition to="In Progress"/><transition to="Closed" name="Resolve this issue">%s</transition>`

	// a stop at step 2 keeps the reached status and holds the body edit back
	req, out := change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "")), "status", "priority", "transitions")
	apply(t, s, req, out)
	want := `stopped at "In Progress" (step 2 of 2, to "Closed"): transition "Resolve this issue" requires <resolution>; add it`
	if out[0].Err == nil || out[0].Err.Error() != want || !out[0].Partial || out[2].Err == nil || out[2].Err.Error() != want {
		t.Fatalf("%+v", out)
	}
	if out[1].Err == nil || out[1].Err.Error() != `not sent: the transitions stopped at "In Progress"` {
		t.Fatal(out[1].Err)
	}
	if is := srv.Issue(id); is.Status != "In Progress" || priorityOf(is) == "Low" || posts() != 1 {
		t.Fatalf("%+v %d", is, posts())
	}

	// the retry resumes at step 2, then sends the body edit
	req, out = change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "<resolution>Done</resolution>")), "status", "priority", "transitions")
	apply(t, s, req, out)
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if out[0].Detail != "In Progress -> Closed" || posts() != 2 {
		t.Fatalf("%q %d", out[0].Detail, posts())
	}
	is := srv.Issue(id)
	if is.Status != "Closed" || is.Fields["resolution"].(map[string]any)["name"] != "Done" || priorityOf(is) != "Low" {
		t.Fatalf("%+v", is)
	}
	lastPost, lastPut := -1, -1
	for i, r := range srv.Requests {
		switch r {
		case "POST /rest/api/3/issue/" + id + "/transitions":
			lastPost = i
		case "PUT /rest/api/3/issue/" + id:
			lastPut = i
		}
	}
	if lastPut < lastPost {
		t.Fatal("the body edit goes after the transitions", srv.Requests)
	}

	// the whole chain from Open
	srv.Edit(id, func(is *jtest.Issue) { is.Status = "Open" })
	req, out = change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "<resolution>Done</resolution>")), "status", "priority", "transitions")
	apply(t, s, req, out)
	if out[0].Err != nil || out[0].Detail != "Open -> In Progress -> Closed" {
		t.Fatalf("%+v", out[0])
	}
}

func TestTransitionChainRefusals(t *testing.T) {
	srv, s, id := chainSite(t)
	posts := func() int { return srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") }
	cases := []struct{ status, block, want string }{
		{"In Progress", `<transition to="In Progress"><priority>High</priority></transition>`,
			`stopped at "Open" (step 1 of 1, to "In Progress"): <priority> is not on the screen of transition "Start"`},
		{"Closed", `<transition to="Closed" name="Start"/>`,
			`stopped at "Open" (step 1 of 1, to "Closed"): transition "Start" leads to "In Progress", not "Closed"`},
		{"In Progress", `<transition to="In Progress"/><transition to="Closed"/>`,
			`<status> In Progress does not match the last <transition to="Closed">`},
	}
	for _, c := range cases {
		req, out := change(t, s, id, withChain(t, c.status, c.block), "status", "transitions")
		apply(t, s, req, out)
		if out[0].Err == nil || out[0].Err.Error() != c.want || out[0].Partial {
			t.Errorf("%s: %+v", c.block, out[0])
		}
	}
	if posts() != 0 {
		t.Fatal("nothing may move")
	}
	req, out := change(t, s, id, func(r *xmltree.Node) {
		r.Children = append(r.Children, parse(t, `<transitions><transition to="Open"/></transitions>`))
	}, "transitions")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != "<transitions> needs a <status> change" {
		t.Fatal(out[0].Err)
	}
}

func TestTransitionChainDryRun(t *testing.T) {
	_, s, id := chainSite(t)
	req, _ := change(t, s, id, withChain(t, "Closed", `<transition to="In Progress"/><transition to="Closed"><resolution>Done</resolution></transition>`), "status", "transitions")
	out := s.Check(bg, req)
	if out[0].Err != nil || out[0].Detail != "transition Open -> In Progress -> Closed (step 1 checked; the rest when run)" {
		t.Fatalf("%+v", out[0])
	}
	req, _ = change(t, s, id, withChain(t, "Closed", `<transition to="In Progress"><assignee>nobody@x.com</assignee></transition><transition to="Closed"/>`), "status", "transitions")
	if out := s.Check(bg, req); out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `<transition to="In Progress">: `) {
		t.Fatalf("%+v", out[0])
	}
}
