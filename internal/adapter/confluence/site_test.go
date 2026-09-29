package confluence

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// site builds ENG and OPS (global, both with a "Home"), ~jan (personal) and OLD (archived).
func site(t *testing.T) *cftest.Server {
	t.Helper()
	s := cftest.New()
	t.Cleanup(s.Close)
	s.AddSpace("ENG", "100")
	s.AddSpace("OPS", "200")
	s.PutSpace(cftest.Space{Key: "~jan", ID: "300", Type: "personal"})
	s.PutSpace(cftest.Space{Key: "OLD", ID: "400", Status: "archived"})
	s.AddPage(cftest.Page{ID: "81001", Title: "Home", SpaceID: "100", Storage: "<p>eng</p>"})
	s.AddPage(cftest.Page{ID: "81002", Title: "Architecture", ParentID: "81001", SpaceID: "100", Storage: "<p>a</p>"})
	s.AddPage(cftest.Page{ID: "82001", Title: "Home", SpaceID: "200", Storage: "<p>ops</p>"})
	s.AddPage(cftest.Page{ID: "83001", Title: "Jan", SpaceID: "300", Storage: "<p>jan</p>"})
	s.AddPage(cftest.Page{ID: "84001", Title: "Old", SpaceID: "400", Storage: "<p>old</p>"})
	return s
}

func open(t *testing.T, s *cftest.Server, sel selection) *session {
	t.Helper()
	sess, err := openSession(bg, target{base: s.URL, email: "me@x.com", token: "t", sel: sel})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func dirs(sess *session) string {
	var out []string
	for _, sp := range sess.sortedSpaces() {
		out = append(out, sp.dir)
	}
	return strings.Join(out, ",")
}

func TestOpenSelection(t *testing.T) {
	s := site(t)
	cases := []struct {
		sel  selection
		want string
	}{
		{selection{typ: "global"}, "eng,ops"},
		{selection{typ: "personal"}, "~jan"},
		{selection{typ: "all"}, "eng,ops,~jan"},
		{selection{typ: "global", exclude: []string{"OPS", "NOPE"}}, "eng"},
		{selection{keys: []string{"OLD", "~jan"}}, "old,~jan"},
	}
	for _, c := range cases {
		if got := dirs(open(t, s, c.sel)); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.sel, got, c.want)
		}
	}
	_, err := openSession(bg, target{base: s.URL, email: "me@x.com", token: "t", sel: selection{keys: []string{"ENG", "NOPE"}}})
	if err == nil || !strings.Contains(err.Error(), "space NOPE not found") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenPagesThroughSpaces(t *testing.T) {
	s := site(t)
	s.PageLimit = 1
	if got := dirs(open(t, s, selection{typ: "all"})); got != "eng,ops,~jan" {
		t.Fatal(got)
	}
}

func TestListSite(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	l, err := sess.List(bg, "")
	if err != nil || !l.Full {
		t.Fatalf("%+v %v", l, err)
	}
	var paths []string
	for _, r := range l.Resources {
		if r.Root == nil {
			t.Fatalf("empty cursor must fetch every page: %s", r.Path)
		}
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, "|") != "eng/Home.xml|eng/Home/Architecture.xml|ops/Home.xml" {
		t.Fatal(paths)
	}
}

var spacePages = regexp.MustCompile(`^GET /wiki/api/v2/spaces/(\d+)/pages$`)

func loadedSpaces(reqs []string) string {
	var out []string
	for _, r := range reqs {
		if m := spacePages.FindStringSubmatch(r); m != nil {
			out = append(out, m[1])
		}
	}
	return strings.Join(out, ",")
}

func TestFetchLoadsOnlyItsSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	s.TakeRequests()
	r, err := sess.Fetch(bg, "82001")
	if err != nil || r.Path != "ops/Home.xml" {
		t.Fatalf("%+v %v", r, err)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "200" {
		t.Fatalf("loaded spaces %q, want only 200", got)
	}
	if _, err := sess.Fetch(bg, "81002"); err != nil {
		t.Fatal(err)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "100" {
		t.Fatalf("second fetch loaded %q", got)
	}
}

func TestFetchOutsideSelection(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if _, err := sess.Fetch(bg, "84001"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("page in an unselected space: want ErrNotFound, got %v", err)
	}
}

func TestPageMovedBetweenSpaces(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if r, _ := sess.Fetch(bg, "81002"); r.Path != "eng/Home/Architecture.xml" {
		t.Fatal(r.Path)
	}
	s.EditPage("81002", func(p *cftest.Page) { p.SpaceID, p.ParentID = "200", "82001" })
	sess = open(t, s, selection{typ: "global"})
	l, err := sess.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Resources {
		if r.ID == "81002" && r.Path == "ops/Home/Architecture.xml" {
			return
		}
	}
	t.Fatalf("moved page not under ops/: %+v", l.Resources)
}

var siteIDs = ids(map[string]string{"eng/Home.xml": "81001", "eng/Home/Architecture.xml": "81002", "ops/Home.xml": "82001"})

func pageRoot(t *testing.T, title string) *xmltree.Node {
	t.Helper()
	root, err := xmltree.ParseString(`<page><title>` + title + `</title><body type="application/xhtml+xml"><p>x</p></body></page>`)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createReq(t *testing.T, path, title string) adapter.ApplyRequest {
	return adapter.ApplyRequest{Local: &adapter.Resource{Path: path, Root: pageRoot(t, title)},
		Actions: []adapter.Action{{Verb: "create"}}, IDByPath: siteIDs}
}

func TestCreateInEachSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	for path, space := range map[string]string{"ops/Home/Deploy.xml": "200", "eng/Home/Runbook.xml": "100"} {
		res := sess.Apply(bg, createReq(t, path, strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".xml")))
		if res[0].Err != nil {
			t.Fatalf("%s: %+v", path, res)
		}
		if p, _ := s.Page(res[0].ID); p.SpaceID != space {
			t.Fatalf("%s: created in space %s", path, p.SpaceID)
		}
	}
}

func TestTitleTakenPerSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if res := sess.Check(bg, createReq(t, "ops/Home/Architecture.xml", "Architecture")); res[0].Err != nil {
		t.Fatalf("title used only in ENG must be free in OPS: %+v", res)
	}
	res := sess.Check(bg, createReq(t, "eng/Home/Arch2.xml", "Architecture"))
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `"Architecture" is already used in space ENG`) {
		t.Fatalf("%+v", res)
	}
}

func TestCreateUnknownSpaceFolder(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	for _, check := range []bool{true, false} {
		req := createReq(t, "new/Page.xml", "Page")
		var res []adapter.Result
		if check {
			res = sess.Check(bg, req)
		} else {
			res = sess.Apply(bg, req)
		}
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `no space "new" in this tree; gfs does not create spaces (spaces: eng, ops)`) {
			t.Fatalf("check=%v: %+v", check, res)
		}
	}
}

func TestMoveBetweenSpacesRefused(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	base, err := sess.Fetch(bg, "81002")
	if err != nil {
		t.Fatal(err)
	}
	local := &adapter.Resource{ID: base.ID, Path: "ops/Home/Architecture.xml", Root: base.Root.Clone()}
	req := adapter.ApplyRequest{Local: local, Base: base, Lock: base.Version, IDByPath: siteIDs,
		Actions: []adapter.Action{{Verb: "move", From: base.Path, To: local.Path}}}
	for _, res := range [][]adapter.Result{sess.Check(bg, req), sess.Apply(bg, req)} {
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "moving pages between spaces is not supported") {
			t.Fatalf("%+v", res)
		}
	}
	if p, _ := s.Page("81002"); p.SpaceID != "100" || p.Version != 1 {
		t.Fatalf("page changed: %+v", p)
	}
}

func TestApplyLoadsOnlyTouchedSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	base, _ := sess.Fetch(bg, "82001")
	sess = open(t, s, selection{typ: "global"})
	s.TakeRequests()
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	res := sess.Apply(bg, adapter.ApplyRequest{Local: local, Base: base, Lock: base.Version, IDByPath: siteIDs,
		Actions: []adapter.Action{{Verb: "update", Group: "body"}}})
	if res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "200" {
		t.Fatalf("loaded %q, want only 200", got)
	}
}
