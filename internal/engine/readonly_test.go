package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

func frozen(t *testing.T) (*Env, *fake.Frozen, *bytes.Buffer) {
	t.Helper()
	ad := fake.NewFrozen()
	ad.Remote.WithFiles = true
	ad.Remote.Partial = true // like KSeF: a listing never reports deletions
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, ad, &out
}

func TestReadOnlyRefusesEveryChange(t *testing.T) {
	env, ad, _ := frozen(t)
	edit(t, env, "_m/a/one.xml", "<title>One</title>", "<title>Changed</title>")
	write(t, env, "_m/a/new.xml", `<note><title>New</title></note>`)
	cs := status(t, env)
	if len(cs) != 2 {
		t.Fatalf("%+v", cs)
	}
	for _, c := range cs {
		if c.Err == nil || !strings.Contains(c.Err.Error(), "notes are read-only") {
			t.Fatalf("%c %s: %v", c.Status, c.Path, c.Err)
		}
	}
	r, err := Commit(ctx, env, CommitOpts{Force: true})
	if err != nil || r.Failed != 2 || len(ad.Remote.Calls) != 0 {
		t.Fatalf("%+v %v %v", r, err, ad.Remote.Calls)
	}
}

func TestReadOnlyDeleteNeverForgets(t *testing.T) {
	env, ad, _ := frozen(t)
	if err := env.Tree.Remove("_m/a/one.xml"); err != nil {
		t.Fatal(err)
	}
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Status != 'D' || cs[0].Err == nil {
		t.Fatalf("%+v", cs)
	}
	if r, _ := Commit(ctx, env, CommitOpts{Force: true}); r.Failed != 1 || len(ad.Remote.Calls) != 0 {
		t.Fatalf("%+v", r)
	}
	if _, ok := env.Index.ByID("1"); !ok || read(t, env, "a/one.x.png") != "abc" {
		t.Fatal("a refused delete keeps the index entry and the attachment")
	}
}

func TestReadOnlyPullForceRestores(t *testing.T) {
	env, _, out := frozen(t)
	before := read(t, env, "_m/a/one.xml")
	edit(t, env, "_m/a/one.xml", "<title>One</title>", "<title>Changed</title>")
	pull(t, env, PullOpts{Force: true})
	if read(t, env, "_m/a/one.xml") != before || !strings.Contains(out.String(), "restored") {
		t.Fatalf("edited file restored from the base:\n%s", out)
	}
	env.Tree.Remove("_m/a/one.xml")
	pull(t, env, PullOpts{Force: true})
	if read(t, env, "_m/a/one.xml") != before {
		t.Fatal("deleted file restored")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}
