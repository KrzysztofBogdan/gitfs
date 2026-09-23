// Package xmltree is a small XML tree that round-trips prefixes, keeps mixed
// content verbatim, and prints in gfs canonical formatting.
package xmltree

import "strings"

type Kind int

const (
	Element Kind = iota
	Text
	Comment
	Raw // printed verbatim, no indentation; used for conflict markers
)

type Attr struct{ Name, Value string }

type Node struct {
	Kind     Kind
	Name     string // Element only, with prefix as written
	Attrs    []Attr
	Children []*Node
	Text     string // Text, Comment, Raw
}

func (n *Node) Attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// SetAttr replaces an existing attribute in place or appends a new one.
func (n *Node) SetAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, Attr{name, value})
}

func (n *Node) DelAttr(name string) {
	out := n.Attrs[:0]
	for _, a := range n.Attrs {
		if a.Name != name {
			out = append(out, a)
		}
	}
	n.Attrs = out
}

func (n *Node) Elements() []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == Element {
			out = append(out, c)
		}
	}
	return out
}

func (n *Node) Child(name string) *Node {
	for _, c := range n.Children {
		if c.Kind == Element && c.Name == name {
			return c
		}
	}
	return nil
}

func (n *Node) ChildrenNamed(name string) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == Element && c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *Node) TextContent() string {
	var b strings.Builder
	for _, c := range n.Children {
		if c.Kind == Text {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Attrs = append([]Attr(nil), n.Attrs...)
	c.Children = make([]*Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = ch.Clone()
	}
	return &c
}

func Equal(a, b *Node) bool { return Print(a, 0) == Print(b, 0) }
