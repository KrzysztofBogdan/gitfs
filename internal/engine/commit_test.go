package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func commit(t *testing.T, env *Env, o CommitOpts) Report {
	t.Helper()
	r, err := Commit(ctx, env, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func title(t *testing.T, env *Env, id string) string {
	t.Helper()
	res, err := env.Session.Fetch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return res.Root.Child("title").TextContent()
}

func TestCommitFastPath(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || r.Actions != 1 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(out.String(), "update  a/one.xml   ok") {
		t.Fatalf("report:\n%s", out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `version="2"`) || len(status(t, env)) != 0 {
		t.Fatal("write-back must bump version and leave a clean status")
	}
	log, _ := env.Tree.ReadLog()
	if len(log) != 1 || log[0].Verb != "update" || log[0].Outcome != "ok" {
		t.Fatalf("log: %+v", log)
	}
}

func TestCommitCreateLenient(t *testing.T) {
	env, _, out := cloned(t)
	write(t, env, "a/new.xml", "<note><title>N</title><comment>first</comment></note>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/new.xml")
	if !strings.HasPrefix(got, "<?xml") || !strings.Contains(got, `<note id="`) || !strings.Contains(got, `<comment id="c`) {
		t.Fatalf("not written back canonical with ids:\n%s", got)
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status not clean after create")
	}
}

func TestCommitMerged(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("2", func(r *xmltree.Node) {
		b := r.Child("body")
		b.Children[0].Text = strings.Replace(b.Children[0].Text, "line5", "LINE5", 1)
	})
	edit(t, env, "a/b/two.xml", "<title>Two</title>", "<title>Dos</title>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || !strings.Contains(out.String(), "merged") {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/b/two.xml")
	if !strings.Contains(got, "<title>Dos</title>") || !strings.Contains(got, "LINE5") || len(status(t, env)) != 0 {
		t.Fatalf("merge not reflected:\n%s", got)
	}
}

func TestCommitConflict(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	r := commit(t, env, CommitOpts{})
	if r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	for _, want := range []string{`<conflict remote-version="2" by="bob"`, "<<<<<<< local", "<title>Local</title>", "||||||| base", "<title>One</title>", ">>>>>>> remote v2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if e, _ := env.Index.ByID("1"); e.Version != "2" {
		t.Fatal("base must move to the remote version")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
	if title(t, env, "1") != "Remote" {
		t.Fatal("remote must be untouched")
	}
}

func TestCommitNoMerge(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("2", func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	edit(t, env, "a/b/two.xml", "line1", "LINE1")
	r := commit(t, env, CommitOpts{NoMerge: true})
	got := read(t, env, "a/b/two.xml")
	if r.Conflicts != 1 || strings.Contains(got, "<<<<<<<") || !strings.Contains(got, `<conflict remote-version="2"`) {
		t.Fatalf("%+v\n%s", r, got)
	}
	if e, _ := env.Index.ByID("2"); e.Version != "1" {
		t.Fatal("--no-merge must not move base")
	}
}

func TestCommitLockRetry(t *testing.T) {
	for _, c := range []struct {
		failures int
		exit     int
	}{{1, 0}, {2, 1}} {
		env, ad, out := cloned(t)
		ad.Remote.LockFailures = c.failures
		edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
		if r := commit(t, env, CommitOpts{}); r.ExitCode() != c.exit {
			t.Fatalf("failures=%d: %+v\n%s", c.failures, r, out)
		}
	}
}

func TestCommitPartialFailure(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.FailVerb = map[string]error{"create comment[1]": errors.New("comment service down")}
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	edit(t, env, "a/one.xml", "</note>", "<comment>retry me</comment></note>")
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || r.Actions != 2 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	if !strings.Contains(got, `target="comment[1]"`) || !strings.Contains(got, "<comment>retry me</comment>") || !strings.Contains(got, "<title>Uno</title>") {
		t.Fatalf("partial write-back wrong:\n%s", got)
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != '!' {
		t.Fatalf("%+v", cs)
	}
	ad.Remote.FailVerb = nil
	out.Reset()
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("retry: %+v\n%s", r, out)
	}
	if got := read(t, env, "a/one.xml"); strings.Contains(got, "<errors>") || !strings.Contains(got, "retry me</comment>") {
		t.Fatalf("retry write-back:\n%s", got)
	}
}

func TestCommitAllFailed(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.FailVerb = map[string]error{"update": errors.New("500 boom")}
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	r := commit(t, env, CommitOpts{})
	got := read(t, env, "a/one.xml")
	if r.Failed != 1 || r.ExitCode() != 1 || !strings.Contains(got, "<msg>500 boom</msg>") || !strings.Contains(got, "<title>Uno</title>") {
		t.Fatalf("%+v\n%s", r, got)
	}
	if e, _ := env.Index.ByID("1"); e.Version != "1" {
		t.Fatal("base must stay")
	}
}

func TestCommitDeletePolicy(t *testing.T) {
	env, ad, out := cloned(t)
	env.Tree.Remove("a/one.xml")
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 || r.ExitCode() != 1 {
		t.Fatalf("non-TTY ask must deny: %+v\n%s", r, out)
	}
	if _, ok := ad.Remote.Get("1"); !ok {
		t.Fatal("denied delete must not run")
	}
	env.Prompt = func(q string) bool { return strings.HasPrefix(q, "delete a/one.xml") }
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("TTY yes: %+v\n%s", r, out)
	}
	if _, ok := ad.Remote.Get("1"); ok {
		t.Fatal("delete did not run")
	}
	if _, ok := env.Index.ByID("1"); ok || len(status(t, env)) != 0 {
		t.Fatal("delete must forget the resource")
	}
}

func TestCommitExplicitVerb(t *testing.T) {
	env, ad, out := cloned(t)
	edit(t, env, "a/one.xml", "<gfs>", `<gfs action="publish" channel="#eng">`)
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 {
		t.Fatalf("publish is ask: %+v", r)
	}
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"publish": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if len(ad.Remote.Published) != 1 || ad.Remote.Published[0] != "a/one.xml #eng" {
		t.Fatalf("%v", ad.Remote.Published)
	}
	if strings.Contains(read(t, env, "a/one.xml"), "action=") {
		t.Fatal("envelope must be bare after success")
	}
}

func TestCommitDryRun(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	env.Tree.Remove("a/b/two.xml")
	r := commit(t, env, CommitOpts{DryRun: true})
	if title(t, env, "1") != "One" || r.Actions != 1 || r.Denied != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(out.String(), "would run") || !strings.Contains(out.String(), "[ask]") {
		t.Fatalf("dry-run output:\n%s", out)
	}
}

func TestCommitInvalid(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", `version="1"`, `version="7"`)
	if r := commit(t, env, CommitOpts{}); r.Failed != 1 || !strings.Contains(out.String(), "read-only") {
		t.Fatalf("%+v\n%s", r, out)
	}
}

func TestCommitDeletesChildrenBeforeParent(t *testing.T) {
	// Confluence re-parents the children of a deleted page, so a parent deleted
	// first would leave its children changed on the remote and undeletable.
	env, ad, out := cloned(t)
	ad.Remote.Put("3", "a/one/child.xml", `<note><title>Child</title></note>`)
	ad.Remote.Put("4", "a/one/child/grandchild.xml", `<note><title>Grandchild</title></note>`)
	pull(t, env, PullOpts{})
	for _, p := range []string{"a/one.xml", "a/one/child.xml", "a/one/child/grandchild.xml"} {
		env.Tree.Remove(p)
	}
	out.Reset()
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"delete": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := out.String()
	grandchild := strings.Index(got, "a/one/child/grandchild.xml")
	child := strings.Index(got, "a/one/child.xml")
	parent := strings.Index(got, "a/one.xml")
	if grandchild < 0 || grandchild > child || child > parent {
		t.Fatalf("deletes must run deepest first:\n%s", got)
	}
}

// A dry run shows what Check resolved an action to, when Check says.
func TestDryRunShowsCheckDetail(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.CheckDetail = map[string]string{"update title": "rename to One v2 (resolved remotely)"}
	t.Cleanup(func() { ad.Remote.CheckDetail = nil })
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>One v2</title>")
	out.Reset()
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "would run  rename to One v2 (resolved remotely)") {
		t.Fatal(out.String())
	}
}
