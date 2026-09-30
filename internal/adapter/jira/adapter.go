// Package jira mirrors Jira Cloud as XML files: every project a user sees
// (jira://), or the requests a customer raised on a service desk
// (jira+customer://).
package jira

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func init() { adapter.Register(&Adapter{}) }

// Adapter is agent mode: jira://<site>.
type Adapter struct{}

var (
	_ adapter.Normalizer = (*Adapter)(nil)
	_ adapter.Session    = (*session)(nil)
	_ adapter.Cacher     = (*session)(nil)
	_ adapter.Reporter   = (*session)(nil)
	_ adapter.Advisor    = (*session)(nil)
	_ adapter.Identified = (*session)(nil)
)

func (*Adapter) Name() string                 { return "jira" }
func (*Adapter) Schemes() []string            { return []string{"jira"} }
func (*Adapter) Schema() *schema.Schema       { return issueSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openSession(ctx, t)
}

func topDir(p string) string {
	dir, _, _ := strings.Cut(p, "/")
	return dir
}

// Describe says what an action will do and its policy class (jira spec §7.7).
func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	var root *xmltree.Node
	p := ""
	if local != nil {
		root, p = local.Root, local.Path
	}
	switch {
	case a.IsAttachment():
		switch a.Verb {
		case "create":
			a.Detail = "attach " + path.Base(a.File)
		case "delete":
			a.Detail = "delete attachment " + path.Base(a.File)
		default:
			a.Detail = a.Verb + " attachment (Jira attachments have no versions; delete and re-add)"
		}
	case a.Target != "":
		describeSub(a, root)
	case a.Verb == "create":
		typ := textOf(child(root, "type"))
		if typ == "" {
			typ = "issue"
		}
		a.Detail = fmt.Sprintf("create %s in %s", typ, strings.ToUpper(topDir(p)))
	case a.Verb == "delete":
		a.Detail = "delete issue"
	case a.Verb == "update" && a.Group == "status":
		a.Class, a.Detail = "transition", "transition to "+textOf(child(root, "status"))
	case a.Verb == "update" && a.Group == "field":
		a.Detail = "update custom fields"
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	default:
		a.Detail = a.Verb + " (not supported by jira)"
	}
}

func describeSub(a *adapter.Action, root *xmltree.Node) {
	name, id, nth, _ := changes.ParseTarget(a.Target)
	switch name + " " + a.Verb {
	case "comment create":
		c := nthNew(root, "comment", "id", nth)
		switch {
		case attrIs(c, "public", "true"):
			a.Class, a.Detail = "reply", "add public reply (emails the customer)"
		case attrIs(c, "internal", "true"):
			a.Class, a.Detail = "comment", "add internal comment"
		default:
			a.Class, a.Detail = "comment", "add comment"
		}
	case "comment update":
		a.Class, a.Detail = "comment", "edit comment "+id
	case "worklog create":
		a.Class, a.Detail = "worklog", "log "+textOf(child(nthNew(root, "worklog", "id", nth), "spent"))
	case "worklog update":
		a.Class, a.Detail = "worklog", "edit worklog "+id
	case "link create":
		l := nthNew(root, "link", "id", nth)
		typ, _ := attr(l, "type")
		a.Class, a.Detail = "link", fmt.Sprintf("link: %s %s", typ, textOf(l))
	case "link update":
		a.Detail = "edit link " + id + " (links cannot be edited; delete and add)"
	case "comment delete", "worklog delete", "link delete":
		a.Class, a.Detail = "delete", "delete "+name+" "+id
	default:
		a.Detail = a.Verb + " " + a.Target
	}
}

func attrIs(n *xmltree.Node, name, want string) bool {
	v, _ := attr(n, name)
	return v == want
}
