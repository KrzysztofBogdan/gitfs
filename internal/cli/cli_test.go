package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

var fk = fake.New()

func init() {
	adapter.Register(fk) // test-only registration
	fk.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
}

func gfs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String(), exitCode(err)
}

func TestCLIFlow(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt", "a"))
	if out, _ := gfs(t, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
	data, _ := os.ReadFile("one.xml")
	os.WriteFile("one.xml", bytes.Replace(data, []byte("<title>One</title>"), []byte("<title>Uno</title>"), 1), 0o644)

	out, _ := gfs(t, "status")
	if !strings.Contains(out, "On remote fake://x") || !strings.Contains(out, "  M  a/one.xml") || !strings.Contains(out, "update title") {
		t.Fatal(out)
	}
	out, _ = gfs(t, "diff", "one.xml")
	if !strings.Contains(out, "-      <title>One</title>") || !strings.Contains(out, "+      <title>Uno</title>") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit", "--dry-run"); code != 0 || !strings.Contains(out, "would run") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit"); code != 0 || !strings.Contains(out, "update  a/one.xml   ok") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "log"); !strings.Contains(out, "update  a/one.xml  ok") {
		t.Fatal(out)
	}

	os.Remove("one.xml")
	if out, code := gfs(t, "commit"); code != 1 || !strings.Contains(out, "--allow delete") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if out, code := gfs(t, "commit", "--allow", "delete"); code != 0 {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "actions"); !strings.Contains(out, "publish  ask  channel") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "pull"); !strings.Contains(out, "Already up to date.") {
		t.Fatal(out)
	}
	if _, code := gfs(t, "resolve", "x.xml"); code != 2 {
		t.Fatal("resolve without --ours/--theirs must be a usage error")
	}
}

func TestOutsideTree(t *testing.T) {
	t.Chdir(t.TempDir())
	if out, code := gfs(t, "status"); code != 2 || !strings.Contains(out, ".gfs not found") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestCommitForce(t *testing.T) {
	fk.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`) // TestCLIFlow deletes it
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt", "a"))
	os.Remove("one.xml")
	if out, code := gfs(t, "commit", "--dry-run", "--force"); code != 0 || strings.Contains(out, "[ask]") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if out, code := gfs(t, "commit", "--force"); code != 0 || !strings.Contains(out, "delete  a/one.xml   ok") {
		t.Fatalf("code %d\n%s", code, out)
	}
}
