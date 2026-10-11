package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTailNeedsATailer(t *testing.T) {
	freshFake(t)
	fk.Remote.Put("tail1", "t/run.xml", `<note><title>Run</title></note>`)
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt"))
	if out, code := gfs(t, "tail", "t/run.xml"); code != 2 || !strings.Contains(out, "fake cannot tail") {
		t.Fatalf("%d %s", code, out)
	}
	if out, code := gfs(t, "tail", "a/missing.xml"); code != 2 || !strings.Contains(out, "is not tracked") {
		t.Fatalf("%d %s", code, out)
	}
}
