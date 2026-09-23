package confluence

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func names(id string) string { return map[string]string{"bob-id": "bob"}[id] }

func TestPageNodeGolden(t *testing.T) {
	p := apiPage{ID: "98120", Title: "Architecture", ParentID: "98001", CreatedAt: "2025-11-03T09:15:00.000Z",
		Version: apiVersion{Number: 8, AuthorID: "bob-id", CreatedAt: "2026-09-22T14:03:00.000Z"}}
	p.Body.Storage.Value = `<p>Hi <ac:link><ri:page ri:content-title="X"/></ac:link></p><ac:structured-macro ac:name="info"><ac:rich-text-body><p>n</p></ac:rich-text-body></ac:structured-macro>`
	c := apiComment{ID: "7731", Version: apiVersion{Number: 1, AuthorID: "bob-id", CreatedAt: "2026-09-16T17:10:00.000Z"}}
	c.Body.Storage.Value = "<p>Out of date.</p>"
	n, err := pageNode(p, []string{"b", "a"}, []apiComment{c}, names)
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(n, pageSchema)
	want := `<page id="98120" version="8" parent="98001" created="2025-11-03T09:15:00.000Z" updated="2026-09-22T14:03:00.000Z">
  <title>Architecture</title>
  <labels>
    <label>a</label>
    <label>b</label>
  </labels>
  <body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">
    <p>Hi <ac:link><ri:page ri:content-title="X"/></ac:link></p>
    <ac:structured-macro ac:name="info">
      <ac:rich-text-body>
        <p>n</p>
      </ac:rich-text-body>
    </ac:structured-macro>
  </body>
  <comment id="7731" author="bob" created="2026-09-16T17:10:00.000Z" version="1">
    <p>Out of date.</p>
  </comment>
</page>`
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if titleOf(n) != "Architecture" || strings.Join(labelsOf(n), ",") != "a,b" {
		t.Fatal("titleOf/labelsOf")
	}
}

func TestStorageRoundTrip(t *testing.T) {
	cases := []string{
		`<p>a&nbsp;b &amp; c</p>`,
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[./deploy.sh && echo <ok>]]></ac:plain-text-body></ac:structured-macro>`,
		`<table><tbody><tr><td data-highlight-colour="#e3fcef">99.9%</td></tr></tbody></table>`,
		`plain text only`,
		``,
	}
	for _, in := range cases {
		n, err := parseStorage("body", in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		once := storageOf(n)
		n2, err := parseStorage("body", once)
		if err != nil {
			t.Fatalf("reparse %q: %v", once, err)
		}
		if twice := storageOf(n2); once != twice {
			t.Fatalf("not stable:\n%s\n%s", once, twice)
		}
	}
	n, _ := parseStorage("body", cases[1])
	if !strings.Contains(storageOf(n), "<![CDATA[./deploy.sh && echo <ok>]]>") {
		t.Fatal(storageOf(n))
	}
	n, _ = parseStorage("body", cases[0])
	if got := storageOf(n); got != "<p>a\u00a0b &amp; c</p>" {
		t.Fatalf("text must be entity-escaped, not CDATA: %q", got)
	}
	if storageOf(nil) != "" {
		t.Fatal("nil")
	}
}
