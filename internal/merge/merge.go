// Package merge implements the gfs three-way merge (spec section 8).
package merge

import (
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/textdiff"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// rootDepth is the depth of the resource root inside a canonical file.
const rootDepth = 2

type Result struct {
	Root            *xmltree.Node // merged root, nil when conflicted
	Text            string        // conflicted root with markers, printed at rootDepth
	Elements, Hunks int
}

func (r Result) Conflicted() bool { return r.Elements > 0 }

type grouping struct {
	keys  []string
	nodes map[string][]*xmltree.Node
}

func group(root *xmltree.Node, s *schema.Schema) grouping {
	g := grouping{nodes: map[string][]*xmltree.Node{}}
	newIdx := 0
	for _, c := range root.Elements() {
		key := c.Name
		if e := schema.Find(s.Elems, c.Name); e != nil && e.Kind == schema.Sub {
			if id, ok := c.Attr(e.ID); ok {
				key = c.Name + "\x00" + id
			} else {
				newIdx++
				key = c.Name + "\x00new\x00" + strconv.Itoa(newIdx)
			}
		}
		if _, seen := g.nodes[key]; !seen {
			g.keys = append(g.keys, key)
		}
		g.nodes[key] = append(g.nodes[key], c)
	}
	return g
}

func text(nodes []*xmltree.Node) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = xmltree.Print(n, rootDepth+1)
	}
	return strings.Join(parts, "\n")
}

func parseFragment(lines []string) ([]*xmltree.Node, error) {
	n, err := xmltree.ParseString("<fragment>" + strings.Join(lines, "\n") + "</fragment>")
	if err != nil {
		return nil, err
	}
	return n.Elements(), nil
}

func prep(n *xmltree.Node, s *schema.Schema) *xmltree.Node {
	c := n.Clone()
	canon.Normalize(c, s)
	return c
}

func Merge(base, local, remote *xmltree.Node, s *schema.Schema, remoteLabel string) (Result, error) {
	b, l, r := prep(base, s), prep(local, s), prep(remote, s)
	bg, lg, rg := group(b, s), group(l, s), group(r, s)
	var keys []string
	seen := map[string]bool{}
	for _, g := range []grouping{lg, rg, bg} {
		for _, k := range g.keys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	whole := map[string]bool{} // groups forced to a whole-group conflict
	for attempt := 0; ; attempt++ {
		out := &xmltree.Node{Kind: xmltree.Element, Name: r.Name, Attrs: append([]xmltree.Attr(nil), r.Attrs...)}
		raws := map[*xmltree.Node]*xmltree.Node{}
		var textual []string
		res := Result{}
		conflict := func(k string, lines []string, hunks int) {
			ph := firstOf(lg.nodes[k], rg.nodes[k]).Clone()
			out.Children = append(out.Children, ph)
			raws[ph] = &xmltree.Node{Kind: xmltree.Raw, Text: strings.Join(lines, "\n")}
			res.Elements++
			res.Hunks += hunks
		}
		for _, k := range keys {
			bt, lt, rt := text(bg.nodes[k]), text(lg.nodes[k]), text(rg.nodes[k])
			switch {
			case lt == bt:
				out.Children = append(out.Children, clones(rg.nodes[k])...)
			case rt == bt, lt == rt:
				out.Children = append(out.Children, clones(lg.nodes[k])...)
			default:
				bl, ll, rl := textdiff.Lines(bt), textdiff.Lines(lt), textdiff.Lines(rt)
				if !whole[k] {
					lines, n := textdiff.Render(textdiff.Merge3(bl, ll, rl), remoteLabel)
					if n > 0 {
						conflict(k, lines, n)
						continue
					}
					if nodes, err := parseFragment(lines); err == nil {
						out.Children = append(out.Children, nodes...)
						textual = append(textual, k)
						continue
					}
				}
				lines, n := textdiff.Render([]textdiff.Chunk{{Conflict: true, Local: ll, Base: bl, Remote: rl}}, remoteLabel)
				conflict(k, lines, n)
			}
		}
		canon.Normalize(out, s)
		if res.Conflicted() {
			for i, c := range out.Children {
				if raw, ok := raws[c]; ok {
					out.Children[i] = raw
				}
			}
			res.Text = xmltree.Print(out, rootDepth)
			return res, nil
		}
		err := validate.Resource(out, r, s)
		if err == nil {
			res.Root = out
			return res, nil
		}
		if len(textual) == 0 || attempt > 0 {
			return Result{}, err
		}
		for _, k := range textual {
			whole[k] = true
		}
	}
}

func firstOf(a, b []*xmltree.Node) *xmltree.Node {
	if len(a) > 0 {
		return a[0]
	}
	return b[0]
}

func clones(nodes []*xmltree.Node) []*xmltree.Node {
	out := make([]*xmltree.Node, len(nodes))
	for i, n := range nodes {
		out[i] = n.Clone()
	}
	return out
}
