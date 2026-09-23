package workdir

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTree(t *testing.T) *Tree {
	t.Helper()
	cfg := NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestInitAndFind(t *testing.T) {
	tr := newTree(t)
	for _, p := range []string{".gfs/config", ".gfs/base", ".gfs/index", ".gfs/log"} {
		if _, err := os.Stat(filepath.Join(tr.Root, p)); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(tr.Root, "a", "b")
	os.MkdirAll(sub, 0o755)
	got, err := Find(sub)
	if err != nil || got.Root != tr.Root {
		t.Fatalf("Find = %v, %v", got, err)
	}
	if _, err := Find(t.TempDir()); err != ErrNotInTree {
		t.Fatalf("want ErrNotInTree, got %v", err)
	}
	if _, err := Init(tr.Root, NewConfig()); err == nil {
		t.Fatal("Init into non-empty dir must fail")
	}
}

func TestFilesScanRenamePrune(t *testing.T) {
	tr := newTree(t)
	must(t, tr.WriteFile("eng/Home.xml", []byte("h")))
	must(t, tr.WriteFile("eng/Home/Arch itecture.xml", []byte("a")))
	must(t, tr.WriteBase("eng/Home.xml", []byte("h")))
	files, err := tr.Scan()
	must(t, err)
	if !slices.Equal(files, []string{"eng/Home.xml", "eng/Home/Arch itecture.xml"}) {
		t.Fatal(files)
	}
	must(t, tr.Rename("eng/Home/Arch itecture.xml", "eng/Other/A.xml"))
	if tr.Exists("eng/Home") || !tr.Exists("eng/Other/A.xml") {
		t.Fatal("rename must prune empty dir and create target dir")
	}
	must(t, tr.Remove("eng/Other/A.xml"))
	if tr.Exists("eng/Other") {
		t.Fatal("remove must prune")
	}
	b, err := tr.ReadBase("eng/Home.xml")
	if err != nil || string(b) != "h" {
		t.Fatal("base")
	}
}

func TestRel(t *testing.T) {
	tr := newTree(t)
	if r, err := tr.Rel(filepath.Join(tr.Root, "eng", "x.xml")); err != nil || r != "eng/x.xml" {
		t.Fatal(r, err)
	}
	if _, err := tr.Rel(filepath.Join(tr.Root, ".gfs", "index")); err == nil {
		t.Fatal("inside .gfs must fail")
	}
	if _, err := tr.Rel(t.TempDir()); err == nil {
		t.Fatal("outside must fail")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	in := "# comment\n[policy]\ndelete = deny\n\n[remote]\nurl = confluence://h/ENG\nemail = a@b\n"
	c, err := ParseConfig([]byte(in))
	must(t, err)
	if c.Get("remote", "url") != "confluence://h/ENG" || c.Get("policy", "delete") != "deny" || c.Get("x", "y") != "" {
		t.Fatal("Get")
	}
	want := "[remote]\nemail = a@b\nurl = confluence://h/ENG\n\n[policy]\ndelete = deny\n"
	if got := string(c.Bytes()); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := ParseConfig([]byte("novalue\n")); err == nil {
		t.Fatal("want parse error")
	}
}

func TestIndexRoundTrip(t *testing.T) {
	tr := newTree(t)
	ix, err := tr.LoadIndex()
	must(t, err)
	ix.Cursor = "c1"
	ix.Put(Entry{"2", "5", "eng/b b.xml"})
	ix.Put(Entry{"1", "3", "eng/a.xml"})
	ix.Put(Entry{"1", "4", "eng/a.xml"})
	must(t, tr.SaveIndex(ix))
	got, err := tr.LoadIndex()
	must(t, err)
	if got.Cursor != "c1" || len(got.All()) != 2 {
		t.Fatalf("%+v", got.All())
	}
	if e, ok := got.ByPath("eng/b b.xml"); !ok || e.ID != "2" {
		t.Fatal("ByPath")
	}
	if e, ok := got.ByID("1"); !ok || e.Version != "4" {
		t.Fatal("ByID")
	}
	got.Delete("1")
	if _, ok := got.ByID("1"); ok {
		t.Fatal("Delete")
	}
}

func TestLog(t *testing.T) {
	tr := newTree(t)
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	must(t, tr.AppendLog(LogEntry{At: at, Verb: "update", Path: "eng/a.xml", Outcome: "ok", Detail: "v2 -> v3"}))
	must(t, tr.AppendLog(LogEntry{At: at, Verb: "move", Path: "a.xml", NewPath: "b c.xml", Outcome: "FAIL", Detail: "409"}))
	es, err := tr.ReadLog()
	must(t, err)
	if len(es) != 2 || es[1].NewPath != "b c.xml" || es[0].Detail != "v2 -> v3" {
		t.Fatalf("%+v", es)
	}
	if s := es[1].String(); s != "2026-09-23T10:00:00Z  move  a.xml -> b c.xml  FAIL  409" {
		t.Fatal(s)
	}
}

func TestLock(t *testing.T) {
	tr := newTree(t)
	unlock, err := tr.Lock()
	must(t, err)
	if _, err := tr.Lock(); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock2, err := tr.Lock()
	must(t, err)
	unlock2()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
