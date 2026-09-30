// Package canon applies an adapter schema to a resource tree so that
// xmltree.Print yields the canonical form.
package canon

import (
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Normalize reorders and prunes root in place according to s.
func Normalize(root *xmltree.Node, s *schema.Schema) {
	orderAttrs(root, s.RootAttrs)
	normalizeChildren(root, s.Elems)
}

func orderAttrs(n *xmltree.Node, decl []schema.Attr) {
	var head, tail []xmltree.Attr
	for _, d := range decl {
		if v, ok := n.Attr(d.Name); ok {
			head = append(head, xmltree.Attr{Name: d.Name, Value: v})
		}
	}
	for _, a := range n.Attrs {
		if _, ok := schema.AttrDecl(decl, a.Name); !ok {
			tail = append(tail, a)
		}
	}
	sort.SliceStable(tail, func(i, j int) bool { return tail[i].Name < tail[j].Name })
	n.Attrs = append(head, tail...)
}

func sortAttrsDeep(n *xmltree.Node) {
	for _, c := range n.Children {
		if c.Kind == xmltree.Element {
			orderAttrs(c, nil)
			sortAttrsDeep(c)
		}
	}
}

func isEmpty(n *xmltree.Node) bool {
	return len(n.Attrs) == 0 && len(n.Elements()) == 0 && strings.TrimSpace(n.TextContent()) == ""
}

func normalizeChildren(parent *xmltree.Node, elems []schema.Elem) {
	groups := make([][]*xmltree.Node, len(elems))
	var unknown []*xmltree.Node
	for _, c := range parent.Children {
		if c.Kind != xmltree.Element {
			continue // whitespace text and comments are dropped
		}
		i := indexOf(elems, c.Name)
		if i < 0 {
			unknown = append(unknown, c)
			continue
		}
		e := &elems[i]
		orderAttrs(c, e.Attrs)
		switch e.Kind {
		case schema.Field, schema.List:
			if isEmpty(c) {
				continue
			}
			if e.Kind == schema.List && e.Sorted {
				items := c.Elements()
				sort.SliceStable(items, func(a, b int) bool { return items[a].TextContent() < items[b].TextContent() })
				c.Children = items
			}
		case schema.Body:
			sortAttrsDeep(c)
		case schema.Sub:
			normalizeSub(c, e)
		}
		groups[i] = append(groups[i], c)
	}
	var out []*xmltree.Node
	for i, g := range groups {
		kind := elems[i].Kind
		sortable := kind == schema.Sub || kind == schema.Attachment || (kind == schema.Field && elems[i].Repeated)
		if sortable && elems[i].SortKey != "" {
			key, idAttr := elems[i].SortKey, elems[i].ID
			sort.SliceStable(g, func(a, b int) bool {
				ka, oka := g[a].Attr(key)
				kb, okb := g[b].Attr(key)
				if oka != okb {
					return oka // keyed before unkeyed
				}
				if !oka {
					return false
				}
				if ka != kb || kind != schema.Attachment {
					return ka < kb
				}
				ia, _ := g[a].Attr(idAttr)
				ib, _ := g[b].Attr(idAttr)
				return ia < ib // attachments: stable order even when timestamps tie
			})
		}
		out = append(out, g...)
	}
	parent.Children = append(out, unknown...)
}

// normalizeSub normalises declared nested children and treats the rest like a body.
func normalizeSub(n *xmltree.Node, e *schema.Elem) {
	if len(e.Children) == 0 {
		sortAttrsDeep(n)
		return
	}
	var own, nested []*xmltree.Node
	for _, c := range n.Children {
		if c.Kind == xmltree.Element && indexOf(e.Children, c.Name) >= 0 {
			nested = append(nested, c)
		} else {
			own = append(own, c)
		}
	}
	tmp := &xmltree.Node{Kind: xmltree.Element, Children: nested}
	normalizeChildren(tmp, e.Children)
	for _, c := range own {
		if c.Kind == xmltree.Element {
			orderAttrs(c, nil)
			sortAttrsDeep(c)
		}
	}
	n.Children = append(own, tmp.Children...)
}

func indexOf(elems []schema.Elem, name string) int {
	for i := range elems {
		if elems[i].Name == name {
			return i
		}
	}
	return -1
}
