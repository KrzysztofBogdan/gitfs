package confluence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var bg = context.Background()

// space builds ENG with Home > {Architecture, Runbooks}.
func space(t *testing.T) (*cftest.Server, *session) {
	t.Helper()
	s := cftest.New()
	t.Cleanup(s.Close)
	s.AddSpace("ENG", "100")
	s.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Engineering space.</p>", Labels: []string{"index"}})
	s.AddPage(cftest.Page{ID: "98120", Title: "Architecture", ParentID: "98001", SpaceID: "100", Storage: "<p>a</p><p>b</p>"})
	s.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	s.AddComment(cftest.Comment{ID: "7731", PageID: "98120", Storage: "<p>Out of date.</p>"})
	sess, err := openSession(bg, target{base: s.URL, space: "ENG", email: "me@x.com", token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return s, sess
}

func byPath(l adapter.Listing) map[string]adapter.Resource {
	m := map[string]adapter.Resource{}
	for _, r := range l.Resources {
		m[r.Path] = r
	}
	return m
}

func TestListAndFetch(t *testing.T) {
	_, sess := space(t)
	l, err := sess.List(bg, "")
	if err != nil || !l.Full || len(l.Resources) != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	m := byPath(l)
	arch, ok := m["eng/Home/Architecture.xml"]
	if !ok || arch.ID != "98120" || arch.Version != "1" {
		t.Fatalf("%v", m)
	}
	got := xmltree.Print(arch.Root, 0)
	for _, want := range []string{`parent="98001"`, "<title>Architecture</title>", `<comment id="7731" author="bob"`, "<p>Out of date.</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if _, ok := m["eng/Home.xml"]; !ok {
		t.Fatal("Home.xml")
	}
	if _, err := sess.Fetch(bg, "424242"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func resource(t *testing.T, sess *session, id string) *adapter.Resource {
	t.Helper()
	r, err := sess.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ids(m map[string]string) func(string) (string, bool) {
	return func(p string) (string, bool) { id, ok := m[p]; return id, ok }
}

var known = ids(map[string]string{"eng/Home.xml": "98001", "eng/Home/Runbooks.xml": "98130", "eng/Home/Architecture.xml": "98120"})

func apply(sess *session, local, base *adapter.Resource, acts ...adapter.Action) []adapter.Result {
	req := adapter.ApplyRequest{Local: local, Base: base, Actions: acts, IDByPath: known}
	if base != nil {
		req.Lock = base.Version
	}
	return sess.Apply(bg, req)
}

func setText(n *xmltree.Node, name, text string) {
	c := n.Child(name)
	c.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

func TestUpdateTitleBodyAndLock(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	setText(local.Root, "title", "Architecture v2")
	local.Root.Child("body").Children = local.Root.Child("body").Children[:1]
	res := apply(sess, local, base, adapter.Action{Verb: "update", Group: "title"}, adapter.Action{Verb: "update", Group: "body"})
	if res[0].Err != nil || res[1].Err != nil || res[0].Detail != "v1 -> v2" {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	if p.Title != "Architecture v2" || p.Storage != "<p>a</p>" || p.Version != 2 {
		t.Fatalf("%+v", p)
	}
	stale := apply(sess, local, base, adapter.Action{Verb: "update", Group: "body"})
	if len(stale) != 1 || !errors.Is(stale[0].Err, adapter.ErrLock) {
		t.Fatalf("want ErrLock, got %+v", stale)
	}
}

func TestCreateUnderParent(t *testing.T) {
	srv, sess := space(t)
	root, _ := xmltree.ParseString(`<page><title>Rollback</title><labels><label>runbook</label></labels>` +
		`<body type="application/xhtml+xml"><ol><li>Redeploy <code>v2.3.1</code></li></ol></body><comment><p>draft</p></comment></page>`)
	res := apply(sess, &adapter.Resource{Path: "eng/Home/Runbooks/Rollback.xml", Root: root}, nil, adapter.Action{Verb: "create"})
	if len(res) != 1 || res[0].Err != nil || res[0].ID == "" {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page(res[0].ID)
	if p.ParentID != "98130" || p.Title != "Rollback" || strings.Join(p.Labels, ",") != "runbook" || !strings.Contains(p.Storage, "<code>v2.3.1</code>") {
		t.Fatalf("%+v", p)
	}
	if cs := srv.Comments(res[0].ID); len(cs) != 1 || cs[0].Storage != "<p>draft</p>" {
		t.Fatalf("%+v", cs)
	}
	got := resource(t, sess, res[0].ID)
	if got.Path != "eng/Home/Runbooks/Rollback.xml" {
		t.Fatal(got.Path)
	}
	orphan := apply(sess, &adapter.Resource{Path: "eng/Nope/X.xml", Root: root}, nil, adapter.Action{Verb: "create"})
	if orphan[0].Err == nil || !strings.Contains(orphan[0].Err.Error(), "commit it first") {
		t.Fatalf("%+v", orphan)
	}
}

func TestMoveAndRename(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: "eng/Home/Runbooks/Arch.xml", Root: base.Root.Clone()}
	res := apply(sess, local, base, adapter.Action{Verb: "move", From: base.Path, To: local.Path})
	if res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	if p.ParentID != "98130" || p.Title != "Arch" {
		t.Fatalf("%+v", p)
	}
	if got := resource(t, sess, "98120"); got.Path != "eng/Home/Runbooks/Arch.xml" {
		t.Fatal(got.Path)
	}
}

func TestLabelsCommentsDelete(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	lbl, _ := xmltree.ParseString(`<labels><label>architecture</label></labels>`)
	local.Root.Children = append(local.Root.Children, lbl)
	c := local.Root.Child("comment")
	c.Children = []*xmltree.Node{{Kind: xmltree.Element, Name: "p", Children: []*xmltree.Node{{Kind: xmltree.Text, Text: "Fixed."}}}}
	res := apply(sess, local, base,
		adapter.Action{Verb: "update", Group: "labels"},
		adapter.Action{Verb: "update", Target: "comment[id=7731]"})
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	cs := srv.Comments("98120")
	if strings.Join(p.Labels, ",") != "architecture" || cs[0].Storage != "<p>Fixed.</p>" || cs[0].Version != 2 {
		t.Fatalf("%+v %+v", p, cs[0])
	}
	base = resource(t, sess, "98120")
	res = apply(sess, base, base, adapter.Action{Verb: "delete", Target: "comment[id=7731]"}, adapter.Action{Verb: "delete"})
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	if _, ok := srv.Page("98120"); ok || len(srv.Comments("98120")) != 0 {
		t.Fatal("not deleted")
	}
}

func TestCheckDuplicateTitle(t *testing.T) {
	_, sess := space(t)
	root, _ := xmltree.ParseString(`<page><title>Runbooks</title><body type="application/xhtml+xml"/></page>`)
	res := sess.Check(bg, adapter.ApplyRequest{Local: &adapter.Resource{Path: "eng/Home/Runbooks (2).xml", Root: root},
		Actions: []adapter.Action{{Verb: "create"}}, IDByPath: known})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "already used") {
		t.Fatalf("%+v", res)
	}
}

func TestDescribe(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		act  adapter.Action
		path string
		want string
	}{
		{adapter.Action{Verb: "create"}, "eng/Home/Runbooks/Rollback.xml", `create page under "Runbooks"`},
		{adapter.Action{Verb: "create"}, "eng/Top.xml", "create top-level page"},
		{adapter.Action{Verb: "update", Group: "body"}, "eng/Home.xml", "update body"},
		{adapter.Action{Verb: "move", From: "eng/Home/A.xml", To: "eng/Home/B.xml"}, "eng/Home/B.xml", `rename to "B"`},
		{adapter.Action{Verb: "move", From: "eng/Home/A.xml", To: "eng/Home/Runbooks/A.xml"}, "", `move under "Runbooks"`},
		{adapter.Action{Verb: "delete"}, "", "delete page"},
	}
	for _, c := range cases {
		act := c.act
		root, _ := xmltree.ParseString(`<page><title>T</title></page>`)
		a.Describe(&act, &adapter.Resource{Path: c.path, Root: root})
		if act.Detail != c.want || act.Class != act.Verb {
			t.Errorf("%+v: got %q", c.act, act.Detail)
		}
	}
}
