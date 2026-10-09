package attach

import (
	"path"
	"strings"
	"testing"
)

// kPlacer keeps resources under _m/ and the attachment of _m/r/a.xml at r/a.bin.
type kPlacer struct{}

func (kPlacer) ResourceDir() string { return "_m" }
func (kPlacer) AttachmentPath(res, name string) string {
	return path.Dir(strings.TrimPrefix(res, "_m/")) + "/" + name
}
func (kPlacer) ResourceOf(p string) (string, bool) {
	if strings.HasPrefix(p, "_m/") || !strings.HasSuffix(p, ".bin") {
		return "", false
	}
	return "_m/" + strings.TrimSuffix(p, ".bin") + ".xml", true
}

func TestPlacementDefault(t *testing.T) {
	pl := PlacementOf(nil)
	if pl.Placed() || !pl.IsResource("a/b.xml") || pl.Dir("a/b.xml") != "a/b.files" || pl.Path("a/b.xml", "x.png") != "a/b.files/x.png" {
		t.Fatal(pl)
	}
	if r, ok := pl.ResourceOf("a/b.files/x.png"); !ok || r != "a/b.xml" {
		t.Fatal(r, ok)
	}
	if !pl.Owns("a/b.xml", "a/b.files/x.png") || pl.Owns("a/b.xml", "a/c.files/x.png") {
		t.Fatal("owns")
	}
}

func TestPlacementPlaced(t *testing.T) {
	pl := PlacementOf(kPlacer{})
	if !pl.Placed() || pl.IsResource("r/a.bin") || !pl.IsResource("_m/r/a.xml") {
		t.Fatal("resource dir")
	}
	if pl.Path("_m/r/a.xml", "a.bin") != "r/a.bin" || pl.Dir("_m/r/a.xml") != "r" {
		t.Fatal(pl.Path("_m/r/a.xml", "a.bin"))
	}
	if !pl.Owns("_m/r/a.xml", "r/a.bin") || pl.Owns("_m/r/a.xml", "r/b.bin") {
		t.Fatal("owns")
	}
	if r, ok := pl.ResourceOf("r/a.bin"); !ok || r != "_m/r/a.xml" {
		t.Fatal(r)
	}
}
