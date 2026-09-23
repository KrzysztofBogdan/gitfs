package merge

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var s = &schema.Schema{
	Root: "page", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type", ReadOnly: false}}, BodyTypes: []string{"x"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "author", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

func p(t *testing.T, x string) *xmltree.Node {
	t.Helper()
	n, err := xmltree.ParseString(x)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func run(t *testing.T, b, l, r string) Result {
	t.Helper()
	res, err := Merge(p(t, b), p(t, l), p(t, r), s, "remote v4")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCleanDifferentElements(t *testing.T) {
	res := run(t,
		`<page id="1" version="3"><title>T</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="3"><title>Local</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="4"><title>T</title><labels><label>a</label><label>r</label></labels></page>`)
	if res.Conflicted() {
		t.Fatalf("unexpected conflict:\n%s", res.Text)
	}
	got := xmltree.Print(res.Root, 0)
	for _, want := range []string{`version="4"`, "<title>Local</title>", "<label>r</label>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestSubResources(t *testing.T) {
	res := run(t,
		`<page id="1"><comment id="1" created="1">a</comment><comment id="2" created="2">b</comment></page>`,
		`<page id="1"><comment id="1" created="1">a</comment><comment>new local</comment></page>`,
		`<page id="1"><comment id="1" created="1">a</comment><comment id="2" created="2">b</comment><comment id="3" created="3">new remote</comment></page>`)
	if res.Conflicted() {
		t.Fatal(res.Text)
	}
	want := "<page id=\"1\">\n  <comment id=\"1\" created=\"1\">a</comment>\n  <comment id=\"3\" created=\"3\">new remote</comment>\n  <comment>new local</comment>\n</page>"
	if got := xmltree.Print(res.Root, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestDeleteVersusChangeConflicts(t *testing.T) {
	res := run(t,
		`<page id="1"><comment id="2" created="2">b</comment></page>`,
		`<page id="1"/>`,
		`<page id="1"><comment id="2" created="2">b edited</comment></page>`)
	if !res.Conflicted() || res.Elements != 1 {
		t.Fatalf("want 1 conflict, got %+v", res)
	}
}

func TestBodyTextualMergeClean(t *testing.T) {
	b := "<page id=\"1\"><body type=\"x\"><p>1</p><p>2</p><p>3</p></body></page>"
	l := "<page id=\"1\"><body type=\"x\"><p>one</p><p>2</p><p>3</p></body></page>"
	r := "<page id=\"1\"><body type=\"x\"><p>1</p><p>2</p><p>three</p></body></page>"
	res := run(t, b, l, r)
	if res.Conflicted() {
		t.Fatal(res.Text)
	}
	got := xmltree.Print(res.Root, 0)
	if !strings.Contains(got, "<p>one</p>") || !strings.Contains(got, "<p>three</p>") {
		t.Fatal(got)
	}
}

func TestConflictMarkersGolden(t *testing.T) {
	res := run(t,
		`<page id="1" version="3"><title>T</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="3"><title>T</title><labels><label>a</label><label>local</label></labels></page>`,
		`<page id="1" version="4"><title>T</title><labels><label>a</label><label>remote</label></labels></page>`)
	want := `    <page id="1" version="4">
      <title>T</title>
      <labels>
        <label>a</label>
<<<<<<< local
        <label>local</label>
||||||| base
=======
        <label>remote</label>
>>>>>>> remote v4
      </labels>
    </page>`
	if res.Text != want || res.Elements != 1 || res.Hunks != 1 {
		t.Fatalf("got (%d el, %d hunks)\n%s\nwant\n%s", res.Elements, res.Hunks, res.Text, want)
	}
}

func TestCleanDiff3ButIllFormedConflicts(t *testing.T) {
	// Each side is well-formed; line-merging them crosses <b> and <i>.
	b := "<page id=\"1\"><body type=\"x\">a\nb\nc\nd\ne\nf\ng</body></page>"
	l := "<page id=\"1\"><body type=\"x\"><b>a\nb\nc\nd\ne</b>\nf\ng</body></page>"
	r := "<page id=\"1\"><body type=\"x\">a\nb\n<i>c\nd\ne\nf\ng</i></body></page>"
	res := run(t, b, l, r)
	if !res.Conflicted() || res.Hunks != 1 || !strings.Contains(res.Text, "<<<<<<< local") {
		t.Fatalf("want whole-body conflict, got %+v", res)
	}
}
