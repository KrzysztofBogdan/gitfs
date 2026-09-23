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
	},
}
