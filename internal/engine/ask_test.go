package engine

import (
	"errors"
	"strings"
	"testing"
)

// An ask says what the action does (with the remote's view for deletes)
// and prints the adapter's warning first; a dry run shows both.
func TestAskShowsDetailAndWarning(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Warn = map[string]string{"delete comment[id=c1]": "comments cannot be restored"}
	t.Cleanup(func() { ad.Remote.Warn = nil })
	edit(t, env, "a/one.xml", `<comment id="c1" created="2026-01-01">hi</comment>`, "")
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "would run  delete comment[id=c1] (hi)  [ask]\n    ⚠ comments cannot be restored\n") {
		t.Fatal(out.String())
	}
	out.Reset()
	var asked []string
	env.Prompt = func(q string) bool {
		asked = append(asked, out.String()+"|"+q)
		return true
	}
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "⚠ comments cannot be restored\n|delete a/one.xml  delete comment[id=c1] (hi) ?") {
		t.Fatalf("%q", asked)
	}
}

// Apply may read a secret (a DynHost login's password) through the request.
func TestApplySecret(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.NeedSecret = true
	t.Cleanup(func() { ad.Remote.NeedSecret = false; ad.Remote.Secrets = nil })
	edit(t, env, "a/one.xml", "</note>", "<comment>new</comment></note>")
	if r := commit(t, env, CommitOpts{}); r.Failed != 1 || !strings.Contains(out.String(), "needs a password") {
		t.Fatalf("no terminal: %+v\n%s", r, out)
	}
	env.ReadSecret = func(prompt string) (string, error) { return "pw", nil }
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || len(ad.Remote.Secrets) != 1 || ad.Remote.Secrets[0] != "pw" {
		t.Fatalf("%+v %v\n%s", r, ad.Remote.Secrets, out)
	}
}

// A failed update whose element is gone on the remote (a delete-and-create
// whose create failed) stays in the file as a new element, without its id
// and read-only attributes, so the next commit creates it.
func TestFailedUpdateOfVanishedElementKept(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.FailVerb = map[string]error{"update comment[id=c1]": errors.New("deleted, create failed: 500")}
	ad.Remote.VanishOnFail = true
	t.Cleanup(func() { ad.Remote.FailVerb = nil; ad.Remote.VanishOnFail = false })
	edit(t, env, "a/one.xml", ">hi</comment>", ">hello</comment>")
	commit(t, env, CommitOpts{})
	got := read(t, env, "a/one.xml")
	if !strings.Contains(got, "<comment>hello</comment>") {
		t.Fatalf("%s\n%s", got, out)
	}
	ad.Remote.FailVerb = nil
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || !strings.Contains(read(t, env, "a/one.xml"), ">hello</comment>") {
		t.Fatalf("retry: %+v\n%s", r, out)
	}
}
