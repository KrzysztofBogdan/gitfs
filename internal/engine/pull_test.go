package engine

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func pull(t *testing.T, env *Env, o PullOpts) PullReport {
	t.Helper()
	r, err := Pull(ctx, env, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setTitle(s string) func(*xmltree.Node) {
	return func(r *xmltree.Node) { r.Child("title").Children[0].Text = s }
}

func TestPullNewAndUpdated(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Put("9", "c/nine.xml", `<note><title>Nine</title></note>`)
	ad.Remote.Edit("1", setTitle("One v2"))
	r := pull(t, env, PullOpts{})
	if r.Added != 1 || r.Updated != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(read(t, env, "c/nine.xml"), "<title>Nine</title>") || !strings.Contains(read(t, env, "a/one.xml"), "One v2") {
		t.Fatal("files not written")
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status must be clean after pull")
	}
	out.Reset()
	pull(t, env, PullOpts{})
	if !strings.Contains(out.String(), "Already up to date.") {
		t.Fatal(out.String())
	}
}

func TestPullMergesIntoLocalWork(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("2", setTitle("Remote title"))
	edit(t, env, "a/b/two.xml", "line1", "LOCAL1")
	r := pull(t, env, PullOpts{})
	if r.Merged != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/b/two.xml")
	if !strings.Contains(got, "Remote title") || !strings.Contains(got, "LOCAL1") {
		t.Fatal(got)
	}
	if e, _ := env.Index.ByID("2"); e.Version != "2" {
		t.Fatal("base must be the remote version, not the merge")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != 'M' {
		t.Fatalf("local edit must still be pending: %+v", cs)
	}
}

func TestPullConflictAndForce(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), "<<<<<<< local") {
		t.Fatal("markers expected")
	}
	ad.Remote.Edit("1", setTitle("Remote again"))
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 {
		t.Fatalf("still conflicted: %+v", r)
	}
	pull(t, env, PullOpts{Force: true})
	if got := read(t, env, "a/one.xml"); strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "Remote again") {
		t.Fatal(got)
	}
}

func TestPullMovedAndDeleted(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Move("1", "z/one.xml")
	ad.Remote.Delete("2")
	r := pull(t, env, PullOpts{})
	if r.Moved != 1 || r.Deleted != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if env.Tree.Exists("a/one.xml") || !env.Tree.Exists("z/one.xml") || env.Tree.Exists("a/b/two.xml") {
		t.Fatal("moves/deletes not applied")
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status must be clean")
	}
}

func TestPullDeletedRemotelyWithLocalEdits(t *testing.T) {
	env, ad, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Keep me</title>")
	ad.Remote.Delete("1")
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	if !strings.Contains(got, `<conflict remote-version="deleted"`) || strings.Contains(got, `id="`) || !strings.Contains(got, "Keep me") {
		t.Fatal(got)
	}
	// re-create: remove the conflict element and commit
	edit(t, env, "a/one.xml", `  <conflict remote-version="deleted" elements="0" hunks="0"/>`+"\n", "")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("re-create: %+v\n%s", r, out)
	}
}

func TestPullNeverTouchesUntracked(t *testing.T) {
	env, ad, out := cloned(t)
	write(t, env, "c/nine.xml", "<note><title>mine</title></note>")
	ad.Remote.Put("9", "c/nine.xml", `<note><title>theirs</title></note>`)
	pull(t, env, PullOpts{})
	if !strings.Contains(read(t, env, "c/nine.xml"), "mine") || !strings.Contains(out.String(), "not tracked") {
		t.Fatalf("%s", out)
	}
}
