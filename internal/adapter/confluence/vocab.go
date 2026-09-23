package confluence

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Vocabulary is what Confluence is known to store unchanged, generated from
// the storage examples (storage reference spec §3.1).
type Vocabulary struct {
	Elements map[string][]string `json:"elements"` // element -> attribute names
	Macros   map[string][]string `json:"macros"`   // structured-macro ac:name -> parameter names
	ADF      map[string][]string `json:"adf"`      // adf-node type -> adf-attribute keys
	Dropped  map[string]string   `json:"dropped"`  // "elem" or "elem@attr" -> reason
}

//go:embed vocabulary.json
var vocabularyJSON []byte

var vocabulary = sync.OnceValue(func() *Vocabulary {
	v := &Vocabulary{}
	if err := json.Unmarshal(vocabularyJSON, v); err != nil {
		panic("confluence: bad embedded vocabulary.json: " + err.Error())
	}
	return v
})

// EmbeddedVocabulary is the vocabulary built into the binary.
func EmbeddedVocabulary() *Vocabulary { return vocabulary() }

type vocabBuilder struct {
	elems, macros, adf map[string]map[string]bool
}

func (b *vocabBuilder) add(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	if v != "\x00" {
		m[k][v] = true
	}
}

func (b *vocabBuilder) walk(n *xmltree.Node) {
	for _, c := range n.Children {
		if c.Kind != xmltree.Element {
			continue
		}
		b.add(b.elems, c.Name, "\x00")
		for _, a := range c.Attrs {
			if !ignoredAttrs[a.Name] {
				b.add(b.elems, c.Name, a.Name)
			}
		}
		switch c.Name {
		case "ac:structured-macro":
			name, _ := c.Attr("ac:name")
			b.add(b.macros, name, "\x00")
			for _, p := range c.ChildrenNamed("ac:parameter") {
				pn, _ := p.Attr("ac:name")
				b.add(b.macros, name, pn)
			}
		case "ac:adf-node":
			typ, _ := c.Attr("type")
			b.add(b.adf, typ, "\x00")
			for _, a := range c.ChildrenNamed("ac:adf-attribute") {
				k, _ := a.Attr("key")
				b.add(b.adf, typ, k)
			}
		}
		b.walk(c)
	}
}

func sortedSets(m map[string]map[string]bool) map[string][]string {
	out := map[string][]string{}
	for k, set := range m {
		vs := []string{}
		for v := range set {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		out[k] = vs
	}
	return out
}

// BuildVocabulary collects the vocabulary from the examples (their remote
// form when Confluence rewrote them) and marks what only rejected forms use.
func BuildVocabulary(ex, rejected []Example) (*Vocabulary, error) {
	b := &vocabBuilder{elems: map[string]map[string]bool{}, macros: map[string]map[string]bool{}, adf: map[string]map[string]bool{}}
	for _, e := range ex {
		for _, s := range []string{e.Body, e.Remote} {
			if s == "" {
				continue
			}
			n, err := parseStorage("fragment", s)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Key(), err)
			}
			b.walk(n)
		}
	}
	v := &Vocabulary{Elements: sortedSets(b.elems), Macros: sortedSets(b.macros), ADF: sortedSets(b.adf), Dropped: map[string]string{}}
	for _, e := range rejected {
		n, err := parseStorage("fragment", e.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Key(), err)
		}
		for _, p := range v.problems(n) {
			v.Dropped[p.key] = e.Comment
		}
	}
	return v, nil
}

// JSON is the committed form of the vocabulary.
func (v *Vocabulary) JSON() []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

type problem struct{ key, msg string }

// problems lists what in the children of n is outside the vocabulary.
func (v *Vocabulary) problems(n *xmltree.Node) []problem {
	var out []problem
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		for _, c := range n.Children {
			if c.Kind != xmltree.Element {
				continue
			}
			attrs, known := v.Elements[c.Name]
			if !known {
				out = append(out, problem{c.Name, fmt.Sprintf("<%s>: element not verified (gfs help confluence-storage)", c.Name)})
			} else {
				for _, a := range c.Attrs {
					if !ignoredAttrs[a.Name] && !slices.Contains(attrs, a.Name) {
						out = append(out, problem{c.Name + "@" + a.Name, fmt.Sprintf("<%s %s>: attribute not verified", c.Name, a.Name)})
					}
				}
			}
			switch c.Name {
			case "ac:structured-macro":
				name, _ := c.Attr("ac:name")
				params, ok := v.Macros[name]
				if !ok {
					out = append(out, problem{"macro:" + name, fmt.Sprintf("macro %q: not verified", name)})
					break
				}
				for _, p := range c.ChildrenNamed("ac:parameter") {
					if pn, _ := p.Attr("ac:name"); !slices.Contains(params, pn) {
						out = append(out, problem{"macro:" + name + "@" + pn, fmt.Sprintf("macro %q parameter %q: not verified", name, pn)})
					}
				}
			case "ac:adf-node":
				typ, _ := c.Attr("type")
				keys, ok := v.ADF[typ]
				if !ok {
					out = append(out, problem{"adf:" + typ, fmt.Sprintf("adf node %q: not verified", typ)})
					break
				}
				for _, a := range c.ChildrenNamed("ac:adf-attribute") {
					if k, _ := a.Attr("key"); !slices.Contains(keys, k) {
						out = append(out, problem{"adf:" + typ + "@" + k, fmt.Sprintf("adf node %q attribute %q: not verified", typ, k)})
					}
				}
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

// Lint reports what in a page's body and comments Confluence is not known to
// store unchanged (adapter.Linter).
func (*Adapter) Lint(root *xmltree.Node) []string { return vocabulary().Lint(root) }

// Lint checks the <body> and every <comment> of a page root.
func (v *Vocabulary) Lint(root *xmltree.Node) []string {
	if root == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	check := func(n *xmltree.Node) {
		if n == nil {
			return
		}
		for _, p := range v.problems(n) {
			msg := p.msg
			if reason, ok := v.Dropped[p.key]; ok {
				what, _, _ := strings.Cut(p.msg, ": ")
				msg = what + ": Confluence drops this; " + reason
			}
			if !seen[msg] {
				seen[msg] = true
				out = append(out, msg)
			}
		}
	}
	check(root.Child("body"))
	for _, c := range root.ChildrenNamed("comment") {
		check(c)
	}
	return out
}
