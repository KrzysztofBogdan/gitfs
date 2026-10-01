package validate

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
		{Name: "labels", Kind: schema.List, Item: "label"},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type", ReadOnly: false}}, BodyTypes: []string{"application/xhtml+xml"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "author", ReadOnly: true}}},
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

const base = `<page id="1" version="3"><title>T</title><comment id="7" author="bob">c</comment></page>`

func TestResource(t *testing.T) {
	cases := []struct{ name, local, ref, wantErr string }{
		{"ok unchanged", base, base, ""},
		{"ok new comment", `<page id="1" version="3"><title>T</title><comment id="7" author="bob">c</comment><comment>n</comment></page>`, base, ""},
		{"ok new page", `<page><title>T</title><body type="application/xhtml+xml"><p/></body></page>`, "", ""},
		{"wrong root", `<issue/>`, "", "root element <issue>, want <page>"},
		{"unknown element", `<page><foo/></page>`, "", "unknown element <foo>"},
		{"stray text", `<page>hello<title>T</title></page>`, "", "text outside elements"},
		{"field twice", `<page><title>a</title><title>b</title></page>`, "", "<title> occurs more than once"},
		{"bad list item", `<page><labels><tag>x</tag></labels></page>`, "", "<labels> may only contain <label>"},
		{"body no type", `<page><body><p/></body></page>`, "", `<body> type "" not allowed`},
		{"body wrong type", `<page><body type="text/html">x</body></page>`, "", `<body> type "text/html" not allowed`},
		{"version edited", `<page id="1" version="4"><title>T</title><comment id="7" author="bob">c</comment></page>`, base, `page: read-only attribute "version" changed`},
		{"id on new page", `<page id="5"/>`, "", `page: read-only attribute "id" changed`},
		{"comment author edited", `<page id="1" version="3"><comment id="7" author="eve">c</comment></page>`, base, `comment[id=7]: read-only attribute "author" changed`},
		{"unknown comment id", `<page id="1" version="3"><comment id="99">c</comment></page>`, base, `comment[id=99]: no such comment on the remote`},
		{"new comment with author", `<page id="1" version="3"><comment author="me">c</comment></page>`, base, `comment[1]: read-only attribute "author" changed`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ref *xmltree.Node
			if c.ref != "" {
				ref = p(t, c.ref)
			}
			err := Resource(p(t, c.local), ref, s)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want %q", err, c.wantErr)
			}
		})
	}
}

// A single Field's read-only attributes must match the remote's (DNS SOA
// serial): a change, an addition or a removal is refused.
func TestFieldReadOnlyAttrs(t *testing.T) {
	s := &schema.Schema{Root: "zone", Elems: []schema.Elem{
		{Name: "soa", Kind: schema.Field, Attrs: []schema.Attr{{Name: "ttl"}, {Name: "serial", ReadOnly: true}}},
	}}
	ref, _ := xmltree.ParseString(`<zone><soa ttl="1" serial="7"/></zone>`)
	for in, ok := range map[string]bool{
		`<zone><soa ttl="2" serial="7"/></zone>`: true,
		`<zone><soa ttl="1" serial="8"/></zone>`: false,
		`<zone><soa ttl="1"/></zone>`:            false,
		`<zone/>`:                                true,
	} {
		n, _ := xmltree.ParseString(in)
		if err := Resource(n, ref, s); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}
	n, _ := xmltree.ParseString(`<zone><soa serial="1"/></zone>`)
	if err := Resource(n, nil, s); err == nil {
		t.Error("a new resource may not set a read-only attribute")
	}
}
