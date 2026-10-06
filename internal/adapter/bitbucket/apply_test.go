package bitbucket

import (
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// prSite: repo api, PR 1 by Ana with Bo reviewing, a comment by Bo and one by me.
func prSite(t *testing.T) (*bbtest.Server, *session) {
	t.Helper()
	srv := bbtest.New()
	t.Cleanup(srv.Close)
	srv.AddMember("712020:bo", "Bo Smith", "bo")
	srv.AddMember("712020:cy", "Cy Lee", "cy")
	srv.AddMember("712020:ana", "Ana Nowak", "ana")
	srv.AddRepo("api", "feat2")
	srv.AddPR("api", bbtest.PR{Title: "Add rate limiting", Source: "feat", Reviewers: []string{"712020:bo"}, CloseSource: true,
		Strategies: []string{"merge_commit", "squash"}, DefaultStrategy: "squash"})
	srv.AddComment("api", 1, bbtest.Comment{Account: "712020:bo", Raw: "Bo says"})
	srv.AddComment("api", 1, bbtest.Comment{Account: bbtest.Me, Raw: "I say"})
	return srv, testSession(t, srv, "bitbucket://bitbucket.org/acme")
}

// change fetches id, applies f to a copy and resolves the engine's actions.
func change(t *testing.T, s *session, id string, f func(root *xmltree.Node)) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	local := base.Root.Clone()
	f(local)
	acts := changes.ResolveActions(&Adapter{}, base.Root, &envelope.Doc{Content: local}, base.Path, base.Path)
	return adapter.ApplyRequest{Local: &adapter.Resource{ID: base.ID, Path: base.Path, Root: local}, Base: base, Actions: acts}
}

func setText(root *xmltree.Node, name, v string) {
	n := root.Child(name)
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: v}}
}

func appendXML(t *testing.T, root *xmltree.Node, x string) {
	t.Helper()
	n, err := xmltree.ParseString(x)
	if err != nil {
		t.Fatal(err)
	}
	root.Children = append(root.Children, n)
}

func writes(srv *bbtest.Server) []string {
	var out []string
	for _, c := range srv.Calls() {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

func failures(rs []adapter.Result) []string {
	var out []string
	for _, r := range rs {
		if r.Err != nil {
			out = append(out, r.Action.Verb+" "+r.Action.Target+r.Action.Group+": "+r.Err.Error())
		}
	}
	return out
}

func TestUpdateFieldsOnePut(t *testing.T) {
	srv, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) {
		setText(r, "title", "Rate limiting v2")
		setText(r, "description", "new text")
		appendXML(t, r, `<reviewer>Cy Lee</reviewer>`)
	})
	srv.ResetCalls()
	if f := failures(s.Apply(bg, req)); len(f) > 0 {
		t.Fatal(f)
	}
	if w := writes(srv); len(w) != 1 || w[0] != "PUT /repositories/acme/api/pullrequests/1" {
		t.Fatal(w)
	}
	p := srv.PR("api", 1)
	if p.Title != "Rate limiting v2" || p.Description != "new text" || !slices.Equal(p.Reviewers, []string{"712020:bo", "712020:cy"}) {
		t.Fatalf("%+v", p)
	}
}

func TestComments(t *testing.T) {
	srv, s := prSite(t)
	base, _ := s.Fetch(bg, "api/pr/1")
	bo := attr(base.Root.ChildrenNamed("comment")[0], "id")
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) {
		appendXML(t, r, `<comment>top</comment>`)
		appendXML(t, r, `<comment parent="`+bo+`">reply</comment>`)
		appendXML(t, r, `<comment path="src/a.go" line="7" side="old">inline</comment>`)
		cs := r.ChildrenNamed("comment")
		cs[0].SetAttr("resolved", "true")
		cs[1].Children[0].Text = "I say, edited"
	})
	if f := failures(s.Apply(bg, req)); len(f) > 0 {
		t.Fatal(f)
	}
	p := srv.PR("api", 1)
	var got []string
	for _, c := range p.Comments {
		got = append(got, c.Raw)
	}
	if !slices.Equal(got, []string{"Bo says", "I say, edited", "top", "reply", "inline"}) || !p.Comments[0].Resolved {
		t.Fatalf("%v", got)
	}
	if p.Comments[3].Parent == 0 || p.Comments[4].Path != "src/a.go" || p.Comments[4].From != 7 || p.Comments[4].To != 0 {
		t.Fatalf("%+v %+v", p.Comments[3], p.Comments[4])
	}
	// others' comments: no edit, no delete
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { r.ChildrenNamed("comment")[0].Children[0].Text = "not mine" })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "is Bo Smith's; you can only edit your own") {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) {
		r.Children = slices.DeleteFunc(r.Children, func(c *xmltree.Node) bool { return attr(c, "id") == bo })
	})
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "you can only delete your own") {
		t.Fatal(f)
	}
}

func TestReplyToNewCommentRefused(t *testing.T) {
	_, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) { appendXML(t, r, `<comment parent="999">x</comment>`) })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "commit the parent first") {
		t.Fatal(f)
	}
}

func TestReview(t *testing.T) {
	srv, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) { setText(r, "my-review", "approved") })
	if req.Actions[0].Verb != "review" || req.Actions[0].Detail != "approve" {
		t.Fatalf("%+v", req.Actions)
	}
	srv.ResetCalls()
	if f := failures(s.Apply(bg, req)); len(f) > 0 {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { setText(r, "my-review", "changes_requested") })
	if f := failures(s.Apply(bg, req)); len(f) > 0 {
		t.Fatal(f)
	}
	want := []string{"POST /repositories/acme/api/pullrequests/1/approve", "DELETE /repositories/acme/api/pullrequests/1/approve",
		"POST /repositories/acme/api/pullrequests/1/request-changes"}
	if w := writes(srv); !slices.Equal(w, want) {
		t.Fatal(w)
	}
}

