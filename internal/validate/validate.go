// Package validate checks a resource tree against its adapter schema.
package validate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Resource validates root against s. ref is the remote/base version of the
// same resource, or nil for a new one.
func Resource(root, ref *xmltree.Node, s *schema.Schema) error {
	var errs []error
	if root.Name != s.Root {
		return fmt.Errorf("root element <%s>, want <%s>", root.Name, s.Root)
	}
	errs = append(errs, readOnly(s.Root, root, ref, s.RootAttrs)...)
	errs = append(errs, children(root, ref, s.Elems)...)
	return errors.Join(errs...)
}

func readOnly(label string, n, ref *xmltree.Node, decl []schema.Attr) []error {
	var errs []error
	for _, d := range decl {
		if !d.ReadOnly {
			continue
		}
		v, ok := n.Attr(d.Name)
		var rv string
		var rok bool
		if ref != nil {
			rv, rok = ref.Attr(d.Name)
		}
		if ok != rok || v != rv {
			errs = append(errs, fmt.Errorf("%s: read-only attribute %q changed", label, d.Name))
		}
	}
	return errs
}

func children(n, ref *xmltree.Node, elems []schema.Elem) []error {
	var errs []error
	count := map[string]int{}
	newIdx := map[string]int{}
	for _, c := range n.Children {
		switch c.Kind {
		case xmltree.Text:
			if strings.TrimSpace(c.Text) != "" {
				errs = append(errs, fmt.Errorf("<%s>: text outside elements", n.Name))
			}
			continue
		case xmltree.Element:
		default:
			continue
		}
		e := schema.Find(elems, c.Name)
		if e == nil {
			errs = append(errs, fmt.Errorf("unknown element <%s>", c.Name))
			continue
		}
		count[c.Name]++
		switch e.Kind {
		case schema.Field, schema.List, schema.Body:
			if count[c.Name] == 2 && !(e.Kind == schema.Field && e.Repeated) {
				errs = append(errs, fmt.Errorf("<%s> occurs more than once", c.Name))
			}
		}
		switch e.Kind {
		case schema.List:
			for _, it := range c.Elements() {
				if it.Name != e.Item {
					errs = append(errs, fmt.Errorf("<%s> may only contain <%s>", c.Name, e.Item))
					break
				}
			}
		case schema.Body:
			typ, _ := c.Attr("type")
			if !slices.Contains(e.BodyTypes, typ) {
				errs = append(errs, fmt.Errorf("<%s> type %q not allowed", c.Name, typ))
			}
		case schema.Sub:
			errs = append(errs, sub(c, ref, e, newIdx)...)
		}
	}
	return errs
}

func sub(c, parentRef *xmltree.Node, e *schema.Elem, newIdx map[string]int) []error {
	id, hasID := c.Attr(e.ID)
	if !hasID {
		newIdx[c.Name]++
		label := fmt.Sprintf("%s[%d]", c.Name, newIdx[c.Name])
		return append(readOnly(label, c, nil, withoutID(e)), children(ownless(c, e), nil, e.Children)...)
	}
	label := fmt.Sprintf("%s[id=%s]", c.Name, id)
	ref := FindSub(parentRef, c.Name, e.ID, id)
	if ref == nil {
		return []error{fmt.Errorf("%s: no such %s on the remote", label, c.Name)}
	}
	return append(readOnly(label, c, ref, e.Attrs), children(ownless(c, e), ref, e.Children)...)
}

// withoutID drops the identity attribute: its absence is what makes a sub new.
func withoutID(e *schema.Elem) []schema.Attr {
	var out []schema.Attr
	for _, a := range e.Attrs {
		if a.Name != e.ID {
			out = append(out, a)
		}
	}
	return out
}

// ownless returns a view of c holding only its declared nested children, so
// that a sub's own (body-like) content is not checked as schema elements.
func ownless(c *xmltree.Node, e *schema.Elem) *xmltree.Node {
	v := &xmltree.Node{Kind: xmltree.Element, Name: c.Name}
	for _, ch := range c.Children {
		if ch.Kind == xmltree.Element && schema.Find(e.Children, ch.Name) != nil {
			v.Children = append(v.Children, ch)
		}
	}
	return v
}

// FindSub returns parent's child element named name whose attribute idAttr is id.
func FindSub(parent *xmltree.Node, name, idAttr, id string) *xmltree.Node {
	if parent == nil {
		return nil
	}
	for _, c := range parent.ChildrenNamed(name) {
		if v, ok := c.Attr(idAttr); ok && v == id {
			return c
		}
	}
	return nil
}
