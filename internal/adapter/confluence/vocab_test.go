package confluence

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func pageWith(t *testing.T, body string) *xmltree.Node {
	t.Helper()
	b, err := parseStorage("body", body)
	if err != nil {
		t.Fatal(err)
	}
	return &xmltree.Node{Kind: xmltree.Element, Name: "page", Children: []*xmltree.Node{b}}
}

func TestLintExamplesAndRejected(t *testing.T) {
	ex, rejected := loadDocs(t)
	v, err := BuildVocabulary(ex, rejected)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ex {
		if w := v.Lint(pageWith(t, e.Body)); len(w) > 0 {
			t.Errorf("%s: verified example warns: %v", e.Key(), w)
		}
	}
	for _, e := range rejected {
		w := v.Lint(pageWith(t, e.Body))
		if len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "Confluence drops this") {
			t.Errorf("%s: rejected form not flagged as dropped: %v", e.Key(), w)
		}
	}
}

func TestLintMessages(t *testing.T) {
	v := &Vocabulary{
		Elements: map[string][]string{"p": {"style"}, "span": {"style"}, "ac:structured-macro": {"ac:name", "ac:schema-version"}, "ac:parameter": {"ac:name"}},
		Macros:   map[string][]string{"code": {"language"}},
		ADF:      map[string][]string{},
		Dropped:  map[string]string{"span@data-highlight-colour": "use a style"},
	}
	root := pageWith(t, `<p foo="1" ac:macro-id="x"><blink/><span data-highlight-colour="#fff">a</span><span data-highlight-colour="#000">b</span></p>`+
		`<ac:structured-macro ac:name="code"><ac:parameter ac:name="theme">x</ac:parameter></ac:structured-macro><ac:structured-macro ac:name="nope"/>`)
	root.Children = append(root.Children, &xmltree.Node{Kind: xmltree.Element, Name: "comment", Children: []*xmltree.Node{{Kind: xmltree.Element, Name: "u"}}})
	got := strings.Join(v.Lint(root), "\n")
	want := strings.Join([]string{
		`<p foo>: attribute not verified`,
		`<blink>: element not verified (gfs help confluence-storage)`,
		`<span data-highlight-colour>: Confluence drops this; use a style`,
		`macro "code" parameter "theme": not verified`,
		`macro "nope": not verified`,
		`<u>: element not verified (gfs help confluence-storage)`,
	}, "\n")
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestEmbeddedVocabulary(t *testing.T) {
	if v := EmbeddedVocabulary(); len(v.Elements) == 0 || len(v.Macros) == 0 {
		t.Fatal("embedded vocabulary is empty; run TestStorageCatalogue -update")
	}
}

// TestStorageSchema checks the generated RELAX NG grammar with xmllint.
func TestStorageSchema(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	ex, rejected := loadDocs(t)
	v, err := BuildVocabulary(ex, rejected)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rng := filepath.Join(dir, "storage.rng")
	os.WriteFile(rng, []byte(v.RelaxNG()), 0o644)
	check := func(name, body string, wantValid bool) {
		page := `<page><title>t</title><body type="application/xhtml+xml" xmlns:ac="` + nsAC + `" xmlns:ri="` + nsRI + `">` + body + `</body></page>`
		f := filepath.Join(dir, strings.ReplaceAll(name, "/", "_")+".xml")
		os.WriteFile(f, []byte(page), 0o644)
		out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, f).CombinedOutput()
		if (err == nil) != wantValid {
			t.Errorf("%s: valid=%v, want %v\n%s", name, err == nil, wantValid, out)
		}
	}
	for _, e := range ex {
		check(e.Key(), e.Body, true)
		if e.Remote != "" {
			check(e.Key()+".remote", e.Remote, true)
		}
	}
	for _, e := range rejected {
		check(e.Key(), e.Body, false)
	}
	env := `<?xml version="1.0" encoding="UTF-8"?><gfs><content><page id="1" version="2"><title>t</title><labels><label>a</label></labels>` +
		`<body type="application/xhtml+xml" xmlns:ac="` + nsAC + `" xmlns:ri="` + nsRI + `"><p>x</p></body><comment id="7" author="me" created="c" version="1"><p>c</p></comment>` +
		`<attachment id="a" name="n" type="t" size="1" version="1" created="c" author="me"/></page></content></gfs>`
	f := filepath.Join(dir, "envelope.xml")
	os.WriteFile(f, []byte(env), 0o644)
	if out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, f).CombinedOutput(); err != nil {
		t.Errorf("envelope: %v\n%s", err, out)
	}
}
