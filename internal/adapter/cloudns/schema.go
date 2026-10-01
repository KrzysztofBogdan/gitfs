package cloudns

import (
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

// zoneSchema is a ClouDNS zone file (DNS spec §5.2). Type-specific record
// fields (caa-flag, weight, …) are attributes outside this list, kept in
// name order. <failover> is a repeated field keyed by the record it watches:
// the adapter compares the set as a whole (a new failover carries its
// record's id, so it cannot be a sub-resource).
var zoneSchema = &schema.Schema{
	Root: "zone",
	RootAttrs: []schema.Attr{{Name: "name", ReadOnly: true}, {Name: "type", ReadOnly: true}, {Name: "kind", ReadOnly: true},
		{Name: "active"}},
	ID: "name",
	Elems: []schema.Elem{
		{Name: "soa", Kind: schema.Field, Attrs: []schema.Attr{{Name: "primary"}, {Name: "admin"}, {Name: "refresh"}, {Name: "retry"},
			{Name: "expire"}, {Name: "ttl"}, {Name: "serial", ReadOnly: true}}},
		{Name: "dnssec", Kind: schema.Field, Attrs: []schema.Attr{{Name: "status"}}},
		{Name: "record", Kind: schema.Sub, ID: "id", Less: dnsx.Less,
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name"}, {Name: "type"}, {Name: "ttl"}, {Name: "geo"},
				{Name: "status"}, {Name: "priority"}, {Name: "weight"}, {Name: "port"}}},
		{Name: "failover", Kind: schema.Field, Repeated: true, SortKey: "record", Attrs: []schema.Attr{{Name: "record"}}},
		{Name: "mail-forward", Kind: schema.Sub, ID: "id", SortKey: "box",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "box"}, {Name: "host"}, {Name: "destination"}}},
	},
}
