package xmltree

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// Parse reads one XML document and returns its root element.
func Parse(r io.Reader) (*Node, error) {
	d := xml.NewDecoder(r)
	d.Strict = true
	d.Entity = xml.HTMLEntity
	var root *Node
	var stack []*Node
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Kind: Element, Name: qname(t.Name)}
			for _, a := range t.Attr {
				n.Attrs = append(n.Attrs, Attr{qname(a.Name), a.Value})
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("more than one root element")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].Name != qname(t.Name) {
				return nil, fmt.Errorf("unexpected </%s>", qname(t.Name))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, errors.New("text outside root element")
				}
				continue
			}
			p := stack[len(stack)-1]
			if k := len(p.Children); k > 0 && p.Children[k-1].Kind == Text {
				p.Children[k-1].Text += string(t) // CDATA next to text: one node
			} else {
				p.Children = append(p.Children, &Node{Kind: Text, Text: string(t)})
			}
		case xml.Comment:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, &Node{Kind: Comment, Text: string(t)})
			}
		case xml.ProcInst:
			// XML declaration and processing instructions are dropped.
		case xml.Directive:
			return nil, errors.New("DOCTYPE and other directives are not allowed")
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("unclosed <%s>", stack[len(stack)-1].Name)
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

func ParseString(s string) (*Node, error) { return Parse(strings.NewReader(s)) }
