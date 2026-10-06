package bitbucket

import (
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func ro(names ...string) []schema.Attr {
	out := make([]schema.Attr, len(names))
	for i, n := range names {
		out[i] = schema.Attr{Name: n, ReadOnly: true}
	}
	return out
}

var stateElem = schema.Elem{Name: "state", Kind: schema.Field, Attrs: []schema.Attr{{Name: "strategy"}, {Name: "message"}}}

// prElems is a <pullrequest> (bitbucket spec §5.1). User attributes the
// service also writes (reviewer account, comment parent/path/line/side) are
// checked in Check.
var prElems = []schema.Elem{
	{Name: "title", Kind: schema.Field},
	stateElem,
	{Name: "draft", Kind: schema.Field},
	{Name: "source", Kind: schema.Field, Attrs: []schema.Attr{{Name: "branch"}, {Name: "commit", ReadOnly: true}}},
	{Name: "destination", Kind: schema.Field, Attrs: []schema.Attr{{Name: "branch"}, {Name: "commit", ReadOnly: true}}},
	{Name: "close-source-branch", Kind: schema.Field},
	{Name: "author", Kind: schema.Field, Attrs: []schema.Attr{{Name: "account"}}},
	{Name: "reviewer", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: []schema.Attr{{Name: "account"}, {Name: "state", ReadOnly: true}}},
	{Name: "participant", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: ro("account", "state")},
	{Name: "my-review", Kind: schema.Field},
	{Name: "merge-commit", Kind: schema.Field},
	{Name: "description", Kind: schema.Field},
	{Name: "build", Kind: schema.Field, Repeated: true, SortKey: "key", Attrs: ro("key", "state", "url", "run")},
	{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
		Attrs: append(ro("id", "account", "author", "created", "updated"),
			schema.Attr{Name: "parent"}, schema.Attr{Name: "path"}, schema.Attr{Name: "line"}, schema.Attr{Name: "side"}, schema.Attr{Name: "resolved"})},
}

// runElems is a <pipeline> (bitbucket spec §5.5); all of it is the service's.
var runElems = []schema.Elem{
	stateElem,
	{Name: "result", Kind: schema.Field},
	{Name: "trigger", Kind: schema.Field, Attrs: ro("type", "account")},
	{Name: "target", Kind: schema.Field, Attrs: ro("type", "ref", "commit", "pipeline", "pr")},
	{Name: "duration", Kind: schema.Field, Attrs: ro("build-seconds")},
	{Name: "step", Kind: schema.Sub, ID: "id", Less: stepLess,
		Attrs:    ro("id", "n", "name", "image", "parallel", "stage", "state", "result", "started", "completed", "duration", "log-size"),
		Children: []schema.Elem{{Name: "log", Kind: schema.Field}}},
	{Name: "steplog", Kind: schema.Attachment, ID: "step", SortKey: "name", NameAttr: "name", Attrs: ro("step", "name", "size")},
}

var rootAttrs = ro("id", "number", "created", "updated", "completed", "version")

var (
	prSchema  = &schema.Schema{Root: "pullrequest", ID: "id", Version: "version", RootAttrs: rootAttrs, Elems: prElems}
	runSchema = &schema.Schema{Root: "pipeline", ID: "id", Version: "version", RootAttrs: rootAttrs, Elems: runElems}
	// mergedSchema is what the engine sees (it has one schema per adapter):
	// both roots' elements in one canonical order, state shared. Check keeps
	// each file to its own root's elements.
	mergedSchema = merge(prSchema, runSchema)
)

// merge is a with b's root as an alternate and b's elements a lacks, each
// placed after the element that precedes it in b.
func merge(a, b *schema.Schema) *schema.Schema {
	out := *a
	out.Roots = []string{b.Root}
	out.Elems = append([]schema.Elem(nil), a.Elems...)
	prev := ""
	for _, e := range b.Elems {
		if schema.Find(out.Elems, e.Name) == nil {
			at := len(out.Elems)
			for i := range out.Elems {
				if out.Elems[i].Name == prev {
					at = i + 1
				}
			}
			out.Elems = append(out.Elems[:at], append([]schema.Elem{e}, out.Elems[at:]...)...)
		}
		prev = e.Name
	}
	return &out
}

// stepLess orders steps as Bitbucket runs them.
func stepLess(x, y *xmltree.Node) bool {
	nx, _ := x.Attr("n")
	ny, _ := y.Attr("n")
	a, _ := strconv.Atoi(nx)
	b, _ := strconv.Atoi(ny)
	return a < b
}
