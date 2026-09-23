package canon

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true},
				{Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

func TestAttachmentsSortedByKeyThenID(t *testing.T) {
	n, err := xmltree.ParseString(`<page><attachment created="2" id="b" name="y"/><attachment version="1" id="c" name="z" created="1"/><title>T</title><attachment id="a" name="x" created="2"/></page>`)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(n, attSchema)
	want := "<page>\n  <title>T</title>\n  <attachment id=\"c\" name=\"z\" version=\"1\" created=\"1\"/>\n  <attachment id=\"a\" name=\"x\" created=\"2\"/>\n  <attachment id=\"b\" name=\"y\" created=\"2\"/>\n</page>"
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
