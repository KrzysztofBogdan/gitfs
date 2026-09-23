package changes

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func CanonContent(root *xmltree.Node, s *schema.Schema) string {
	c := root.Clone()
	canon.Normalize(c, s)
	return xmltree.Print(c, 0)
}

func groupText(root *xmltree.Node, name string) string {
	var out string
	for _, c := range root.ChildrenNamed(name) {
		out += xmltree.Print(c, 0) + "\n"
	}
	return out
}

func subsByID(root *xmltree.Node, e *schema.Elem) (map[string]*xmltree.Node, []*xmltree.Node, []string) {
	byID := map[string]*xmltree.Node{}
	var fresh []*xmltree.Node
	var order []string
	for _, c := range root.ChildrenNamed(e.Name) {
		if id, ok := c.Attr(e.ID); ok {
			byID[id] = c
			order = append(order, id)
		} else {
			fresh = append(fresh, c)
		}
	}
	return byID, fresh, order
}

func ResolveActions(ad adapter.Adapter, base *xmltree.Node, local *envelope.Doc, basePath, path string) []adapter.Action {
	s := ad.Schema()
	res := &adapter.Resource{Path: path, Root: local.Content}
	var acts []adapter.Action
	switch {
	case base == nil:
		acts = []adapter.Action{{Verb: "create"}}
	case local.Action != "":
		acts = []adapter.Action{{Verb: local.Action, Params: local.Params}}
	default:
		b, l := base.Clone(), local.Content.Clone()
		canon.Normalize(b, s)
		canon.Normalize(l, s)
		var creates, updates, deletes []adapter.Action
		for i := range s.Elems {
			e := &s.Elems[i]
			if e.Kind != schema.Sub {
				if groupText(b, e.Name) != groupText(l, e.Name) {
					acts = append(acts, adapter.Action{Verb: "update", Group: e.Name})
				}
				continue
			}
			bs, _, border := subsByID(b, e)
			ls, fresh, _ := subsByID(l, e)
			for n := range fresh {
				creates = append(creates, adapter.Action{Verb: "create", Target: fmt.Sprintf("%s[%d]", e.Name, n+1)})
			}
			for _, id := range border {
				ln, ok := ls[id]
				switch {
				case !ok:
					deletes = append(deletes, adapter.Action{Verb: "delete", Target: fmt.Sprintf("%s[id=%s]", e.Name, id)})
				case xmltree.Print(ln, 0) != xmltree.Print(bs[id], 0):
					updates = append(updates, adapter.Action{Verb: "update", Target: fmt.Sprintf("%s[id=%s]", e.Name, id)})
				}
			}
		}
		acts = append(append(append(acts, creates...), updates...), deletes...)
		if adapter.IsMove(ad.PathModel(), basePath, path) {
			acts = append(acts, adapter.Action{Verb: "move", From: basePath, To: path})
		}
	}
	for i := range acts {
		ad.Describe(&acts[i], res)
	}
	return acts
}

var targetRe = regexp.MustCompile(`^([\w:.-]+)\[(?:id=([^\]]+)|(\d+))\]$`)

// ParseTarget splits "comment[id=7]" or "comment[2]".
func ParseTarget(t string) (name, id string, nth int, ok bool) {
	m := targetRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", 0, false
	}
	if m[3] != "" {
		nth, _ = strconv.Atoi(m[3])
	}
	return m[1], m[2], nth, true
}
