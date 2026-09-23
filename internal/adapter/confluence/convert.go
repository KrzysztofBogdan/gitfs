package confluence

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiVersion struct {
	Number    int    `json:"number"`
	AuthorID  string `json:"authorId"`
	CreatedAt string `json:"createdAt"`
}

type apiBody struct {
	Storage struct {
		Value string `json:"value"`
	} `json:"storage"`
}

type apiPage struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	SpaceID   string     `json:"spaceId"`
	ParentID  string     `json:"parentId"`
	CreatedAt string     `json:"createdAt"`
	Version   apiVersion `json:"version"`
	Body      apiBody    `json:"body"`
}

type apiComment struct {
	ID      string     `json:"id"`
	Version apiVersion `json:"version"`
	Body    apiBody    `json:"body"`
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], attrs[i+1])
		}
	}
	return n
}

func textEl(name, text string) *xmltree.Node {
	n := el(name)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
	}
	return n
}

// parseStorage parses a storage-format fragment into the children of <elem>.
func parseStorage(elem, value string) (*xmltree.Node, error) {
	n, err := xmltree.ParseString(fmt.Sprintf(`<%s xmlns:ac=%q xmlns:ri=%q>%s</%s>`, elem, nsAC, nsRI, value, elem))
	if err != nil {
		return nil, fmt.Errorf("storage format: %w", err)
	}
	n.Attrs = nil
	return n, nil
}

var (
	textEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
		"\n", "&#10;", "\r", "&#13;", "\t", "&#9;")
)

// storageOf serialises the children of n as Confluence storage format.
func storageOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range n.Children {
		writeStorage(&b, c, n.Name)
	}
	return b.String()
}

func writeStorage(b *strings.Builder, n *xmltree.Node, parent string) {
	switch n.Kind {
	case xmltree.Text:
		if parent == "ac:plain-text-body" || parent == "ac:plain-text-link-body" {
			b.WriteString("<![CDATA[" + strings.ReplaceAll(n.Text, "]]>", "]]]]><![CDATA[>") + "]]>")
		} else {
			b.WriteString(textEsc.Replace(n.Text))
		}
	case xmltree.Comment:
		b.WriteString("<!--" + n.Text + "-->")
	case xmltree.Element:
		b.WriteString("<" + n.Name)
		for _, a := range n.Attrs {
			b.WriteString(" " + a.Name + `="` + attrEsc.Replace(a.Value) + `"`)
		}
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteByte('>')
		for _, c := range n.Children {
			writeStorage(b, c, n.Name)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

func pageNode(p apiPage, labels []string, comments []apiComment, name func(string) string) (*xmltree.Node, error) {
	root := el("page", "id", p.ID, "version", strconv.Itoa(p.Version.Number), "parent", p.ParentID,
		"created", p.CreatedAt, "updated", p.Version.CreatedAt)
	root.Children = append(root.Children, textEl("title", p.Title))
	if len(labels) > 0 {
		ls := el("labels")
		for _, l := range labels {
			ls.Children = append(ls.Children, textEl("label", l))
		}
		root.Children = append(root.Children, ls)
	}
	body, err := parseStorage("body", p.Body.Storage.Value)
	if err != nil {
		return nil, fmt.Errorf("page %s: %w", p.ID, err)
	}
	body.Attrs = []xmltree.Attr{{Name: "type", Value: bodyType}, {Name: "xmlns:ac", Value: nsAC}, {Name: "xmlns:ri", Value: nsRI}}
	root.Children = append(root.Children, body)
	for _, c := range comments {
		cn, err := parseStorage("comment", c.Body.Storage.Value)
		if err != nil {
			return nil, fmt.Errorf("comment %s: %w", c.ID, err)
		}
		author := name(c.Version.AuthorID)
		if author == "" {
			author = c.Version.AuthorID
		}
		cn.Attrs = el("", "id", c.ID, "author", author, "created", c.Version.CreatedAt,
			"version", strconv.Itoa(c.Version.Number)).Attrs
		root.Children = append(root.Children, cn)
	}
	return root, nil
}

func titleOf(root *xmltree.Node) string {
	if t := root.Child("title"); t != nil {
		return t.TextContent()
	}
	return ""
}

func labelsOf(root *xmltree.Node) []string {
	var out []string
	if ls := root.Child("labels"); ls != nil {
		for _, l := range ls.ChildrenNamed("label") {
			out = append(out, l.TextContent())
		}
	}
	return out
}
