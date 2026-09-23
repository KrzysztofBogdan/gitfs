package engine

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestStripReadOnlyDropsAttachments(t *testing.T) {
	s := &schema.Schema{
		Root: "note", ID: "id", Version: "version",
		RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
		Elems: []schema.Elem{
			{Name: "title", Kind: schema.Field},
			{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name",
				Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}}},
		},
	}
	root, err := xmltree.ParseString(`<note id="1" version="2"><title>T</title><attachment id="a1" name="x.png"/></note>`)
	if err != nil {
		t.Fatal(err)
	}
	StripReadOnly(root, s)
	if got := xmltree.Print(root, 0); got != "<note>\n  <title>T</title>\n</note>" {
		t.Fatalf("got\n%s", got)
	}
}
