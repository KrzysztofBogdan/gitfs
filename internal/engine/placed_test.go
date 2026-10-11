package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

func placed(t *testing.T) (*Env, *fake.Placed, *bytes.Buffer) {
	t.Helper()
	ad := fake.NewPlaced()
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	ad.Remote.Put("2", "_m/a/two.xml", `<note><title>Two</title></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, ad, &out
}

func TestPlacedGetAndStatus(t *testing.T) {
	env, _, out := placed(t)
	fetchAll(t, env, out)
	if read(t, env, "a/one.x.png") != "abc" || env.Tree.Exists("_m/a/one.files") {
		t.Fatal("the attachment lands beside, not in a .files folder")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("clean tree: %+v", cs)
	}
	write(t, env, "a/one.x.png", "changed")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Path != "_m/a/one.xml" || len(cs[0].Attachments) != 1 || cs[0].Attachments[0].Status != 'M' {
		t.Fatalf("%+v", cs)
	}
}

func TestPlacedGetFolder(t *testing.T) {
	env, _, _ := placed(t)
	if r, err := Get(ctx, env, []string{"a"}); err != nil || r.ExitCode() != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if read(t, env, "a/one.x.png") != "abc" {
		t.Fatal("a folder of placed attachments selects them")
	}
}

func TestPlacedStray(t *testing.T) {
	env, _, _ := placed(t)
	write(t, env, "a/scan.pdf", "x")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Status != '!' || cs[0].Path != "a/scan.pdf" || cs[0].Err == nil || !strings.Contains(cs[0].Err.Error(), "not part of the mirror") {
		t.Fatalf("%+v", cs)
	}
}

func TestPlacedNewFileOfResource(t *testing.T) {
	env, _, _ := placed(t)
	write(t, env, "a/two.y.txt", "new")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Path != "_m/a/two.xml" || len(cs[0].Attachments) != 1 || cs[0].Attachments[0].Status != 'A' {
		t.Fatalf("a file the placer maps to a resource is that resource's new attachment: %+v", cs)
	}
}

func TestPlacedOrphanIsNotStray(t *testing.T) {
	env, _, out := placed(t)
	fetchAll(t, env, out)
	if err := env.Tree.Remove("_m/a/one.xml"); err != nil {
		t.Fatal(err)
	}
	for _, c := range status(t, env) {
		if c.Path == "a/one.x.png" {
			t.Fatalf("a tracked attachment of a deleted resource is not a stray: %+v", c)
		}
	}
}

func TestListingDeliversFiles(t *testing.T) {
	ad := fake.NewPlaced()
	ad.Remote.WithFiles = true
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, env, "a/one.x.png") != "abc" || ad.Remote.Downloads != 0 {
		t.Fatal("bytes from the listing are written without a download")
	}
	if l, ok := env.Atts.Get("1", "10"); !ok || l.Path != "a/one.x.png" || l.Version != "1" {
		t.Fatalf("tracked as fetched: %+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
	ad.Remote.Put("2", "_m/a/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.PutAttachment("2", "20", "y.png", []byte("def"))
	pull(t, env, PullOpts{})
	if read(t, env, "a/two.y.png") != "def" {
		t.Fatal("pull writes listed bytes of a new resource")
	}
	fresh, _ := env.Tree.LoadAttachments()
	if _, ok := fresh.Get("2", "20"); !ok {
		t.Fatal(".gfs/attachments saved at the end of the batch")
	}
}

func TestListedFilesNeverOverwriteLocalEdits(t *testing.T) {
	ad := fake.NewPlaced()
	ad.Remote.WithFiles = true
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, env, "a/one.x.png", "mine")
	res, _ := ad.Remote.Get("1")
	if err := env.Store(res, ""); err != nil {
		t.Fatal(err)
	}
	if read(t, env, "a/one.x.png") != "mine" {
		t.Fatal("a locally changed attachment is kept")
	}
}

func TestPullErrorKeepsTracking(t *testing.T) {
	ad := fake.NewPlaced()
	ad.Remote.WithFiles = true
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	ad.Remote.Put("2", "_m/a/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.PutAttachment("2", "20", "y.png", []byte("def"))
	ad.Remote.Put("3", "_m/b/three.xml", `<note><title>Three</title></note>`)
	write(t, env, "_m/b/three.xml", "untracked") // makes the pull stop after storing two
	env.Tree.Remove("_m/b/three.xml")
	if err := env.Tree.WriteFile("_m/b", []byte("a file where a folder must go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(ctx, env, PullOpts{}); err == nil {
		t.Fatal("the pull must fail on three")
	}
	fresh, _ := env.Tree.LoadAttachments()
	if _, ok := fresh.Get("2", "20"); !ok {
		t.Fatal("tracking lines written before the error are saved")
	}
}

// A layout change moves a resource on the remote: its placed files follow it.
func TestPlacedMovedOnRemoteTakesFiles(t *testing.T) {
	env, ad, out := placed(t)
	fetchAll(t, env, out)
	ad.Remote.Move("1", "_m/b/one.xml")
	pull(t, env, PullOpts{})
	if read(t, env, "b/one.x.png") != "abc" || env.Tree.Exists("a/one.x.png") {
		t.Fatalf("the placed file must move with its resource\n%s", out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v\n%s", cs, out)
	}
}
