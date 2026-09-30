package jira

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// subChange is change() for sub-resource actions.
func subChange(t *testing.T, s *session, id string, edit func(root *xmltree.Node), targets ...adapter.Action) (adapter.ApplyRequest, *issueCtx) {
	t.Helper()
	req, _ := change(t, s, id, edit)
	req.Actions = targets
	ic, err := s.issueCtx(bg, req.Local)
	if err != nil {
		t.Fatal(err)
	}
	return req, ic
}

func adfEl(name, text string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	n.Children = []*xmltree.Node{textEl("paragraph", text)}
	return n
}

func add(root *xmltree.Node, n *xmltree.Node) { root.Children = append(root.Children, n) }

func TestCommentsInServiceProject(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")
	create := adapter.Action{Verb: "create", Target: "comment[1]"}

	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "hello")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != `SUP is a service project: mark the comment internal="true" or public="true"` {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "checked", "internal", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "codes attached", "public", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Fetch(bg, id)
	cs := r.Root.ChildrenNamed("comment")
	if len(cs) != 2 {
		t.Fatalf("%d comments", len(cs))
	}
	if v, _ := cs[0].Attr("internal"); v != "true" {
		t.Fatalf("first comment must be internal:\n%s", xmltree.Print(cs[0], 0))
	}
	if v, _ := cs[1].Attr("public"); v != "true" {
		t.Fatalf("second comment must be public:\n%s", xmltree.Print(cs[1], 0))
	}

	cid, _ := cs[0].Attr("id")
	upd := adapter.Action{Verb: "update", Target: "comment[id=" + cid + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		c := r.ChildrenNamed("comment")[0]
		c.DelAttr("internal")
		c.SetAttr("public", "true")
	}, upd)
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.Contains(err.Error(), "visibility of comment "+cid+" cannot be changed") {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		r.ChildrenNamed("comment")[0].Children = []*xmltree.Node{textEl("paragraph", "checked twice")}
	}, upd)
	if err := s.sub(bg, ic, req, upd); err != nil {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "comment[id=" + cid + "]"}
	req, ic = subChange(t, s, id, func(*xmltree.Node) {}, del)
	if err := s.sub(bg, ic, req, del); err != nil || len(srv.Issue(id).Comments) != 1 {
		t.Fatalf("%v %d", err, len(srv.Issue(id).Comments))
	}
}

func TestCommentsElsewhere(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "comment[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "x", "internal", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || !strings.Contains(err.Error(), "GEN is not one") {
		t.Fatalf("%v", err)
	}
	srv.AddComment(id, jtest.Comment{Author: "712020:a", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"theirs"}]}`))})
	r, _ := s.Fetch(bg, id)
	theirs, _ := r.Root.Child("comment").Attr("id")
	upd := adapter.Action{Verb: "update", Target: "comment[id=" + theirs + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		r.Child("comment").Children = []*xmltree.Node{textEl("paragraph", "mine now")}
	}, upd)
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("someone else's comment: %v", err)
	}
}

func TestWorklogs(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "worklog[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, el("worklog")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != "<worklog> needs <started> and <spent>" {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		w := el("worklog")
		w.Children = []*xmltree.Node{textEl("started", "2026-09-29T09:00:00.000+0200"), textEl("spent", "2h"),
			adfEl("comment", "pairing", "type", adfType)}
		add(r, w)
	}, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	wl := srv.Issue(id).Worklogs[0]
	if wl.Spent != "2h" || !strings.Contains(string(wl.Comment), "pairing") {
		t.Fatalf("%+v", wl)
	}
	upd := adapter.Action{Verb: "update", Target: "worklog[id=" + wl.ID + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { r.Child("worklog").Child("spent").Children[0].Text = "3h" }, upd)
	if err := s.sub(bg, ic, req, upd); err != nil || srv.Issue(id).Worklogs[0].Spent != "3h" {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "worklog[id=" + wl.ID + "]"}
	req, ic = subChange(t, s, id, func(*xmltree.Node) {}, del)
	if err := s.sub(bg, ic, req, del); err != nil || len(srv.Issue(id).Worklogs) != 0 {
		t.Fatal(err)
	}
}

func TestLinks(t *testing.T) {
	srv, now := site(t)
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	srv.AddLinkType(jtest.LinkType{ID: "2", Name: "Relates", Inward: "relates to", Outward: "relates to"})
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "link[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, textEl2("link", "GEN-2", "type", "depends on")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != `unknown link type "depends on"; use one of: "blocks", "is blocked by", "relates to", "relates to"` {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, textEl2("link", "GEN-2", "type", "is blocked by")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Fetch(bg, id)
	l := r.Root.Child("link")
	if typ, _ := l.Attr("type"); typ != "is blocked by" || textOf(l) != "GEN-2" {
		t.Fatalf("GEN-1 after linking:\n%s", xmltree.Print(r.Root, 0))
	}
	other, _ := s.Fetch(bg, srv.ID("GEN-2"))
	if typ, _ := other.Root.Child("link").Attr("type"); typ != "blocks" {
		t.Fatalf("GEN-2 must show the other direction:\n%s", xmltree.Print(other.Root, 0))
	}
	lid, _ := l.Attr("id")
	upd := adapter.Action{Verb: "update", Target: "link[id=" + lid + "]"}
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.HasPrefix(err.Error(), "links cannot be edited") {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "link[id=" + lid + "]"}
	if err := s.sub(bg, ic, req, del); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Fetch(bg, id); r.Root.Child("link") != nil {
		t.Fatal("link still there")
	}
}
