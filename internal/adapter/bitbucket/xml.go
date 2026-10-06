package bitbucket

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// el is an element with the non-empty attributes of attrs (name, value pairs).
func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], xmlText(attrs[i+1]))
		}
	}
	return n
}

func withText(n *xmltree.Node, text string) *xmltree.Node {
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(text)}}
	}
	return n
}

func add(root *xmltree.Node, kids ...*xmltree.Node) {
	for _, k := range kids {
		if k != nil {
			root.Children = append(root.Children, k)
		}
	}
}

func attr(n *xmltree.Node, name string) string {
	if n == nil {
		return ""
	}
	v, _ := n.Attr(name)
	return v
}

func text(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return n.TextContent()
}

// xmlText replaces what XML cannot hold (and invalid UTF-8) with U+FFFD.
func xmlText(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return !xmlChar(r) }) {
		return s
	}
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "�") {
		if !xmlChar(r) {
			r = '�'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func xmlChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// logText is a log as a file holds it inline (bitbucket spec §5.5): \r\n and
// lone \r as \n, ANSI escapes removed.
func logText(b []byte) string {
	s := ansiRe.ReplaceAllString(string(b), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return xmlText(strings.ReplaceAll(s, "\r", "\n"))
}

// maxName keeps file names under the usual 255-byte limit, with room for
// ".xml", ".files" and conflict-copy suffixes.
const maxName = 200

// sanitize makes a title safe as a file name, as Jira does for summaries.
func sanitize(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "untitled"
	}
	if s[0] == '.' {
		s = "_" + s[1:]
	}
	if strings.HasSuffix(s, attach.SidecarSuffix) {
		s += "_" // a file name must never look like a sidecar folder
	}
	return s
}

func cut(name string) string {
	if len(name) <= maxName {
		return name
	}
	i := maxName
	for i > 0 && !utf8.RuneStart(name[i]) {
		i--
	}
	return name[:i]
}
