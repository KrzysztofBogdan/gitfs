package jira

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

// TestRelaxNG checks the grammar with xmllint against real decoded files.
func TestRelaxNG(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	rng := filepath.Join(dir, "jira.rng")
	os.WriteFile(rng, []byte(RelaxNG()), 0o644)
	var is apiIssue
	json.Unmarshal([]byte(issueJSON), &is)
	root, err := decoder{reg: newRegistry(), jsm: true}.issue(is, map[string]fieldMeta{
		"description": {ID: "description"}, "customfield_10040": cf("customfield_10040", "Users", sys+"select")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"issue.xml":   envelope.Bytes(envelope.New(root), issueSchema),
		"request.xml": []byte(`<request id="1" key="E-1"><summary>s</summary><status category="done">Resolved</status><participant account="a">A</participant><comment id="2">hi</comment></request>`),
		"bad.xml":     []byte(`<issue><description type="application/vnd.atlassian.adf+xml"><paragrph>typo</paragrph></description></issue>`),
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, p).CombinedOutput()
		if name == "bad.xml" {
			if err == nil {
				t.Fatalf("a misspelt ADF element must fail:\n%s", out)
			}
			continue
		}
		if err != nil || !strings.Contains(string(out), "validates") {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
}