func TestMerge(t *testing.T) {
	srv, s := prSite(t)
	srv.MergeAsync = true
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) {
		setText(r, "state", "merged")
		r.Child("state").SetAttr("strategy", "squash")
	})
	if a := req.Actions[0]; a.Verb != "merge" || a.Class != "merge" || a.Detail != "merge feat → main (squash), close source branch" {
		t.Fatalf("%+v", a)
	}
	if f := failures(s.Apply(bg, req)); len(f) > 0 {
		t.Fatal(f)
	}
	if p := srv.PR("api", 1); p.State != "MERGED" || p.MergeUsed != "squash" {
		t.Fatalf("%+v", p)
	}
	if !slices.ContainsFunc(srv.Calls(), func(c string) bool { return strings.Contains(c, "/merge/task-status/") }) {
		t.Fatal("202 task polled")
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { setText(r, "state", "open") })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "a merged PR cannot be reopened") {
		t.Fatal(f)
	}
}

func TestMergeStrategyRefused(t *testing.T) {
	_, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) {
		setText(r, "state", "merged")
		r.Child("state").SetAttr("strategy", "fast_forward")
	})
	s.strategies = map[string]strategies{} // as in a dry run: read from Bitbucket
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], `"fast_forward" not allowed on main; allowed: merge_commit, squash (default)`) {
		t.Fatal(f)
	}
}

func TestMergeSkippedAfterFailure(t *testing.T) {
	srv, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) {
		setText(r, "title", "x")
		setText(r, "state", "merged")
	})
	srv.Fail("/pullrequests/1")
	rs := s.Apply(bg, req)
	srv.Fail("")
	f := failures(rs)
	if len(f) != 2 || !strings.Contains(f[1], "skipped: an earlier action failed") {
		t.Fatal(f)
	}
	for _, c := range srv.Calls() {
		if strings.HasSuffix(c, "/merge") {
			t.Fatal("merge not sent")
		}
	}
}

func TestDecline(t *testing.T) {
	srv, s := prSite(t)
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) { setText(r, "state", "declined") })
	if a := req.Actions[0]; a.Verb != "decline" || a.Class != "decline" {
		t.Fatalf("%+v", a)
	}
	if f := failures(s.Apply(bg, req)); len(f) > 0 || srv.PR("api", 1).State != "DECLINED" {
		t.Fatal(f)
	}
}

func TestCreatePR(t *testing.T) {
	srv, s := prSite(t)
	root, _ := xmltree.ParseString(`<pullrequest><title>New thing</title><source branch="feat2"/><destination branch="main"/>
		<reviewer>bo</reviewer><description>why</description><comment>first!</comment></pullrequest>`)
	req := adapter.ApplyRequest{Local: &adapter.Resource{Path: "api/pr/new.xml", Root: root},
		Actions: changes.ResolveActions(&Adapter{}, nil, &envelope.Doc{Content: root}, "", "api/pr/new.xml")}
	if req.Actions[0].Detail != "create PR feat2 → main in api" {
		t.Fatal(req.Actions[0].Detail)
	}
	rs := s.Apply(bg, req)
	if f := failures(rs); len(f) > 0 || rs[0].ID != "api/pr/2" {
		t.Fatal(f, rs[0].ID)
	}
	p := srv.PR("api", 2)
	if p.Title != "New thing" || !slices.Equal(p.Reviewers, []string{"712020:bo"}) || len(p.Comments) != 1 {
		t.Fatalf("%+v", p)
	}
	bad, _ := xmltree.ParseString(`<pullrequest><title>x</title><source branch="nope"/></pullrequest>`)
	req = adapter.ApplyRequest{Local: &adapter.Resource{Path: "api/pr/bad.xml", Root: bad}, Actions: []adapter.Action{{Verb: "create"}}}
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], `branch "nope" does not exist in api`) {
		t.Fatal(f)
	}
	req.Local.Path = "api/pipelines/new.xml"
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "read-only") {
		t.Fatal(f)
	}
	req.Local.Path = "api/new.xml"
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "new files go in <repo>/pr/") {
		t.Fatal(f)
	}
}

func TestReadOnlyRefusals(t *testing.T) {
	srv, s := prSite(t)
	uuid := srv.AddRun("api", bbtest.Run{Result: "SUCCESSFUL"})
	req := change(t, s, runID("api", uuid), func(r *xmltree.Node) { setText(r, "result", "successful!") })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "pipeline runs are read-only") {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) {
		r.Child("author").Children[0].Text = "Someone"
		appendXML(t, r, `<step id="x"/>`)
	})
	if f := failures(s.Check(bg, req)); len(f) == 0 || !strings.Contains(f[0], "<step> belongs in a pipeline file") {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { r.Child("author").Children[0].Text = "Someone" })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "<author> is the PR's author, set by Bitbucket") {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { r.Child("reviewer").Children[0].Text = "Bob" })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "replace the element with one without account") {
		t.Fatal(f)
	}
	req = change(t, s, "api/pr/1", func(r *xmltree.Node) { appendXML(t, r, `<reviewer>Nobody</reviewer>`) })
	if f := failures(s.Check(bg, req)); len(f) != 1 || !strings.Contains(f[0], "no workspace member matches") {
		t.Fatal(f)
	}
}
