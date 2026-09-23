package confluence

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Storage examples live in docs/confluence/examples/<node>/<variant>.xml
// (storage reference spec §2).
type Example struct {
	Node, Variant string
	Comment       string // the leading <!-- --> text
	Body          string // storage fragment without the comment
	Remote        string // what Confluence stores, when it differs from Body
}

func (e Example) Key() string { return e.Node + "/" + e.Variant }

const rejectedDir = "_rejected"

// LoadExamples reads every variant, sorted by node then variant. Forms under
// _rejected are not examples; LoadRejected reads them.
func LoadExamples(fsys fs.FS) ([]Example, error) {
	var out []Example
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == rejectedDir {
				return fs.SkipDir
			}
			return nil
		}
		node, variant, ok := splitExamplePath(p)
		if !ok {
			return nil
		}
		e, err := readExample(fsys, p)
		if err != nil {
			return err
		}
		e.Node, e.Variant = node, variant
		if r, err := fs.ReadFile(fsys, path.Join(node, variant+".remote.xml")); err == nil {
			e.Remote = strings.TrimSpace(string(r))
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, err
}

// LoadRejected reads the forms Confluence is known to drop or rewrite.
func LoadRejected(fsys fs.FS) ([]Example, error) {
	names, err := fs.Glob(fsys, rejectedDir+"/*.xml")
	if err != nil {
		return nil, err
	}
	var out []Example
	for _, p := range names {
		e, err := readExample(fsys, p)
		if err != nil {
			return nil, err
		}
		e.Node, e.Variant = rejectedDir, strings.TrimSuffix(path.Base(p), ".xml")
		out = append(out, e)
	}
	return out, nil
}

func splitExamplePath(p string) (node, variant string, ok bool) {
	node, file := path.Split(p)
	node = strings.TrimSuffix(node, "/")
	if node == "" || strings.Contains(node, "/") || !strings.HasSuffix(file, ".xml") || strings.HasSuffix(file, ".remote.xml") {
		return "", "", false
	}
	return node, strings.TrimSuffix(file, ".xml"), true
}

func readExample(fsys fs.FS, p string) (Example, error) {
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return Example{}, err
	}
	s := strings.TrimSpace(string(b))
	var e Example
	if strings.HasPrefix(s, "<!--") {
		if i := strings.Index(s, "-->"); i >= 0 {
			e.Comment, s = strings.TrimSpace(s[4:i]), strings.TrimSpace(s[i+3:])
		}
	}
	e.Body = s
	return e, nil
}

// Nodes returns the node names in order of first appearance.
func Nodes(ex []Example) []string {
	var out []string
	for _, e := range ex {
		if len(out) == 0 || out[len(out)-1] != e.Node {
			out = append(out, e.Node)
		}
	}
	return out
}

// ignoredAttrs are assigned by Confluence and never part of what we write.
var ignoredAttrs = map[string]bool{"ac:macro-id": true, "ac:local-id": true, "local-id": true, "data-local-id": true,
	"ri:version-at-save": true}

// canonicalFragment prints a storage fragment for comparison: service-owned
// attributes removed, attributes sorted, comments dropped, and macro
// parameters sorted by name (Confluence reorders them).
func canonicalFragment(storage string) (string, error) {
	n, err := parseStorage("fragment", storage)
	if err != nil {
		return "", err
	}
	scrub(n)
	return xmltree.PrintInner(n), nil
}

func scrub(n *xmltree.Node) {
	n.Attrs = slices.DeleteFunc(n.Attrs, func(a xmltree.Attr) bool { return ignoredAttrs[a.Name] })
	sort.SliceStable(n.Attrs, func(i, j int) bool { return n.Attrs[i].Name < n.Attrs[j].Name })
	n.Children = slices.DeleteFunc(n.Children, func(c *xmltree.Node) bool { return c.Kind == xmltree.Comment })
	if n.Name == "ac:structured-macro" {
		sort.SliceStable(n.Children, func(i, j int) bool { return paramKey(n.Children[i]) < paramKey(n.Children[j]) })
	}
	for _, c := range n.Children {
		if c.Kind == xmltree.Element {
			scrub(c)
		}
	}
}

// paramKey orders ac:parameter children by name and leaves everything else after them, in place.
func paramKey(n *xmltree.Node) string {
	if n.Kind == xmltree.Element && n.Name == "ac:parameter" {
		name, _ := n.Attr("ac:name")
		return "0" + name
	}
	return "1"
}

