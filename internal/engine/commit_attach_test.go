package engine

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

// dropAttachment removes the <attachment id=...> line from a working file.
func dropAttachment(t *testing.T, env *Env, p, id string) {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*<attachment id="` + regexp.QuoteMeta(id) + `"[^\n]*\n`)
	s := read(t, env, p)
	if !re.MatchString(s) {
		t.Fatalf("no attachment %s in %s:\n%s", id, p, s)
	}
	write(t, env, p, re.ReplaceAllString(s, ""))
}

func TestCommitAttachmentUpdate(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x.png", "abcd")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || r.Actions != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, v, _ := ad.Remote.Attachment("1", "10"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
	if !strings.Contains(out.String(), "update  a/one.files/x.png   ok") {
		t.Fatal(out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `<attachment id="10" name="x.png" size="4" version="2"`) {
		t.Fatalf("write-back must show the new version:\n%s", read(t, env, "a/one.xml"))
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "2" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
	log, _ := env.Tree.ReadLog()
	if last := log[len(log)-1]; last.Verb != "update" || last.Path != "a/one.files/x.png" || last.Outcome != "ok" {
		t.Fatalf("%+v", last)
	}
}

func TestCommitAttachmentCreate(t *testing.T) {
	env, ad, out := attached(t)
	write(t, env, "a/one.files/new.csv", "a,b")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	id, ok := ad.Remote.AttachmentNamed("1", "new.csv")
	if !ok || !strings.Contains(out.String(), "create  a/one.files/new.csv   ok  id="+id) {
		t.Fatalf("%v\n%s", ok, out)
	}
	if l, ok := env.Atts.Get("1", id); !ok || l.Path != "a/one.files/new.csv" || l.Version != "1" {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `name="new.csv"`) {
		t.Fatal("element not written back")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentDelete(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	dropAttachment(t, env, "a/one.xml", "10")
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 || r.ExitCode() != 1 {
		t.Fatalf("delete is ask; non-TTY denies: %+v\n%s", r, out)
	}
	if _, _, ok := ad.Remote.Attachment("1", "10"); !ok {
		t.Fatal("denied delete must not run")
	}
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"delete": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	// attachment 11 is now the only x.png, so its file takes over the plain name
	if _, _, ok := ad.Remote.Attachment("1", "10"); ok || read(t, env, "a/one.files/x.png") != "second" || env.Tree.Exists("a/one.files/x (2).png") {
		t.Fatal("remote attachment and its local file must be gone")
	}
	if _, ok := env.Atts.Get("1", "10"); ok {
		t.Fatal("line must be gone")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitDeleteOfUnfetchedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	dropAttachment(t, env, "b/two.xml", "20")
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"delete": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if _, _, ok := ad.Remote.Attachment("2", "20"); ok {
		t.Fatal("not deleted")
	}
}

func TestCommitNewResourceWithAttachment(t *testing.T) {
	env, ad, out := attached(t)
	write(t, env, "c/new.xml", "<note><title>N</title></note>")
	write(t, env, "c/new.files/f.txt", "f")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || r.Actions != 2 {
		t.Fatalf("%+v\n%s", r, out)
	}
	en, ok := env.Index.ByPath("c/new.xml")
	if !ok {
		t.Fatal("new resource not indexed")
	}
	if _, ok := ad.Remote.AttachmentNamed(en.ID, "f.txt"); !ok {
		t.Fatal("attachment of the new resource not uploaded")
	}
	if ls := env.Atts.ForResource(en.ID); len(ls) != 1 || ls[0].Path != "c/new.files/f.txt" {
		t.Fatalf("%+v", ls)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentConflict(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.EditAttachment("1", "10", []byte("remote"))
	write(t, env, "a/one.files/x.png", "mine")
	r := commit(t, env, CommitOpts{})
	if r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "remote" {
		t.Fatal("a conflicting upload must not run")
	}
	if read(t, env, "a/one.files/x.remote-v2.png") != "remote" || read(t, env, "a/one.files/x.png") != "mine" {
		t.Fatal("both copies must be kept")
	}
	cs := status(t, env)
	if len(cs) != 1 || len(cs[0].Attachments) != 1 || cs[0].Attachments[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentPartialFailure(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	write(t, env, "a/one.files/x.png", "abcd")
	ad.Remote.FailVerb = map[string]error{"update attachment[id=10]": errors.New("413 too large")}
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if got := read(t, env, "a/one.xml"); !strings.Contains(got, `target="attachment[id=10]"`) || !strings.Contains(got, "413 too large") {
		t.Fatalf("<errors> must name the attachment:\n%s", got)
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "1" || read(t, env, "a/one.files/x.png") != "abcd" {
		t.Fatal("a failed upload leaves the file and its line alone")
	}
}

func TestCommitMoveTakesSidecarAlong(t *testing.T) {
	env, _, out := attached(t)
	fetchAll(t, env, out)
	if err := env.Tree.Rename("a/one.xml", "a/uno.xml"); err != nil {
		t.Fatal(err)
	}
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if read(t, env, "a/uno.files/x.png") != "abc" || read(t, env, "a/uno.files/x (2).png") != "second" || env.Tree.Exists("a/one.files") {
		t.Fatal("sidecar must follow the resource")
	}
	if l, _ := env.Atts.Get("1", "10"); l.Path != "a/uno.files/x.png" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitReadOnlyAttachments(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.ReadOnlyAttachments()
	write(t, env, "a/one.files/x.png", "abcd")
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || !strings.Contains(out.String(), "read-only") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "abc" {
		t.Fatal("nothing may be uploaded")
	}
}

func TestCommitAttachmentDryRunAndFilter(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	write(t, env, "a/one.files/x.png", "abcd")
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "update  a/one.files/x.png   would run") {
		t.Fatal(out)
	}
	filter, _ := changes.PathFilter(env.Tree, []string{env.Tree.Abs("a/one.files/x.png")})
	if r := commit(t, env, CommitOpts{Filter: filter}); r.ExitCode() != 0 || r.Actions != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "abcd" || title(t, env, "1") != "One" {
		t.Fatal("only the selected attachment may be committed")
	}
	if !strings.Contains(read(t, env, "a/one.xml"), "<title>Uno</title>") {
		t.Fatal("write-back must keep the unselected local edit")
	}
}
