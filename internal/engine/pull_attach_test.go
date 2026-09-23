package engine

import (
	"strings"
	"testing"
)

func TestPullRefreshesFetchedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	before := ad.Remote.Downloads
	ad.Remote.EditAttachment("1", "10", []byte("new!"))
	r := pull(t, env, PullOpts{})
	if r.ExitCode() != 0 || !strings.Contains(out.String(), "  ~  a/one.files/x.png") || read(t, env, "a/one.files/x.png") != "new!" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if ad.Remote.Downloads != before+1 {
		t.Fatalf("exactly one download expected, got %d", ad.Remote.Downloads-before)
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "2" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullNeverDownloadsUnfetched(t *testing.T) {
	env, ad, _ := attached(t)
	ad.Remote.EditAttachment("2", "20", []byte("changed"))
	pull(t, env, PullOpts{})
	if ad.Remote.Downloads != 0 || env.Tree.Exists("b/two.files") {
		t.Fatal("pull must not download attachments that were never fetched")
	}
	if !strings.Contains(read(t, env, "b/two.xml"), `size="7" version="2"`) {
		t.Fatal("the element must still be refreshed")
	}
}

func TestPullAttachmentConflictAndForce(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.EditAttachment("1", "10", []byte("rem"))
	write(t, env, "a/one.files/x.png", "mine")
	r := pull(t, env, PullOpts{})
	if r.Conflicts != 1 || !strings.Contains(out.String(), "  C  a/one.files/x.png   changed locally and on remote v2; remote copy: x.remote-v2.png") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if read(t, env, "a/one.files/x.png") != "mine" || read(t, env, "a/one.files/x.remote-v2.png") != "rem" {
		t.Fatal("both copies must be kept")
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "1" {
		t.Fatal("the line stays at the old version until resolved")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Attachments[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
	out.Reset()
	pull(t, env, PullOpts{Force: true})
	if read(t, env, "a/one.files/x.png") != "rem" || env.Tree.Exists("a/one.files/x.remote-v2.png") {
		t.Fatalf("--force takes the remote and drops the copy\n%s", out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullAttachmentDeletedOnRemote(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x (2).png", "edited")
	ad.Remote.DeleteAttachment("1", "10")
	ad.Remote.DeleteAttachment("1", "11")
	r := pull(t, env, PullOpts{})
	if env.Tree.Exists("a/one.files/x.png") || !strings.Contains(out.String(), "  -  a/one.files/x.png   (deleted on remote)") {
		t.Fatalf("unchanged file must go\n%s", out)
	}
	if read(t, env, "a/one.files/x (2).png") != "edited" || r.Conflicts != 1 {
		t.Fatalf("changed file must stay as a conflict: %+v\n%s", r, out)
	}
}

func TestPullAttachmentRenamedOnRemote(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.RenameAttachment("1", "10", "y.png")
	pull(t, env, PullOpts{})
	if read(t, env, "a/one.files/y.png") != "abc" || read(t, env, "a/one.files/x.png") != "second" || env.Tree.Exists("a/one.files/x (2).png") {
		t.Fatalf("files must follow the derived names\n%s", out)
	}
	if !strings.Contains(out.String(), "  ~  a/one.files/x.png -> a/one.files/y.png") {
		t.Fatal(out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullResourceMovedTakesSidecar(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.Move("1", "c/one.xml")
	pull(t, env, PullOpts{})
	if read(t, env, "c/one.files/x.png") != "abc" || env.Tree.Exists("a/one.files") {
		t.Fatalf("sidecar must move with the resource\n%s", out)
	}
}

func TestPullResourceDeletedKeepsChangedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x (2).png", "edited")
	ad.Remote.Delete("1")
	r := pull(t, env, PullOpts{})
	if env.Tree.Exists("a/one.xml") || env.Tree.Exists("a/one.files/x.png") || read(t, env, "a/one.files/x (2).png") != "edited" || r.Conflicts != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	cs := status(t, env)
	if len(cs) != 1 || !cs[0].Quiet || cs[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
}

func TestResolveAttachment(t *testing.T) {
	conflicted := func(t *testing.T) *Env {
		env, ad, out := attached(t)
		fetchAll(t, env, out)
		ad.Remote.EditAttachment("1", "10", []byte("rem"))
		write(t, env, "a/one.files/x.png", "mine")
		pull(t, env, PullOpts{})
		return env
	}
	env := conflicted(t)
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, false); err != nil {
		t.Fatal(err)
	}
	if read(t, env, "a/one.files/x.png") != "rem" || env.Tree.Exists("a/one.files/x.remote-v2.png") || len(status(t, env)) != 0 {
		t.Fatal("--theirs takes the remote copy")
	}
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, true); err == nil {
		t.Fatal("a clean attachment is not in conflict")
	}

	env = conflicted(t)
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, true); err != nil {
		t.Fatal(err)
	}
	cs := status(t, env)
	if read(t, env, "a/one.files/x.png") != "mine" || env.Tree.Exists("a/one.files/x.remote-v2.png") ||
		len(cs) != 1 || cs[0].Attachments[0].Status != 'M' {
		t.Fatalf("--ours keeps the local file as a change: %+v", cs)
	}
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v", r)
	}
}
