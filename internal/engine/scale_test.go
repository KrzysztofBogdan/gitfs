package engine

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

// A clone of 1,200 resources must not rewrite the index once per resource.
func TestCloneBatchesIndexSaves(t *testing.T) {
	ad := fake.New()
	for i := 1; i <= 1200; i++ {
		ad.Remote.Put(fmt.Sprint(i), fmt.Sprintf("n/%04d.xml", i), `<note><title>t</title></note>`)
	}
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", t.TempDir()+"/wt", &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.IndexSaves > 4 {
		t.Fatalf("index saved %d times", env.IndexSaves)
	}
	ix, err := env.Tree.LoadIndex()
	if err != nil || len(ix.All()) != 1200 || ix.Cursor != "c1" {
		t.Fatalf("index after clone: %d entries, cursor %q, %v", len(ix.All()), ix.Cursor, err)
	}
	for i := 1; i <= 1200; i++ {
		ad.Remote.Edit(fmt.Sprint(i), setTitle("u"))
	}
	env.IndexSaves = 0
	pull(t, env, PullOpts{})
	if env.IndexSaves > 4 {
		t.Fatalf("pull saved the index %d times", env.IndexSaves)
	}
	if ix, _ := env.Tree.LoadIndex(); ix == nil {
		t.Fatal("index unreadable")
	} else if e, _ := ix.ByID("7"); e.Version != "2" {
		t.Fatalf("entry 7 after pull: %+v", e)
	}
}
