package xmltree

import "strings"

type shape int

const (
	empty shape = iota
	textOnly
	elementOnly
	mixed
)

func shapeOf(n *Node) shape {
	if len(n.Children) == 0 {
		return empty
	}
	allText, wsOnly := true, true
	for _, c := range n.Children {
		if c.Kind != Text {
			allText = false
		} else if strings.TrimSpace(c.Text) != "" {
			wsOnly = false
		}
	}
	switch {
	case allText:
		return textOnly
	case wsOnly:
		return elementOnly
	default:
		return mixed
	}
}

var attrEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	"\n", "&#10;", "\r", "&#13;", "\t", "&#9;")

func startTag(b *strings.Builder, n *Node) {
	b.WriteByte('<')
	b.WriteString(n.Name)
	for _, a := range n.Attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		b.WriteString(attrEsc.Replace(a.Value))
		b.WriteByte('"')
	}
}

func writeText(b *strings.Builder, s string) {
	if strings.ContainsAny(s, "<&") {
		b.WriteString("<![CDATA[")
		b.WriteString(strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>"))
		b.WriteString("]]>")
		return
	}
	b.WriteString(s)
}

// Print formats element n at the given depth, without a trailing newline.
func Print(n *Node, depth int) string {
	var b strings.Builder
	writeBlock(&b, n, depth)
	return b.String()
}

func writeBlock(b *strings.Builder, n *Node, depth int) {
	ind := strings.Repeat("  ", depth)
	switch n.Kind {
	case Raw:
		b.WriteString(n.Text)
		return
	case Comment:
		b.WriteString(ind + "<!--" + n.Text + "-->")
		return
	case Text:
		b.WriteString(ind)
		writeText(b, n.Text)
		return
	}
	b.WriteString(ind)
	startTag(b, n)
	switch shapeOf(n) {
	case empty:
		b.WriteString("/>")
	case elementOnly:
		b.WriteString(">\n")
		for _, c := range n.Children {
			if c.Kind == Text {
				continue
			}
			writeBlock(b, c, depth+1)
			b.WriteByte('\n')
		}
		b.WriteString(ind + "</" + n.Name + ">")
	default: // textOnly, mixed
		b.WriteByte('>')
		for _, c := range n.Children {
			writeInline(b, c)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

func writeInline(b *strings.Builder, n *Node) {
	switch n.Kind {
	case Text:
		writeText(b, n.Text)
	case Comment:
		b.WriteString("<!--" + n.Text + "-->")
	case Raw:
		b.WriteString(n.Text)
	case Element:
		startTag(b, n)
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteByte('>')
		for _, c := range n.Children {
			writeInline(b, c)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

// PrintInner formats only the children of n, as if n were printed at depth -1.
func PrintInner(n *Node) string {
	var b strings.Builder
	switch shapeOf(n) {
	case empty:
	case elementOnly:
		first := true
		for _, c := range n.Children {
			if c.Kind == Text {
				continue
			}
			if !first {
				b.WriteByte('\n')
			}
			first = false
			writeBlock(&b, c, 0)
		}
	default:
		for _, c := range n.Children {
			writeInline(&b, c)
		}
	}
	return b.String()
}
