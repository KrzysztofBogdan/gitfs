package engine

import (
	"strings"
	"testing"
)

func TestResolveOursThenCommit(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, true); err != nil {
		t.Fatal(err)
	}
	got := read(t, env, "a/one.xml")
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, "<conflict") || !strings.Contains(got, "<title>Local</title>") {
		t.Fatal(got)
	}
	out.Reset()
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || title(t, env, "1") != "Local" {
		t.Fatalf("%+v\n%s", r, out)
	}
}

func TestResolveTheirs(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if len(status(t, env)) != 0 {
		t.Fatalf("theirs must leave a clean tree: %+v", status(t, env))
	}
}

func TestResolveNoMergeTheirs(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{NoMerge: true})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, env, "a/one.xml"); !strings.Contains(got, "<title>Remote</title>") || len(status(t, env)) != 0 {
		t.Fatal(got)
	}
}

func TestResolveNotConflicted(t *testing.T) {
	env, _, _ := cloned(t)
	if err := Resolve(ctx, env, []string{"a/one.xml"}, true); err == nil || !strings.Contains(err.Error(), "not in conflict") {
		t.Fatalf("got %v", err)
	}
}
