package merge

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "id", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
	},
}

func TestAttachmentsMergeByIdentity(t *testing.T) {
	base := p(t, `<page><title>T</title><attachment id="a1" name="x" version="1"/></page>`)
	local := p(t, `<page><title>Local</title><attachment id="a1" name="x" version="1"/></page>`)
	remote := p(t, `<page><title>T</title><attachment id="a1" name="x" version="2"/><attachment id="a2" name="y" version="1"/></page>`)
	res, err := Merge(base, local, remote, attSchema, "remote v2")
	if err != nil || res.Conflicted() {
		t.Fatalf("%+v %v", res, err)
	}
	got := xmltree.Print(res.Root, 0)
	for _, want := range []string{"<title>Local</title>", `<attachment id="a1" name="x" version="2"/>`, `<attachment id="a2" name="y" version="1"/>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
