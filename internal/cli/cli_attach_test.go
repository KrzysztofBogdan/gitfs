package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAttachments(t *testing.T) {
	fk.Remote.Put("7", "att/n.xml", `<note><title>N</title></note>`)
	fk.Remote.PutAttachment("7", "70", "x.png", []byte("abc"))
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt"))
	if _, err := os.Stat("att/n.files"); !os.IsNotExist(err) {
		t.Fatal("clone must not download attachments")
	}
	if out, code := gfs(t, "get"); code != 2 {
		t.Fatalf("get without a path is a usage error: %d\n%s", code, out)
	}
	if out, code := gfs(t, "get", "att/n.xml"); code != 0 || !strings.Contains(out, "  +  att/n.files/x.png   3 B   v1") {
		t.Fatalf("%d\n%s", code, out)
	}
	os.WriteFile("att/n.files/x.png", []byte("abcd"), 0o644)
	out, _ := gfs(t, "status")
	if !strings.Contains(out, "  M  att/n.files/x.png") || !strings.Contains(out, "update attachment[id=70]") || strings.Contains(out, "  M  att/n.xml") {
		t.Fatal(out)
	}
	out, _ = gfs(t, "diff", "att")
	if !strings.Contains(out, "Binary files a/att/n.files/x.png (3 B, ba7816bf) and b/att/n.files/x.png (4 B, ") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "actions"); !strings.Contains(out, "attachments  <attachment>  create update delete") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit", "att"); code != 0 || !strings.Contains(out, "update  att/n.files/x.png   ok") {
		t.Fatalf("%d\n%s", code, out)
	}
	if out, _ := gfs(t, "log"); !strings.Contains(out, "update  att/n.files/x.png  ok") {
		t.Fatal(out)
	}
	if data, v, _ := fk.Remote.Attachment("7", "70"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
}
