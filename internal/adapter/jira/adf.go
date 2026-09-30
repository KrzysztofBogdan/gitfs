package jira

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// adfRaw holds, as JSON text, an ADF node gfs does not model.
const adfRaw = "adf-raw"

var markRank = func() map[string]int {
	m := map[string]int{}
	for i, t := range adfMarks {
		m[t] = i
	}
	return m
}()

// xmlSafe reports whether every rune of s may appear in XML 1.0.
func xmlSafe(s string) bool {
	for _, r := range s {
		if !(r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000) {
			return false
		}
	}
	return true
}

// xmlText replaces what XML 1.0 forbids (ANSI escapes in pasted logs, …)
// with U+FFFD, so a file always parses. Rich text keeps such runs exactly,
// as <adf-raw>.
func xmlText(s string) string {
	if xmlSafe(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if xmlSafe(string(r)) {
			return r
		}
		return '\uFFFD'
	}, s)
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], xmlText(attrs[i+1]))
		}
	}
	return n
}

func textEl(name, text string) *xmltree.Node {
	n := el(name)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(text)}}
	}
	return n
}

// adfToNodes renders the content of an ADF doc as XML nodes (jira spec §5.4).
func adfToNodes(doc json.RawMessage) ([]*xmltree.Node, error) {
	doc = bytes.TrimSpace(doc)
	if len(doc) == 0 || string(doc) == "null" {
		return nil, nil
	}
	var d struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("ADF: %w", err)
	}
	if d.Type != "doc" {
		return nil, fmt.Errorf("ADF: root node is %q, want doc", d.Type)
	}
	return adfContent(d.Content)
}

type adfMark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs,omitempty"`
}

// item is one content entry before its marks become wrappers.
type item struct {
	text  *string       // a text run, or
	node  *xmltree.Node // an element
	marks []adfMark     // in wrapper order
	key   string        // identity of marks, for merging runs
}

func adfContent(raws []json.RawMessage) ([]*xmltree.Node, error) {
	var items []item
	for _, raw := range raws {
		it, err := adfItem(raw)
		if err != nil {
			return nil, err
		}
		if n := len(items); n > 0 && it.text != nil && items[n-1].text != nil && items[n-1].key == it.key {
			joined := *items[n-1].text + *it.text
			items[n-1].text = &joined
			continue
		}
		items = append(items, it)
	}
	out := make([]*xmltree.Node, 0, len(items))
	for _, it := range items {
		n := it.node
		if it.text != nil {
			n = &xmltree.Node{Kind: xmltree.Text, Text: *it.text}
		}
		for i := len(it.marks) - 1; i >= 0; i-- {
			w, _ := markElement(it.marks[i]) // validated by adfItem
			w.Children = []*xmltree.Node{n}
			n = w
		}
		out = append(out, n)
	}
	return keepSpaces(out), nil
}

// keepSpaces turns whitespace-only text into <text> where the printer would
// drop it: next to elements, with no other text.
func keepSpaces(ns []*xmltree.Node) []*xmltree.Node {
	if !elementOnly(ns) {
		return ns
	}
	for i, n := range ns {
		if n.Kind == xmltree.Text {
			ns[i] = &xmltree.Node{Kind: xmltree.Element, Name: "text", Children: []*xmltree.Node{n}}
		}
	}
	return ns
}

// elementOnly mirrors the printer: elements and only whitespace text.
func elementOnly(ns []*xmltree.Node) bool {
	hasElem := false
	for _, n := range ns {
		switch n.Kind {
		case xmltree.Element:
			hasElem = true
		case xmltree.Text:
			if strings.TrimSpace(n.Text) != "" {
				return false
			}
		}
	}
	return hasElem
}

var nodeKeys = map[string]bool{"type": true, "attrs": true, "content": true, "marks": true, "text": true}

func adfItem(raw json.RawMessage) (item, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return item{}, fmt.Errorf("ADF: %w", err)
	}
	var typ string
	json.Unmarshal(m["type"], &typ)
	it, ok, err := modelled(typ, m)
	if err != nil {
		return item{}, err
	}
	if ok {
		return it, nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return item{}, fmt.Errorf("ADF: %w", err)
	}
	return item{node: &xmltree.Node{Kind: xmltree.Element, Name: adfRaw,
		Children: []*xmltree.Node{{Kind: xmltree.Text, Text: b.String()}}}}, nil
}

