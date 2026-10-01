package ovh

import (
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

// zoneSchema is an OVH zone file (DNS spec §5.1).
var zoneSchema = &schema.Schema{
	Root:      "zone",
	RootAttrs: []schema.Attr{{Name: "name", ReadOnly: true}},
	ID:        "name",
	Elems: []schema.Elem{
		{Name: "soa", Kind: schema.Field, Attrs: []schema.Attr{{Name: "ttl"}, {Name: "refresh"}, {Name: "expire"},
			{Name: "nx-domain-ttl"}, {Name: "email"}, {Name: "server", ReadOnly: true}, {Name: "serial", ReadOnly: true}}},
		{Name: "dnssec", Kind: schema.Field},
		{Name: "record", Kind: schema.Sub, ID: "id", Less: dnsx.Less,
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name"}, {Name: "type"}, {Name: "ttl"}}},
		{Name: "redirect", Kind: schema.Sub, ID: "id", Less: dnsx.Less,
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name"}, {Name: "type"}, {Name: "title"}, {Name: "keywords"}, {Name: "description"}}},
		{Name: "dynhost", Kind: schema.Sub, ID: "id", Less: dnsx.Less,
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name"}, {Name: "ttl", ReadOnly: true}}},
		{Name: "dynhost-login", Kind: schema.Sub, ID: "login", SortKey: "login",
			Attrs: []schema.Attr{{Name: "login", ReadOnly: true}, {Name: "suffix"}, {Name: "name"}}},
	},
}
