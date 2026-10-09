package ksef

import "github.com/KrzysztofBogdan/gitfs/internal/schema"

func ro(names ...string) []schema.Attr {
	out := make([]schema.Attr, len(names))
	for i, n := range names {
		out[i] = schema.Attr{Name: n, ReadOnly: true}
	}
	return out
}

// invoiceSchema is an <invoice> (ksef spec §5); all of it is KSeF's. The
// identity is <role>/<ksef number>: an invoice can be in two roles.
var invoiceSchema = &schema.Schema{
	Root: "invoice", ID: "id", Version: "version",
	RootAttrs: ro("id", "ksef", "role", "version", "self-invoicing", "has-attachment"),
	Elems: []schema.Elem{
		{Name: "number", Kind: schema.Field},
		{Name: "type", Kind: schema.Field},
		{Name: "form", Kind: schema.Field},
		{Name: "issued", Kind: schema.Field},
		{Name: "invoicing", Kind: schema.Field},
		{Name: "acquired", Kind: schema.Field},
		{Name: "stored", Kind: schema.Field},
		{Name: "mode", Kind: schema.Field},
		{Name: "seller", Kind: schema.Field, Attrs: ro("nip")},
		{Name: "buyer", Kind: schema.Field, Attrs: ro("type", "id")},
		{Name: "third-party", Kind: schema.Field, Repeated: true, SortKey: "id", Attrs: ro("type", "id", "role")},
		{Name: "amounts", Kind: schema.Field, Attrs: ro("currency", "net", "vat", "gross")},
		{Name: "corrects", Kind: schema.Field, Attrs: ro("hash")},
		{Name: "original", Kind: schema.Attachment, ID: "id", SortKey: "id", NameAttr: "name", Attrs: ro("id", "name", "size", "sha256")},
	},
}