const markerPrefix = "gfs:"

// markerPage stacks variants of one node, each after <h6>gfs:<variant></h6>.
func markerPage(ex []Example) string {
	var b strings.Builder
	for _, e := range ex {
		b.WriteString("<h6>" + markerPrefix + e.Variant + "</h6>" + e.Body)
	}
	return b.String()
}

// splitMarkers cuts a stored marker page back into variant fragments.
func splitMarkers(storage string) (map[string]string, error) {
	n, err := parseStorage("fragment", storage)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var cur string
	var seg *xmltree.Node
	flush := func() {
		if seg != nil {
			out[cur] = storageOf(seg)
		}
	}
	for _, c := range n.Children {
		if c.Kind == xmltree.Element && c.Name == "h6" && strings.HasPrefix(c.TextContent(), markerPrefix) {
			flush()
			cur, seg = strings.TrimPrefix(c.TextContent(), markerPrefix), &xmltree.Node{Kind: xmltree.Element, Name: "fragment"}
			continue
		}
		if seg != nil {
			seg.Children = append(seg.Children, c)
		}
	}
	flush()
	return out, nil
}

// prettyFragment indents a fragment for reading; unparsable input is returned as is.
func prettyFragment(storage string) string {
	n, err := parseStorage("fragment", storage)
	if err != nil {
		return storage
	}
	return xmltree.PrintInner(n)
}

// RenderCatalogue renders docs/confluence/storage.md from the examples, the
// rejected forms and the round-trip status ("same" or "changed" by key).
func RenderCatalogue(ex, rejected []Example, status map[string]string) string {
	var b strings.Builder
	b.WriteString(`# Confluence storage reference

Generated from ` + "`docs/confluence/examples`" + ` by ` + "`TestStorageCatalogue -update`" + `; do not edit by hand.

Each variant is a body fragment in Confluence storage format, as it sits inside ` + "`<body>`" + `.
Status comes from the last live round trip (` + "`TestStorageExamples`" + `):

* **same**: Confluence stores the fragment unchanged, ignoring ` + "`ac:macro-id`" + `, local ids, ` + "`ri:version-at-save`" + ` and the order of macro parameters. Safe to write.
* **changed**: Confluence rewrites it; the stored form is shown. Write the stored form to avoid a diff on the next pull.
* **untested**: not part of the last round trip.

Print one fragment with ` + "`gfs example confluence <node>/<variant>`" + `; validate a page with
` + "`gfs schema confluence > storage.rng && xmllint --noout --relaxng storage.rng <page.xml>`" + `.

## Contents

`)
	nodes := Nodes(ex)
	for _, node := range nodes {
		var vs []string
		for _, e := range ex {
			if e.Node == node {
				vs = append(vs, e.Variant)
			}
		}
		fmt.Fprintf(&b, "* [%s](#%s): %s\n", node, strings.ToLower(node), strings.Join(vs, ", "))
	}
	if len(rejected) > 0 {
		b.WriteString("* [Rejected forms](#rejected-forms)\n")
	}
	for _, node := range nodes {
		fmt.Fprintf(&b, "\n## %s\n", node)
		for _, e := range ex {
			if e.Node != node {
				continue
			}
			st := status[e.Key()]
			if st == "" {
				st = "untested"
			}
			fmt.Fprintf(&b, "\n### %s (%s)\n\n", e.Variant, st)
			if e.Comment != "" {
				b.WriteString(e.Comment + "\n\n")
			}
			b.WriteString("```xml\n" + prettyFragment(e.Body) + "\n```\n")
			if st == "changed" && e.Remote != "" {
				b.WriteString("\nConfluence stores:\n\n```xml\n" + prettyFragment(e.Remote) + "\n```\n")
			}
		}
	}
	if len(rejected) > 0 {
		b.WriteString("\n## Rejected forms\n\nConfluence drops or rewrites these; do not write them.\n")
		for _, e := range rejected {
			fmt.Fprintf(&b, "\n### %s\n\n", e.Variant)
			if e.Comment != "" {
				b.WriteString(e.Comment + "\n\n")
			}
			b.WriteString("```xml\n" + prettyFragment(e.Body) + "\n```\n")
		}
	}
	return b.String()
}
