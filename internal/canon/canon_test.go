package canon

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var testSchema = &schema.Schema{
	Root:      "page",
	ID:        "id",
	Version:   "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}, {Name: "parent", ReadOnly: true}, {Name: "created", ReadOnly: true}, {Name: "updated", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type", ReadOnly: false}}, BodyTypes: []string{"application/xhtml+xml"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "author", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

func canonical(t *testing.T, in string) string {
	t.Helper()
	n, err := xmltree.ParseString(in)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(n, testSchema)
	return xmltree.Print(n, 0)
}

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"root attr order", `<page updated="u" zz="1" id="1" aa="2"><title>T</title></page>`,
			"<page id=\"1\" updated=\"u\" aa=\"2\" zz=\"1\">\n  <title>T</title>\n</page>"},
		{"child order by schema", `<page><comment id="9" created="2">c</comment><body type="text/plain">b</body><title>T</title></page>`,
			"<page>\n  <title>T</title>\n  <body type=\"text/plain\">b</body>\n  <comment id=\"9\" created=\"2\">c</comment>\n</page>"},
		{"subs sorted, new last", `<page><comment>new</comment><comment id="2" created="2026-02">b</comment><comment id="1" created="2026-01">a</comment></page>`,
			"<page>\n  <comment id=\"1\" created=\"2026-01\">a</comment>\n  <comment id=\"2\" created=\"2026-02\">b</comment>\n  <comment>new</comment>\n</page>"},
		{"empty fields dropped", `<page><title> </title><labels/></page>`, `<page/>`},
		{"list sorted", `<page><labels><label>b</label><label>a</label></labels></page>`,
			"<page>\n  <labels>\n    <label>a</label>\n    <label>b</label>\n  </labels>\n</page>"},
		{"body attrs sorted inside, text untouched", `<page><body xmlns:ac="u" type="application/xhtml+xml"><p>x <ac:m ac:z="1" ac:a="2">y</ac:m></p></body></page>`,
			"<page>\n  <body type=\"application/xhtml+xml\" xmlns:ac=\"u\">\n    <p>x <ac:m ac:a=\"2\" ac:z=\"1\">y</ac:m></p>\n  </body>\n</page>"},
		{"comments and unknown kept order", `<page><!-- x --><zzz/><title>T</title><yyy/></page>`,
			"<page>\n  <title>T</title>\n  <zzz/>\n  <yyy/>\n</page>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonical(t, c.in); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	in := `<page version="3" id="1"><labels><label>z</label><label>a</label></labels><comment>n</comment><title>T</title><body type="application/xhtml+xml"><p>a</p><ul><li b="1" a="2">x</li></ul></body></page>`
	once := canonical(t, in)
	if twice := canonical(t, once); once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

func TestRepeatedFieldSortedByKey(t *testing.T) {
	s := &schema.Schema{Root: "r", Elems: []schema.Elem{
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id", Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}}},
	}}
	root, _ := xmltree.ParseString(`<r><field name="b" id="c2">2</field><field>new</field><field id="c1">1</field></r>`)
	Normalize(root, s)
	if got := xmltree.Print(root, 0); got != "<r>\n  <field id=\"c1\">1</field>\n  <field id=\"c2\" name=\"b\">2</field>\n  <field>new</field>\n</r>" {
		t.Fatal(got)
	}
}

// An element with Less sorts by it instead of SortKey: DNS records by name,
// type, id, with new records (no id) after the ones they sort with.
func TestSubSortedByLess(t *testing.T) {
	less := func(a, b *xmltree.Node) bool {
		an, _ := a.Attr("name")
		bn, _ := b.Attr("name")
		if an != bn {
			return an < bn
		}
		at, _ := a.Attr("type")
		bt, _ := b.Attr("type")
		return at < bt
	}
	s := &schema.Schema{Root: "zone", Elems: []schema.Elem{
		{Name: "record", Kind: schema.Sub, ID: "id", SortKey: "id", Less: less},
	}}
	root, _ := xmltree.ParseString(`<zone><record id="1" name="www" type="A">1</record><record name="api" type="TXT">t</record><record id="3" name="api" type="A">2</record></zone>`)
	Normalize(root, s)
	want := "<zone>\n  <record id=\"3\" name=\"api\" type=\"A\">2</record>\n  <record name=\"api\" type=\"TXT\">t</record>\n  <record id=\"1\" name=\"www\" type=\"A\">1</record>\n</zone>"
	if got := xmltree.Print(root, 0); got != want {
		t.Fatal(got)
	}
}
