package confluence

import "github.com/KrzysztofBogdan/gitfs/internal/schema"

const (
	nsAC     = "http://atlassian.com/content"
	nsRI     = "http://atlassian.com/resource/identifier"
	bodyType = "application/xhtml+xml"
)

var pageSchema = &schema.Schema{
	Root: "page", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{
		{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}, {Name: "parent", ReadOnly: true},
		{Name: "created", ReadOnly: true}, {Name: "updated", ReadOnly: true},
	},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, BodyTypes: []string{bodyType},
			Attrs: []schema.Attr{{Name: "type"}, {Name: "xmlns:ac"}, {Name: "xmlns:ri"}}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "author", ReadOnly: true},
				{Name: "created", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Ops: []string{"create", "update", "delete"},
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "type", ReadOnly: true},
				{Name: "size", ReadOnly: true}, {Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true},
				{Name: "author", ReadOnly: true}}},
	},
}
