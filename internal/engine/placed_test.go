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
