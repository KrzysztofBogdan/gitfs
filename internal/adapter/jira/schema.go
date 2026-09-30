package jira

import "github.com/KrzysztofBogdan/gitfs/internal/schema"

// wikiType is the body type of customer-mode text, as the customer API returns it.
const wikiType = "text/x-jira-wiki"

func ro(names ...string) []schema.Attr {
	out := make([]schema.Attr, len(names))
	for i, n := range names {
		out[i] = schema.Attr{Name: n, ReadOnly: true}
	}
	return out
}

func userElem(name string) schema.Elem {
	return schema.Elem{Name: name, Kind: schema.Field, Attrs: []schema.Attr{{Name: "account"}}}
}

func adfBody(name string) schema.Elem {
	return schema.Elem{Name: name, Kind: schema.Body, BodyTypes: []string{adfType}, Attrs: []schema.Attr{{Name: "type"}}}
}

// issueSchema is the <issue> root of agent mode (jira spec §5.1). User
// attributes on the root's fields (account on people) and the two declared
// sub-resource exceptions (internal/public on comments, type on links) are
// not read-only; the adapter checks them in Check.
var issueSchema = &schema.Schema{
	Root: "issue", ID: "id", Version: "updated",
	RootAttrs: ro("id", "key", "created", "updated", "resolved"),
	Elems: []schema.Elem{
		{Name: "summary", Kind: schema.Field},
		{Name: "type", Kind: schema.Field},
		{Name: "status", Kind: schema.Field},
		{Name: "resolution", Kind: schema.Field},
		{Name: "priority", Kind: schema.Field},
		userElem("assignee"),
		userElem("reporter"),
		userElem("creator"),
		{Name: "parent", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "components", Kind: schema.List, Item: "component", Sorted: true},
		{Name: "fixVersions", Kind: schema.List, Item: "version", Sorted: true},
		{Name: "affectsVersions", Kind: schema.List, Item: "version", Sorted: true},
		{Name: "due", Kind: schema.Field},
		{Name: "timetracking", Kind: schema.Field, Attrs: []schema.Attr{{Name: "spent"}}},
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id",
			Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}, {Name: "type"}}},
		adfBody("environment"),
		adfBody("description"),
		{Name: "link", Kind: schema.Sub, ID: "id", SortKey: "id",
			Attrs: append(ro("id"), schema.Attr{Name: "type"})},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name",
			Ops:   []string{"create", "delete"},
			Attrs: ro("id", "name", "size", "mime", "created", "author")},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: append(ro("id", "account", "author", "created", "updated"), schema.Attr{Name: "internal"}, schema.Attr{Name: "public"})},
		{Name: "worklog", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: ro("id", "account", "author", "created", "updated"),
			Children: []schema.Elem{
				{Name: "started", Kind: schema.Field},
				{Name: "spent", Kind: schema.Field},
				adfBody("comment"),
			}},
	},
}

// requestSchema is the <request> root of customer mode (jira spec §5.10).
var requestSchema = &schema.Schema{
	Root: "request", ID: "id", Version: "status-date",
	RootAttrs: ro("id", "key", "desk", "type", "created", "status-date"),
	Elems: []schema.Elem{
		{Name: "summary", Kind: schema.Field},
		{Name: "requestType", Kind: schema.Field},
		{Name: "status", Kind: schema.Field, Attrs: []schema.Attr{{Name: "category"}}},
		userElem("reporter"),
		{Name: "participant", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: []schema.Attr{{Name: "account"}}},
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id",
			Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}, {Name: "type"}}},
		{Name: "description", Kind: schema.Body, BodyTypes: []string{wikiType}, Attrs: []schema.Attr{{Name: "type"}}},
		{Name: "approval", Kind: schema.Sub, ID: "id", SortKey: "id",
			Attrs: append(ro("id", "name", "status"), schema.Attr{Name: "decision"})},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name",
			Ops:   []string{"create"},
			Attrs: ro("id", "name", "size", "mime", "created", "author")},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: ro("id", "account", "author", "created")},
	},
}
