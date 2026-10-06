package bitbucket

import (
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var _ adapter.BaseDescriber = (*Adapter)(nil)

func (a *Adapter) Describe(act *adapter.Action, local *adapter.Resource) {
	a.DescribeBase(act, local, nil)
}

// DescribeBase classes and words one action (bitbucket spec §7.5, §7.6).
func (*Adapter) DescribeBase(a *adapter.Action, local, base *adapter.Resource) {
	a.Class = a.Verb
	var root, broot *xmltree.Node
	p := ""
	if local != nil {
		root, p = local.Root, local.Path
	}
	if base != nil {
		broot = base.Root
	}
	switch {
	case a.IsAttachment():
		a.Detail = "pipeline logs are read-only"
	case a.Target != "":
		describeComment(a, root, broot)
	case a.Verb == "create":
		repo, _, _ := strings.Cut(p, "/")
		a.Detail = fmt.Sprintf("create PR %s → %s in %s", attr(child(root, "source"), "branch"), destOf(root), repo)
	case a.Verb == "delete":
		a.Detail = "delete PR (refused: decline it instead)"
	case a.Group == "state":
		switch text(child(root, "state")) {
		case "merged":
			a.Verb, a.Class = "merge", "merge"
			st := child(root, "state")
			strategy := attr(st, "strategy")
			if strategy == "" {
				strategy = "default strategy"
			}
			a.Detail = fmt.Sprintf("merge %s → %s (%s)", attr(child(root, "source"), "branch"), destOf(root), strategy)
			if text(child(root, "close-source-branch")) == "true" {
				a.Detail += ", close source branch"
			}
		case "declined":
			a.Verb, a.Class = "decline", "decline"
			a.Detail = "decline PR " + attr(root, "number")
		default:
			a.Detail = "change state to " + text(child(root, "state"))
		}
	case a.Group == "my-review":
		a.Verb, a.Class = "review", "review"
		switch text(child(root, "my-review")) {
		case "approved":
			a.Detail = "approve"
		case "changes_requested":
			a.Detail = "request changes"
		default:
			a.Detail = "withdraw " + strings.ReplaceAll(text(child(broot, "my-review")), "_", " ")
			if broot == nil {
				a.Detail = "withdraw review"
			}
		}
	default:
		a.Detail = "update " + a.Group
	}
}

func destOf(root *xmltree.Node) string {
	if d := attr(child(root, "destination"), "branch"); d != "" {
		return d
	}
	return "main branch"
}

func describeComment(a *adapter.Action, root, broot *xmltree.Node) {
	name, id, nth, _ := changes.ParseTarget(a.Target)
	if name != "comment" {
		a.Detail = name + " is read-only"
		return
	}
	switch a.Verb {
	case "create":
		a.Class = "comment"
		c := nthNew(root, "comment", nth)
		switch {
		case attr(c, "parent") != "":
			a.Detail = "reply to comment " + attr(c, "parent")
		case attr(c, "path") != "":
			a.Detail = "comment on " + attr(c, "path") + ":" + attr(c, "line")
		default:
			a.Detail = "comment"
		}
	case "update":
		a.Class = "comment"
		l, b := findSub(root, "comment", id), findSub(broot, "comment", id)
		switch {
		case b != nil && text(l) == text(b) && attr(l, "resolved") != attr(b, "resolved") && attr(l, "resolved") == "true":
			a.Detail = "resolve comment " + id
		case b != nil && text(l) == text(b) && attr(l, "resolved") != attr(b, "resolved"):
			a.Detail = "reopen comment " + id
		default:
			a.Detail = "edit comment " + id
		}
	case "delete":
		a.Detail = "delete comment " + id
	}
}

func child(n *xmltree.Node, name string) *xmltree.Node {
	if n == nil {
		return nil
	}
	return n.Child(name)
}

// nthNew is the nth (1-based) name element without an id.
func nthNew(root *xmltree.Node, name string, nth int) *xmltree.Node {
	if root == nil {
		return nil
	}
	for _, c := range root.ChildrenNamed(name) {
		if _, ok := c.Attr("id"); !ok {
			if nth--; nth == 0 {
				return c
			}
		}
	}
	return nil
}

func findSub(root *xmltree.Node, name, id string) *xmltree.Node {
	if root == nil {
		return nil
	}
	for _, c := range root.ChildrenNamed(name) {
		if attr(c, "id") == id {
			return c
		}
	}
	return nil
}
