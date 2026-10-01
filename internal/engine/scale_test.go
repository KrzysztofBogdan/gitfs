package engine

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
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

// A clone that fails part way keeps what it stored tracked, so pull can finish it.
func TestFailedCloneKeepsIndex(t *testing.T) {
	ad := fake.New()
	for i := 1; i <= 50; i++ {
		ad.Remote.Put(fmt.Sprint(i), fmt.Sprintf("n/%04d.xml", i), `<note><title>t</title></note>`)
	}
	ad.Remote.Stubs, ad.Remote.FetchLimit = true, 20
	sess, _ := ad.Open(ctx, nil, nil)
	dir := t.TempDir() + "/wt"
	_, err := Clone(ctx, ad, sess, "fake://x", dir, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("want the fetch failure")
	}
	for _, part := range []string{"clone stopped after 20 of 50 resources", "fetch failed", `run "gfs pull" in ` + dir + " to fetch the rest"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error lacks %q:\n%v", part, err)
		}
	}
	ix, err := (&workdir.Tree{Root: dir}).LoadIndex()
	if err != nil || len(ix.All()) != 20 {
		t.Fatalf("%d indexed, %v", len(ix.All()), err)
	}
}

// A clone that fails while listing says nothing was fetched, and how to retry.
func TestCloneListFailureSaysSo(t *testing.T) {
	ad := fake.New()
	ad.Remote.FailVerb = map[string]error{"list": errors.New("HTTP 404: You cannot create issues in this project.")}
	sess, _ := ad.Open(ctx, nil, nil)
	dir := t.TempDir() + "/wt"
	_, err := Clone(ctx, ad, sess, "fake://x", dir, &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("want the list failure")
	}
	for _, part := range []string{"clone stopped while listing", "You cannot create issues", "nothing was fetched", `run "gfs pull" in ` + dir + " to retry, or delete it"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error lacks %q:\n%v", part, err)
		}
	}
	// and pull there does retry: it clones everything
	ad.Remote.Put("1", "n/1.xml", `<note><title>t</title></note>`)
	delete(ad.Remote.FailVerb, "list")
	tr, err := workdir.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := tr.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Tree: tr, Index: ix, Atts: workdir.NewAttachments(), Adapter: ad, Session: sess, Out: &bytes.Buffer{}, Now: time.Now}
	if r := pull(t, env, PullOpts{}); r.Added != 1 || !tr.Exists("n/1.xml") {
		t.Fatalf("pull after a failed clone: %+v", r)
	}
}