// modelled decodes a node the tables describe; ok is false when any part of
// it (key, node type, mark, attribute type) is outside them.
func modelled(typ string, m map[string]json.RawMessage) (item, bool, error) {
	for k := range m {
		if !nodeKeys[k] {
			return item{}, false, nil
		}
	}
	var marks []adfMark
	if raw, has := m["marks"]; has {
		if json.Unmarshal(raw, &marks) != nil {
			return item{}, false, nil
		}
		for _, mk := range marks {
			if _, known := markRank[mk.Type]; !known {
				return item{}, false, nil
			}
			if _, ok := markElement(mk); !ok {
				return item{}, false, nil
			}
		}
		sort.SliceStable(marks, func(i, j int) bool { return markRank[marks[i].Type] < markRank[marks[j].Type] })
	}
	key, _ := json.Marshal(marks)
	if typ == "text" {
		var s string
		if json.Unmarshal(m["text"], &s) != nil || m["attrs"] != nil || m["content"] != nil || !xmlSafe(s) {
			return item{}, false, nil // text XML cannot hold stays exact as <adf-raw>
		}
		return item{text: &s, marks: marks, key: string(key)}, true, nil
	}
	if !adfNodes[typ] || m["text"] != nil {
		return item{}, false, nil
	}
	n := el(typ)
	var attrs map[string]json.RawMessage
	if raw, has := m["attrs"]; has && json.Unmarshal(raw, &attrs) != nil {
		return item{}, false, nil
	}
	if !setAttrs(n, typ, attrs) {
		return item{}, false, nil
	}
	if raw, has := m["content"]; has {
		var kids []json.RawMessage
		if json.Unmarshal(raw, &kids) != nil {
			return item{}, false, nil
		}
		var err error
		if n.Children, err = adfContent(kids); err != nil {
			return item{}, false, err
		}
	}
	return item{node: n, marks: marks, key: string(key)}, true, nil
}

func markElement(mk adfMark) (*xmltree.Node, bool) {
	n := el(mk.Type)
	return n, setAttrs(n, mk.Type, mk.Attrs)
}

// setAttrs writes attrs as XML attributes in name order; false on a value
// whose JSON type the tables do not allow.
func setAttrs(n *xmltree.Node, owner string, attrs map[string]json.RawMessage) bool {
	names := make([]string, 0, len(attrs))
	for k := range attrs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v, keep, ok := attrToXML(owner, k, attrs[k])
		if !ok {
			return false
		}
		if keep {
			n.Attrs = append(n.Attrs, xmltree.Attr{Name: k, Value: v})
		}
	}
	return true
}

// attrToXML renders one attribute value; keep is false for null (dropped).
func attrToXML(owner, name string, raw json.RawMessage) (v string, keep, ok bool) {
	t := bytes.TrimSpace(raw)
	if string(t) == "null" {
		return "", false, true
	}
	switch kind := attrKinds[owner][name]; {
	case kind == kJSON:
		var b bytes.Buffer
		if json.Compact(&b, t) != nil {
			return "", false, false
		}
		return b.String(), true, true
	case len(t) > 0 && t[0] == '"':
		var s string
		if kind != kString || json.Unmarshal(t, &s) != nil || !xmlSafe(s) {
			return "", false, false
		}
		return s, true, true
	case kind == kNumber:
		if _, err := strconv.ParseFloat(string(t), 64); err != nil {
			return "", false, false
		}
		return string(t), true, true
	case kind == kBool && (string(t) == "true" || string(t) == "false"):
		return string(t), true, true
	}
	return "", false, false
}

// nodesToADF turns the children of a body element back into an ADF doc; no
// content gives nil, which clears the field.
func nodesToADF(children []*xmltree.Node) (any, error) {
	var blocks []*xmltree.Node
	for _, c := range children {
		if c.Kind == xmltree.Text {
			if strings.TrimSpace(c.Text) != "" {
				return nil, errors.New("text outside a block: wrap it in <paragraph>")
			}
			continue
		}
		blocks = append(blocks, c)
	}
	content, err := toADF(blocks, nil)
	if err != nil || len(content) == 0 {
		return nil, err
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}, nil
}

