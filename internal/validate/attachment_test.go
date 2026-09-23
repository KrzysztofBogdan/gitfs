package validate

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
	},
}

func TestAttachmentRules(t *testing.T) {
	ref := p(t, `<page><attachment id="a1" name="x.png" version="2"/></page>`)
	cases := []struct{ name, in, wantErr string }{
		{"unchanged", `<page><attachment id="a1" name="x.png" version="2"/></page>`, ""},
		{"removed is fine", `<page/>`, ""},
		{"new element without id", `<page><attachment name="y.png"/></page>`, "new attachments are created by adding a file"},
		{"unknown id", `<page><attachment id="zz" name="x.png"/></page>`, "attachment[id=zz]: no such attachment on the remote"},
		{"read-only changed", `<page><attachment id="a1" name="x.png" version="3"/></page>`, `attachment[id=a1]: read-only attribute "version" changed`},
		{"content", `<page><attachment id="a1" name="x.png" version="2">data</attachment></page>`, "attachment[id=a1]: must be empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Resource(p(t, c.in), ref, attSchema)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
