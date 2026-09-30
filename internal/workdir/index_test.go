package workdir

import "testing"

func TestIndexByPathFollowsMoves(t *testing.T) {
	ix := &Index{byID: map[string]Entry{}, byPath: map[string]string{}}
	ix.Put(Entry{"1", "v1", "a/one.xml"})
	ix.Put(Entry{"2", "v1", "a/two.xml"})
	ix.Put(Entry{"1", "v2", "b/one.xml"}) // moved
	if _, ok := ix.ByPath("a/one.xml"); ok {
		t.Fatal("old path still maps")
	}
	if e, ok := ix.ByPath("b/one.xml"); !ok || e.ID != "1" || e.Version != "v2" {
		t.Fatalf("%+v %v", e, ok)
	}
	ix.Delete("2")
	if _, ok := ix.ByPath("a/two.xml"); ok {
		t.Fatal("deleted path still maps")
	}
}