func toADF(ns []*xmltree.Node, marks []any) ([]any, error) {
	skipSpace := elementOnly(ns)
	var out []any
	for _, n := range ns {
		switch n.Kind {
		case xmltree.Text:
			if skipSpace && strings.TrimSpace(n.Text) == "" {
				continue
			}
			out = appendMerged(out, textNode(n.Text, marks))
		case xmltree.Element:
			switch {
			case n.Name == "text":
				out = appendMerged(out, textNode(n.TextContent(), marks))
			case n.Name == adfRaw:
				if len(marks) > 0 {
					return nil, fmt.Errorf("<%s> cannot be inside a mark", adfRaw)
				}
				var v any
				if err := json.Unmarshal([]byte(strings.TrimSpace(n.TextContent())), &v); err != nil {
					return nil, fmt.Errorf("<%s>: %w", adfRaw, err)
				}
				out = append(out, v)
			case hasRank(n.Name):
				m := map[string]any{"type": n.Name}
				attrs, err := attrsToADF(n.Name, n.Attrs)
				if err != nil {
					return nil, err
				}
				if len(attrs) > 0 {
					m["attrs"] = attrs
				}
				inner, err := toADF(n.Children, append(append([]any(nil), marks...), m))
				if err != nil {
					return nil, err
				}
				for _, x := range inner {
					out = appendMerged(out, x)
				}
			case adfNodes[n.Name]:
				node := map[string]any{"type": n.Name}
				attrs, err := attrsToADF(n.Name, n.Attrs)
				if err != nil {
					return nil, err
				}
				if len(attrs) > 0 {
					node["attrs"] = attrs
				}
				kids, err := toADF(n.Children, nil)
				if err != nil {
					return nil, err
				}
				if len(kids) > 0 {
					node["content"] = kids
				}
				if len(marks) > 0 {
					node["marks"] = append([]any(nil), marks...)
				}
				out = append(out, node)
			default:
				return nil, fmt.Errorf("unknown ADF element <%s>", n.Name)
			}
		}
	}
	return out, nil
}

func hasRank(name string) bool { _, ok := markRank[name]; return ok }

func textNode(s string, marks []any) map[string]any {
	t := map[string]any{"type": "text", "text": s}
	if len(marks) > 0 {
		t["marks"] = append([]any(nil), marks...)
	}
	return t
}

// appendMerged appends x, joining it to a preceding text node with the same marks.
func appendMerged(out []any, x any) []any {
	xm, ok := x.(map[string]any)
	if n := len(out); ok && n > 0 && xm["type"] == "text" {
		if prev, ok := out[n-1].(map[string]any); ok && prev["type"] == "text" {
			a, _ := json.Marshal(prev["marks"])
			b, _ := json.Marshal(xm["marks"])
			if bytes.Equal(a, b) {
				prev["text"] = prev["text"].(string) + xm["text"].(string)
				return out
			}
		}
	}
	return append(out, x)
}

func attrsToADF(owner string, attrs []xmltree.Attr) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range attrs {
		switch attrKinds[owner][a.Name] {
		case kNumber:
			if _, err := strconv.ParseFloat(a.Value, 64); err != nil {
				return nil, fmt.Errorf("<%s %s=%q>: want a number", owner, a.Name, a.Value)
			}
			out[a.Name] = json.Number(a.Value)
		case kBool:
			if a.Value != "true" && a.Value != "false" {
				return nil, fmt.Errorf("<%s %s=%q>: want true or false", owner, a.Name, a.Value)
			}
			out[a.Name] = a.Value == "true"
		case kJSON:
			if !json.Valid([]byte(a.Value)) {
				return nil, fmt.Errorf("<%s %s=%q>: want JSON", owner, a.Name, a.Value)
			}
			out[a.Name] = json.RawMessage(a.Value)
		default:
			out[a.Name] = a.Value
		}
	}
	return out, nil
}
