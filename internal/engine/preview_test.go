package engine

import (
	"errors"
	"strings"
	"testing"
)

// moveOne renames a/one.xml to a/uno.xml: a move under the fake's Tree model.
func moveOne(t *testing.T, env *Env) {
	t.Helper()
	write(t, env, "a/uno.xml", read(t, env, "a/one.xml"))
	if err := env.Tree.Remove("a/one.xml"); err != nil {
		t.Fatal(err)
	}
}

// askMoves sets move = ask, so only a Check error can skip the prompt.
func askMoves(t *testing.T, env *Env) {
	t.Helper()
	cfg, err := env.Tree.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set("policy", "move", "ask")
	if err := env.Tree.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewReclassifiesBeforeAsk(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Preview = map[string]bool{"move": true}
	ad.Remote.CheckClass = map[string]string{"move ": "delete"}
	ad.Remote.CheckDetail = map[string]string{"move ": "move to B"}
	ad.Remote.CheckWarn = map[string]string{"move ": "drops x"}
	moveOne(t, env)
	var asked []string
	env.Prompt = func(q string) bool { asked = append(asked, q); return false }
	r := commit(t, env, CommitOpts{})
	if len(asked) != 1 || !strings.Contains(asked[0], "move to B") || r.Denied != 1 {
		t.Fatalf("asked %q, %+v\n%s", asked, r, out)
	}
	if !strings.Contains(out.String(), "⚠ drops x") || len(ad.Remote.Calls) != 0 {
		t.Fatalf("calls %v\n%s", ad.Remote.Calls, out)
	}
}

func TestPreviewCheckErrorFailsWithoutAsking(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Preview = map[string]bool{"move": true}
	ad.Remote.FailVerb = map[string]error{"move": errors.New("no such project")}
	askMoves(t, env)
	moveOne(t, env)
	env.Prompt = func(q string) bool { t.Fatalf("asked %q", q); return false }
	r := commit(t, env, CommitOpts{})
	if r.Failed == 0 || len(ad.Remote.Calls) != 0 || !strings.Contains(out.String(), "no such project") {
		t.Fatalf("%+v calls %v\n%s", r, ad.Remote.Calls, out)
	}
	if !strings.Contains(read(t, env, "a/uno.xml"), "no such project") {
		t.Fatal("the error must be written into the file")
	}
}

func TestNoCheckWithoutPreview(t *testing.T) {
	env, ad, out := cloned(t)
	moveOne(t, env)
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || ad.Remote.Checks != 0 {
		t.Fatalf("%+v checks %d\n%s", r, ad.Remote.Checks, out)
	}
}

func TestDryRunUsesCheckClass(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Preview = map[string]bool{"move": true}
	ad.Remote.CheckClass = map[string]string{"move ": "delete"}
	ad.Remote.CheckWarn = map[string]string{"move ": "drops x"}
	moveOne(t, env)
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "[ask]") || !strings.Contains(out.String(), "⚠ drops x") {
		t.Fatalf("\n%s", out)
	}
}
