package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type linkType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// nthNew is root's nth (1-based) child named name without idAttr: the
// sub-resource a "name[n]" create action means.
func nthNew(root *xmltree.Node, name, idAttr string, nth int) *xmltree.Node {
	if root == nil {
		return nil
	}
	i := 0
	for _, c := range root.ChildrenNamed(name) {
		if _, has := c.Attr(idAttr); has {
			continue
		}
		if i++; i == nth {
			return c
		}
	}
	return nil
}

func (s *session) loadLinkTypes(ctx context.Context) ([]linkType, error) {
	if s.linkTypes != nil {
		return s.linkTypes, nil
	}
	var resp struct {
		IssueLinkTypes []linkType `json:"issueLinkTypes"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issueLinkType", nil, &resp); err != nil {
		return nil, fmt.Errorf("link types: %w", err)
	}
	s.linkTypes = resp.IssueLinkTypes
	return s.linkTypes, nil
}

// linkBody is the request that makes "this <phrase> other". A link reads
// "inwardIssue <outward phrase> outwardIssue", the way Jira shows it on the
// inward issue's page (spec §11.4 checks the direction on a real site).
func (s *session) linkBody(ctx context.Context, this, phrase, other string) (map[string]any, error) {
	lts, err := s.loadLinkTypes(ctx)
	if err != nil {
		return nil, err
	}
	for _, lt := range lts {
		switch {
		case strings.EqualFold(lt.Outward, phrase):
			return map[string]any{"type": map[string]any{"name": lt.Name},
				"inwardIssue": map[string]any{"key": this}, "outwardIssue": map[string]any{"key": other}}, nil
		case strings.EqualFold(lt.Inward, phrase):
			return map[string]any{"type": map[string]any{"name": lt.Name},
				"inwardIssue": map[string]any{"key": other}, "outwardIssue": map[string]any{"key": this}}, nil
		}
	}
	var phrases []string
	for _, lt := range lts {
		phrases = append(phrases, fmt.Sprintf("%q", lt.Outward), fmt.Sprintf("%q", lt.Inward))
	}
	sort.Strings(phrases)
	return nil, fmt.Errorf("unknown link type %q; use one of: %s", phrase, strings.Join(phrases, ", "))
}

// visibility checks internal/public on a new comment (jira spec §5.5) and
// returns the comment properties to send.
func visibility(c *xmltree.Node, p *projectMeta) ([]any, error) {
	internal, _ := c.Attr("internal")
	public, _ := c.Attr("public")
	switch {
	case !p.JSM && (internal != "" || public != ""):
		return nil, fmt.Errorf("<comment>: internal and public are for service projects and %s is not one; remove the attribute", p.Key)
	case !p.JSM:
		return nil, nil
	case internal == "true" && public == "true":
		return nil, fmt.Errorf("<comment>: mark it internal or public, not both")
	case internal == "true":
		return []any{map[string]any{"key": "sd.public.comment", "value": map[string]any{"internal": true}}}, nil
	case public == "true":
		return nil, nil
	}
	return nil, fmt.Errorf(`%s is a service project: mark the comment internal="true" or public="true"`, p.Key)
}

func sameAttr(a, b *xmltree.Node, name string) bool {
	x, _ := attr(a, name)
	y, _ := attr(b, name)
	return x == y
}

func commentBody(c *xmltree.Node) (any, error) {
	body, err := nodesToADF(c.Children)
	if err != nil {
		return nil, fmt.Errorf("<comment>: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("<comment> is empty")
	}
	return body, nil
}

func worklogBody(w *xmltree.Node) (map[string]any, error) {
	started, spent := textOf(child(w, "started")), textOf(child(w, "spent"))
	if started == "" || spent == "" {
		return nil, fmt.Errorf("<worklog> needs <started> and <spent>")
	}
	body := map[string]any{"started": started, "timeSpent": spent}
	if c := child(w, "comment"); c != nil {
		v, err := nodesToADF(c.Children)
		if err != nil {
			return nil, fmt.Errorf("<worklog><comment>: %w", err)
		}
		if v != nil {
			body["comment"] = v
		}
	}
	return body, nil
}

// subPlan is one sub-resource request, built by planSub and sent by sub.
type subPlan struct {
	method, path string
	body         any
}

// planSub checks one comment, worklog or link action and builds its
// request (jira spec §7.6). It writes nothing.
func (s *session) planSub(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, a adapter.Action) (*subPlan, error) {
	name, id, nth, ok := changes.ParseTarget(a.Target)
	if !ok {
		return nil, fmt.Errorf("jira has no sub-resource %q", a.Target)
	}
	issue := "/rest/api/3/issue/" + url.PathEscape(ic.id)
	var local, base *xmltree.Node
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
	}
	switch name + " " + a.Verb {
	case "comment create":
		c := nthNew(local, "comment", "id", nth)
		if c == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		props, err := visibility(c, ic.p)
		if err != nil {
			return nil, err
		}
		body, err := commentBody(c)
		if err != nil {
			return nil, err
		}
		m := map[string]any{"body": body}
		if props != nil {
			m["properties"] = props
		}
		return &subPlan{http.MethodPost, issue + "/comment", m}, nil
	case "comment update":
		c, b := validate.FindSub(local, "comment", "id", id), validate.FindSub(base, "comment", "id", id)
		if c == nil || b == nil {
			return nil, fmt.Errorf("%s not found", a.Target)
		}
		if !sameAttr(c, b, "internal") || !sameAttr(c, b, "public") {
			return nil, fmt.Errorf("the visibility of comment %s cannot be changed; add a new comment instead", id)
		}
		body, err := commentBody(c)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPut, issue + "/comment/" + url.PathEscape(id), map[string]any{"body": body}}, nil
	case "comment delete":
		return &subPlan{http.MethodDelete, issue + "/comment/" + url.PathEscape(id), nil}, nil
	case "worklog create":
		w := nthNew(local, "worklog", "id", nth)
		if w == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		body, err := worklogBody(w)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPost, issue + "/worklog?adjustEstimate=auto", body}, nil
	case "worklog update":
		w := validate.FindSub(local, "worklog", "id", id)
		if w == nil {
			return nil, fmt.Errorf("%s not found", a.Target)
		}
		body, err := worklogBody(w)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPut, issue + "/worklog/" + url.PathEscape(id) + "?adjustEstimate=auto", body}, nil
	case "worklog delete":
		return &subPlan{http.MethodDelete, issue + "/worklog/" + url.PathEscape(id) + "?adjustEstimate=auto", nil}, nil
	case "link create":
		l := nthNew(local, "link", "id", nth)
		if l == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		phrase, _ := l.Attr("type")
		other := textOf(l)
		if phrase == "" || other == "" {
			return nil, fmt.Errorf(`<link> needs type="<phrase>" and the other issue's key, e.g. <link type="blocks">ABC-1</link>`)
		}
		body, err := s.linkBody(ctx, ic.key, phrase, other)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPost, "/rest/api/3/issueLink", body}, nil
	case "link update":
		return nil, fmt.Errorf("links cannot be edited; delete the <link> and add a new one")
	case "link delete":
		return &subPlan{http.MethodDelete, "/rest/api/3/issueLink/" + url.PathEscape(id), nil}, nil
	}
	return nil, fmt.Errorf("jira cannot %s %s", a.Verb, a.Target)
}

// sub runs one sub-resource action.
func (s *session) sub(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, a adapter.Action) error {
	p, err := s.planSub(ctx, ic, req, a)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, p.method, p.path, p.body, nil)
}
