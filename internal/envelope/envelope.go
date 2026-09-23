// Package envelope reads and writes the <gfs> document around a resource.
package envelope

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const Decl = `<?xml version="1.0" encoding="UTF-8"?>`

// Footer closes every canonical document.
const Footer = "  </content>\n</gfs>\n"

type Error struct{ Action, Target, Code, At, Msg string }

type Conflict struct {
	RemoteVersion, By, At string
	Elements, Hunks       int
}

type Doc struct {
	Action   string
	Params   map[string]string
	Errors   []Error
	Conflict *Conflict
	Content  *xmltree.Node // the resource root
	Wrapped  bool          // input was a bare resource root
}

func New(root *xmltree.Node) *Doc { return &Doc{Content: root, Params: map[string]string{}} }

func Parse(data []byte) (*Doc, error) {
	root, err := xmltree.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if root.Name != "gfs" {
		d := New(root)
		d.Wrapped = true
		return d, nil
	}
	d := New(nil)
	for _, a := range root.Attrs {
		if a.Name == "action" {
			d.Action = a.Value
		} else {
			d.Params[a.Name] = a.Value
		}
	}
	for _, c := range root.Elements() {
		switch c.Name {
		case "content":
			if d.Content != nil {
				return nil, errors.New("more than one <content>")
			}
			els := c.Elements()
			if len(els) != 1 || strings.TrimSpace(c.TextContent()) != "" {
				return nil, errors.New("<content> must hold exactly one resource element")
			}
			d.Content = els[0]
		case "errors":
			for _, e := range c.ChildrenNamed("error") {
				er := Error{Msg: textOf(e.Child("msg"))}
				er.Action, _ = e.Attr("action")
				er.Target, _ = e.Attr("target")
				er.Code, _ = e.Attr("code")
				er.At, _ = e.Attr("at")
				d.Errors = append(d.Errors, er)
			}
		case "conflict":
			cf := &Conflict{}
			cf.RemoteVersion, _ = c.Attr("remote-version")
			cf.By, _ = c.Attr("by")
			cf.At, _ = c.Attr("at")
			v, _ := c.Attr("elements")
			cf.Elements, _ = strconv.Atoi(v)
			v, _ = c.Attr("hunks")
			cf.Hunks, _ = strconv.Atoi(v)
			d.Conflict = cf
		default:
			return nil, fmt.Errorf("unknown envelope element <%s>", c.Name)
		}
	}
	if d.Content == nil {
		return nil, errors.New("missing <content>")
	}
	return d, nil
}

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return n.TextContent()
}

func (d *Doc) Bare() *Doc {
	return &Doc{Content: d.Content, Params: map[string]string{}, Wrapped: d.Wrapped}
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.Attrs = append(n.Attrs, xmltree.Attr{Name: attrs[i], Value: attrs[i+1]})
		}
	}
	return n
}

func envelopeNode(d *Doc) *xmltree.Node {
	g := el("gfs", "action", d.Action)
	keys := make([]string, 0, len(d.Params))
	for k := range d.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g.Attrs = append(g.Attrs, xmltree.Attr{Name: k, Value: d.Params[k]})
	}
	if len(d.Errors) > 0 {
		es := el("errors")
		for _, e := range d.Errors {
			en := el("error", "action", e.Action, "target", e.Target, "code", e.Code, "at", e.At)
			m := el("msg")
			m.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: e.Msg}}
			en.Children = []*xmltree.Node{m}
			es.Children = append(es.Children, en)
		}
		g.Children = append(g.Children, es)
	}
	if c := d.Conflict; c != nil {
		g.Children = append(g.Children, el("conflict", "remote-version", c.RemoteVersion, "by", c.By, "at", c.At,
			"elements", strconv.Itoa(c.Elements), "hunks", strconv.Itoa(c.Hunks)))
	}
	return g
}

// Header returns the canonical text up to and including the "<content>" line.
func Header(d *Doc) string {
	g := envelopeNode(d)
	g.Children = append(g.Children, el("content"))
	s := xmltree.Print(g, 0) // "...\n  <content/>\n</gfs>"
	s = strings.TrimSuffix(s, "  <content/>\n</gfs>")
	return Decl + "\n" + s + "  <content>\n"
}

// Bytes renders d in canonical form. s may be nil (no schema normalisation).
func Bytes(d *Doc, s *schema.Schema) []byte {
	if s != nil {
		canon.Normalize(d.Content, s)
	}
	return []byte(Header(d) + xmltree.Print(d.Content, 2) + "\n" + Footer)
}

func HasMarkers(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if line == "=======" || strings.HasPrefix(line, "<<<<<<< ") ||
			strings.HasPrefix(line, "||||||| ") || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
}

func HasConflictElement(data []byte) bool {
	s := string(data)
	if i := strings.Index(s, "<content>"); i >= 0 {
		s = s[:i]
	}
	return strings.Contains(s, "<conflict ") || strings.Contains(s, "<conflict/>")
}
