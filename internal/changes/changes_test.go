package changes

import (
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const note1 = `<note id="1" version="2"><title>T</title><comment id="c1" created="2026-01-01">hi</comment></note>`

func file(t *testing.T, x string) []byte {
	t.Helper()
	d, err := envelope.Parse([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Bytes(d, fake.Schema)
}

// setup creates a tree whose base and working copy both hold note1 at a/n.xml.
func setup(t *testing.T) (*workdir.Tree, *workdir.Index) {
	t.Helper()
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("a/n.xml", file(t, note1))
	tr.WriteBase("a/n.xml", file(t, note1))
	ix, _ := tr.LoadIndex()
	ix.Put(workdir.Entry{ID: "1", Version: "2", Path: "a/n.xml"})
	return tr, ix
}

func compute(t *testing.T, tr *workdir.Tree, ix *workdir.Index) []FileChange {
	t.Helper()
	cs, err := Compute(tr, ix, fake.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func one(t *testing.T, cs []FileChange) FileChange {
	t.Helper()
	if len(cs) != 1 {
		t.Fatalf("want 1 change, got %d: %+v", len(cs), cs)
	}
	return cs[0]
}

func verbs(c FileChange) []string {
	var out []string
	for _, a := range c.Actions {
		v := a.Verb
		if a.Group != "" {
			v += ":" + a.Group
		}
		if a.Target != "" {
			v += ":" + a.Target
		}
		out = append(out, v)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnchanged(t *testing.T) {
	tr, ix := setup(t)
	// Reformatting and attribute reordering is not a change.
	tr.WriteFile("a/n.xml", []byte(`<gfs><content><note version="2" id="1">
<comment created="2026-01-01" id="c1">hi</comment><title>T</title></note></content></gfs>`))
	if cs := compute(t, tr, ix); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestStatuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tr *workdir.Tree)
		status byte
		verbs  []string
		err    bool
	}{
		{"title edit", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>New</title><comment id="c1" created="2026-01-01">hi</comment></note>`))
		}, 'M', []string{"update:title"}, false},
		{"new comment", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>T</title><comment id="c1" created="2026-01-01">hi</comment><comment>new</comment></note>`))
		}, 'M', []string{"create:comment[1]"}, false},
		{"comment removed", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>T</title></note>`))
		}, 'M', []string{"delete:comment[id=c1]"}, false},
		{"moved", func(tr *workdir.Tree) { tr.Rename("a/n.xml", "b/n.xml") }, 'R', []string{"move"}, false},
		{"deleted", func(tr *workdir.Tree) { tr.Remove("a/n.xml") }, 'D', []string{"delete"}, false},
		{"conflict", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte("<gfs>\n  <content>\n<<<<<<< local\nx\n||||||| base\n=======\ny\n>>>>>>> remote v3\n"))
		}, 'C', nil, false},
		{"failed last commit", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte(`<gfs action="publish" channel="x"><errors><error action="publish" code="500"><msg>boom</msg></error></errors><content>`+note1+`</content></gfs>`))
		}, '!', []string{"publish"}, false},
		{"explicit verb", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte(`<gfs action="publish" channel="x"><content>`+note1+`</content></gfs>`))
		}, 'M', []string{"publish"}, false},
		{"version edited", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="9"><title>T</title><comment id="c1" created="2026-01-01">hi</comment></note>`))
		}, 'M', nil, true},
		{"not xml", func(tr *workdir.Tree) { tr.WriteFile("a/n.xml", []byte("oops")) }, 'M', nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, ix := setup(t)
			c.mutate(tr)
			ch := one(t, compute(t, tr, ix))
			if ch.Status != c.status {
				t.Fatalf("status %c, want %c (err %v)", ch.Status, c.status, ch.Err)
			}
			if (ch.Err != nil) != c.err {
				t.Fatalf("err = %v", ch.Err)
			}
			if !c.err && !eq(verbs(ch), c.verbs) {
				t.Fatalf("verbs %v, want %v", verbs(ch), c.verbs)
			}
		})
	}
}

func TestNewFileBareRoot(t *testing.T) {
	tr, ix := setup(t)
	tr.WriteFile("a/new.xml", []byte("<note><title>N</title></note>"))
	ch := one(t, compute(t, tr, ix))
	if ch.Status != 'A' || !eq(verbs(ch), []string{"create"}) || !ch.Local.Wrapped || ch.Err != nil {
		t.Fatalf("%+v", ch)
	}
	if ch.Actions[0].Detail != "create note" {
		t.Fatalf("Describe not applied: %q", ch.Actions[0].Detail)
	}
}

func TestDuplicateIdentity(t *testing.T) {
	tr, ix := setup(t)
	data, _ := tr.ReadFile("a/n.xml")
	tr.WriteFile("a/copy.xml", data)
	cs := compute(t, tr, ix)
	var dup *FileChange
	for i := range cs {
		if cs[i].Err != nil {
			dup = &cs[i]
		}
	}
	if dup == nil {
		t.Fatalf("want duplicate identity error: %+v", cs)
	}
}

func TestPathFilter(t *testing.T) {
	tr, _ := setup(t)
	f, err := PathFilter(tr, []string{filepath.Join(tr.Root, "a")})
	if err != nil || !f("a/n.xml") || f("b/n.xml") || f("ab/x.xml") {
		t.Fatal("PathFilter")
	}
	if f, _ := PathFilter(tr, nil); f != nil {
		t.Fatal("no args -> nil filter")
	}
}
