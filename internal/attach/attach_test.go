package attach

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var elem = &schema.Elem{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name", VersionAttr: "version"}

func TestPaths(t *testing.T) {
	if got := SidecarDir("eng/Home/Runbooks.xml"); got != "eng/Home/Runbooks.files" {
		t.Fatal(got)
	}
	if res, ok := ResourceOf("eng/Home/Runbooks.files/a b.png"); !ok || res != "eng/Home/Runbooks.xml" {
		t.Fatal(res, ok)
	}
	if _, ok := ResourceOf("eng/Home/Runbooks.xml"); ok {
		t.Fatal("a resource file is not inside a sidecar")
	}
	for in, want := range map[string]string{"a/b.png": "a-b.png", ".hidden": "_hidden", " x\ty ": "x y", "": "attachment", "..": "attachment"} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveDuplicatesAndPrefix(t *testing.T) {
	root, _ := xmltree.ParseString(`<mail><attachment id="10" name="scan.pdf"/><attachment id="3" name="scan.pdf"/><attachment id="4" name="SCAN.pdf"/><attachment id="5" name="a/b.txt"/><attachment name="new.txt"/></mail>`)
	got := Derive(root, elem, "inbox/Invoice.xml")
	want := map[string]string{
		"3":  "inbox/Invoice.files/scan.pdf",
		"4":  "inbox/Invoice.files/SCAN (2).pdf",
		"5":  "inbox/Invoice.files/a-b.txt",
		"10": "inbox/Invoice.files/scan (3).pdf",
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for id, p := range want {
		if got[id] != p {
			t.Errorf("%s: got %q want %q", id, got[id], p)
		}
	}
	pre := &schema.Elem{Name: "file", Kind: schema.Attachment, ID: "id", NameAttr: "name", PrefixAttr: "ts"}
	msg, _ := xmltree.ParseString(`<message ts="1758096060.000300"><file id="F1" name="graph.png"/></message>`)
	if p := Derive(msg, pre, "general/2026-09-17.xml")["F1"]; p != "general/2026-09-17.files/1758096060.000300-graph.png" {
		t.Fatal(p)
	}
	if len(Derive(nil, elem, "x.xml")) != 0 {
		t.Fatal("nil root")
	}
}

func TestElementsAndVersion(t *testing.T) {
	root, _ := xmltree.ParseString(`<p><attachment id="a" version="3"/><attachment id="b"/></p>`)
	els := Elements(root, elem)
	if len(els) != 2 || Version(els["a"], elem) != "3" || Version(els["b"], elem) != "-" {
		t.Fatalf("%v", els)
	}
	noVer := &schema.Elem{Name: "attachment", ID: "id"}
	if Version(els["a"], noVer) != "-" {
		t.Fatal("kind without versions")
	}
}

func TestConflictCopies(t *testing.T) {
	if got := ConflictCopy("eng/Home.files/logo.svg", "4"); got != "eng/Home.files/logo.remote-v4.svg" {
		t.Fatal(got)
	}
	if got := ConflictCopy("m.files/README", "-"); got != "m.files/README.remote" {
		t.Fatal(got)
	}
	for name, want := range map[string]string{"logo.remote-v4.svg": "logo.svg", "README.remote": "README", "a.b.remote-v12.tar": "a.b.tar"} {
		if got, ok := ConflictOriginal(name); !ok || got != want {
			t.Errorf("ConflictOriginal(%q) = %q %v, want %q", name, got, ok, want)
		}
	}
	if _, ok := ConflictOriginal("logo.svg"); ok {
		t.Fatal("not a conflict copy")
	}
}

func TestHashChangedEntry(t *testing.T) {
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("r.files/a.txt", []byte("abc"))
	line, err := Entry(tr, "1", "a1", "2", "r.files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if line.SHA != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || line.Size != 3 || line.Version != "2" || line.Path != "r.files/a.txt" {
		t.Fatalf("%+v", line)
	}
	if line.MTime != 0 {
		t.Fatal("a file written just now must get mtime 0 (racy)")
	}
	if ch, err := Changed(tr, "r.files/a.txt", line); err != nil || ch {
		t.Fatalf("same bytes: %v %v", ch, err)
	}
	tr.WriteFile("r.files/a.txt", []byte("xyz"))
	if ch, _ := Changed(tr, "r.files/a.txt", line); !ch {
		t.Fatal("same size, different bytes must be changed")
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(tr.Abs("r.files/a.txt"), old, old)
	st, _ := tr.Stat("r.files/a.txt")
	if LineMTime(st) != old.UnixNano() {
		t.Fatal("old files keep their mtime")
	}
	fast := workdir.AttEntry{SHA: "wrong", Size: 3, MTime: old.UnixNano()}
	if ch, _ := Changed(tr, "r.files/a.txt", fast); ch {
		t.Fatal("equal size and mtime must skip hashing")
	}
}
