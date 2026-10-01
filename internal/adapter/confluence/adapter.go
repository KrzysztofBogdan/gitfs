// Package confluence mirrors the spaces of a Confluence Cloud site as page trees of XML files.
package confluence

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

func init() { adapter.Register(&Adapter{}) }

type Adapter struct{}

func (*Adapter) Name() string                 { return "confluence" }
func (*Adapter) Schemes() []string            { return []string{"confluence"} }
func (*Adapter) Schema() *schema.Schema       { return pageSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Tree }
func (*Adapter) Verbs() []adapter.Verb        { return nil }

func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	if _, err := parseSelection(n.Query()); err != nil {
		return "", fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	return n.String(), nil
}

// DefaultDir is the space folder for a one-space selection, else the site name.
func (*Adapter) DefaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "confluence"
	}
	if sel, err := parseSelection(n.Query()); err == nil && len(sel.keys) == 1 {
		return strings.ToLower(sel.keys[0])
	}
	return strings.SplitN(n.Hostname(), ".", 2)[0]
}

// Login guides the user to an Atlassian API token (DNS spec §8.3).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	return atlassian.Login(ctx, n.Hostname(), io)
}

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openSession(ctx, t)
}

func baseName(p string) string { return strings.TrimSuffix(path.Base(p), ".xml") }

func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	switch {
	case a.IsAttachment():
		switch a.Verb {
		case "create":
			title := titleOf(local.Root)
			if title == "" {
				title = baseName(local.Path)
			}
			a.Detail = fmt.Sprintf("attach to %q", title)
		case "update":
			a.Detail = "upload new version of attachment"
		case "delete":
			a.Detail = "delete attachment"
		default:
			a.Detail = a.Verb + " attachment (not supported by confluence)"
		}
	case a.Target != "":
		_, id, _, _ := changes.ParseTarget(a.Target)
		switch a.Verb {
		case "create":
			a.Detail = "add comment"
		case "update":
			a.Detail = "edit comment " + id
		case "delete":
			a.Detail = "delete comment " + id
		}
	case a.Verb == "create":
		if parent := parentPath(local.Path); parent != "" {
			a.Detail = fmt.Sprintf("create page under %q", baseName(parent))
		} else {
			a.Detail = "create top-level page"
		}
	case a.Verb == "update" && a.Group == "title":
		a.Detail = fmt.Sprintf("rename page to %q", titleOf(local.Root))
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	case a.Verb == "move":
		if path.Dir(a.From) == path.Dir(a.To) {
			a.Detail = fmt.Sprintf("rename to %q", baseName(a.To))
		} else {
			a.Detail = fmt.Sprintf("move under %q", baseName(parentPath(a.To)))
		}
	case a.Verb == "delete":
		a.Detail = "delete page"
	default:
		a.Detail = a.Verb + " (not supported by confluence)"
	}
}
