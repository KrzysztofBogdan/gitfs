package engine

import (
	"strings"
	"testing"
)

func TestRefreshMovesUnchangedFile(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Refresh = map[string][]string{"move": {"2"}}
	ad.Remote.Moves = map[string]string{"2": "c/two.xml"}
	moveOne(t, env)
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || !strings.Contains(out.String(), "refresh a/b/two.xml -> c/two.xml   ok  moved with a/uno.xml") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if env.Tree.Exists("a/b/two.xml") || !env.Tree.Exists("c/two.xml") {
		t.Fatal("two.xml must move")
	}
	if e, _ := env.Index.ByID("2"); e.Path != "c/two.xml" || len(status(t, env)) != 0 {
		t.Fatalf("index %+v, status %+v", e, status(t, env))
	}
}

func TestRefreshSkipsChangedAndPending(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Refresh = map[string][]string{"move": {"2"}}
	ad.Remote.Moves = map[string]string{"2": "c/two.xml"}
	moveOne(t, env)
	edit(t, env, "a/b/two.xml", "<title>Two</title>", "<title>Dos</title>")
	commit(t, env, CommitOpts{Filter: func(p string) bool { return strings.HasPrefix(p, "a/uno") || strings.HasPrefix(p, "a/one") }})
	if !strings.Contains(out.String(), "warning: a/b/two.xml: changed on the remote by this commit; run gfs pull") ||
		!strings.Contains(read(t, env, "a/b/two.xml"), "Dos") {
		t.Fatalf("\n%s", out)
	}

	env, ad, out = cloned(t)
	ad.Remote.Refresh = map[string][]string{"move": {"2"}}
	ad.Remote.Moves = map[string]string{"2": "c/two.xml"}
	moveOne(t, env)
	edit(t, env, "a/b/two.xml", "<title>Two</title>", "<title>Dos</title>")
	r := commit(t, env, CommitOpts{}) // two.xml commits first (M before R), then follows the move
	if r.ExitCode() != 0 || !strings.Contains(read(t, env, "c/two.xml"), "Dos") || len(status(t, env)) != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
}
