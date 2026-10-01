package ovh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

// TestRelaxNG checks the grammar with xmllint against a zone as read.
func TestRelaxNG(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	rng := filepath.Join(dir, "zone.rng")
	os.WriteFile(rng, []byte(RelaxNG()), 0o644)
	srv := site(t)
	root, err := testSession(t, srv, selection{}).readZone(bg, "a.com")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"zone.xml": envelope.Bytes(envelope.New(root), zoneSchema),
		"bad.xml":  []byte(`<zone name="x"><recrod name="a" type="A">1.2.3.4</recrod></zone>`),
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, p).CombinedOutput()
		if name == "bad.xml" {
			if err == nil {
				t.Fatalf("a misspelt element must fail:\n%s", out)
			}
			continue
		}
		if err != nil || !strings.Contains(string(out), "validates") {
			t.Fatalf("%s: %v\n%s\n%s", name, err, out, data)
		}
	}
}
