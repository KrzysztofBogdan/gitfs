package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// child is root's first child named name; nil-safe.
func child(root *xmltree.Node, name string) *xmltree.Node {
	if root == nil {
		return nil
	}
	return root.Child(name)
}

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.TextContent())
}

// elemName is how a field id is named in messages: its element.
func elemName(id string) string {
	if e, ok := systemElems[id]; ok {
		return "<" + e + ">"
	}
	return fmt.Sprintf("<field id=%q>", id)
}

// encoder turns file elements into Jira field values for one issue.
type encoder struct {
	s        *session
	issueKey string // "" on create
	project  string
}

// user is the value of a people element (jira spec §5.7): its account, or
// the account the text (an email or a unique display name) resolves to.
func (e encoder) user(ctx context.Context, n, base *xmltree.Node, assignable bool) (any, error) {
	if n == nil {
		return nil, nil
	}
	if acc, ok := n.Attr("account"); ok && acc != "" {
		if bacc, _ := attr(base, "account"); bacc == acc && textOf(base) != textOf(n) {
			return nil, fmt.Errorf("<%s>: to reassign, remove account=%q and write an email or a name", n.Name, acc)
		}
		return map[string]any{"accountId": acc}, nil
	}
	text := textOf(n)
	if text == "" {
		return nil, nil
	}
	acc, err := e.s.resolveUser(ctx, text, e.issueKey, e.project, assignable)
	if err != nil {
		return nil, fmt.Errorf("<%s>: %w", n.Name, err)
	}
	return map[string]any{"accountId": acc}, nil
}

func attr(n *xmltree.Node, name string) (string, bool) {
	if n == nil {
		return "", false
	}
	return n.Attr(name)
}

// resolveUser finds the account for an email or an exact display name.
func (s *session) resolveUser(ctx context.Context, text, issueKey, project string, assignable bool) (string, error) {
	if p, ok := s.reg.byEmail(text); ok {
		return p.Account, nil
	}
	path := "/rest/api/3/user/search?query=" + url.QueryEscape(text)
	if assignable {
		path = "/rest/api/3/user/assignable/search?query=" + url.QueryEscape(text)
		if issueKey != "" {
			path += "&issueKey=" + url.QueryEscape(issueKey)
		} else {
			path += "&project=" + url.QueryEscape(project)
		}
	}
	var us []apiUser
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &us); err != nil {
		return "", err
	}
	isEmail := strings.Contains(text, "@")
	var exact []apiUser
	for _, u := range us {
		if u.EmailAddress != "" && strings.EqualFold(u.EmailAddress, text) {
			s.reg.see(u)
			return u.AccountID, nil
		}
		if u.DisplayName == text {
			exact = append(exact, u)
		}
	}
	if len(exact) == 0 && isEmail && len(us) == 1 { // the email is hidden, but only one account matches it
		exact = us
	}
	switch len(exact) {
	case 1:
		s.reg.see(exact[0])
		return exact[0].AccountID, nil
	case 0:
		return "", fmt.Errorf("no user matches %q", text)
	}
	var cands []string
	for _, u := range exact {
		cands = append(cands, fmt.Sprintf("%s (%s)", u.DisplayName, u.AccountID))
	}
	sort.Strings(cands)
	return "", fmt.Errorf("%q matches %d users: %s; write account=\"…\" (see .people.xml)", text, len(exact), strings.Join(cands, ", "))
}

func items(n *xmltree.Node, item string) []string {
	out := []string{}
	if n == nil {
		return out
	}
	for _, c := range n.ChildrenNamed(item) {
		out = append(out, textOf(c))
	}
	return out
}

func named(texts []string) []any {
	out := []any{}
	for _, t := range texts {
		out = append(out, map[string]any{"name": t})
	}
	return out
}

// system is the value of a system field element; local and base are the
// issue roots (base nil on create).
func (e encoder) system(ctx context.Context, group string, local, base *xmltree.Node) (any, error) {
	n := child(local, group)
	switch group {
	case "summary":
		if textOf(n) == "" {
			return nil, fmt.Errorf("<summary> is required")
		}
		return textOf(n), nil
	case "priority", "resolution":
		if textOf(n) == "" {
			return nil, nil
		}
		return map[string]any{"name": textOf(n)}, nil
	case "assignee":
		return e.user(ctx, n, child(base, group), true)
	case "reporter":
		return e.user(ctx, n, child(base, group), false)
	case "parent":
		if textOf(n) == "" {
			return nil, nil
		}
		return map[string]any{"key": textOf(n)}, nil
	case "labels":
		return items(n, "label"), nil
	case "components":
		return named(items(n, "component")), nil
	case "fixVersions", "affectsVersions":
		return named(items(n, "version")), nil
	case "due":
		if textOf(n) == "" {
			return nil, nil
		}
		return textOf(n), nil
	case "timetracking":
		m := map[string]any{}
		if o := textOf(child(n, "original")); o != "" {
			m["originalEstimate"] = o
		}
		if r := textOf(child(n, "remaining")); r != "" {
			m["remainingEstimate"] = r
		}
		return m, nil
	case "environment", "description":
		if n == nil {
			return nil, nil
		}
		v, err := nodesToADF(n.Children)
		if err != nil {
			return nil, fmt.Errorf("<%s>: %w", group, err)
		}
		return v, nil
	case "type", "creator":
		return nil, fmt.Errorf("<%s> is read-only: Jira cannot change it in place", group)
	}
	return nil, fmt.Errorf("<%s> cannot be changed", group)
}

func optionValue(o *xmltree.Node) any {
	if o == nil {
		return nil
	}
	if id, ok := o.Attr("id"); ok && id != "" {
		return map[string]any{"id": id}
	}
	return map[string]any{"value": textOf(o)}
}

// field is the value of a <field> element (jira spec §5.3); n nil clears it.
func (e encoder) field(ctx context.Context, m fieldMeta, n, base *xmltree.Node) (any, error) {
	if n == nil {
		return nil, nil
	}
	label := fmt.Sprintf("<field id=%q>", m.ID)
	if t, _ := n.Attr("type"); t == "raw" || codecOf(m) == cRaw {
		return nil, fmt.Errorf("%s (%s) is read-only in gfs", label, m.Name)
	}
	text := textOf(n)
	switch codecOf(m) {
	case cText, cDate, cDateTime, cKey:
		if text == "" {
			return nil, nil
		}
		return text, nil
	case cNumber:
		if text == "" {
			return nil, nil
		}
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return nil, fmt.Errorf("%s: want a number, got %q", label, text)
		}
		return json.Number(text), nil
	case cADF:
		v, err := nodesToADF(n.Children)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		return v, nil
	case cOption:
		o := n.Child("option")
		if bo := child(base, "option"); o != nil && bo != nil {
			if id, _ := o.Attr("id"); id != "" {
				if bid, _ := bo.Attr("id"); bid == id && textOf(bo) != textOf(o) {
					return nil, fmt.Errorf("%s: to pick another option, remove id=%q and write its value", label, id)
				}
			}
		}
		return optionValue(o), nil
	case cOptions:
		out := []any{}
		for _, o := range n.ChildrenNamed("option") {
			out = append(out, optionValue(o))
		}
		return out, nil
	case cUser:
		return e.user(ctx, n.Child("user"), child(base, "user"), false)
	case cUsers:
		out := []any{}
		for _, u := range n.ChildrenNamed("user") {
			v, err := e.user(ctx, u, nil, false)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out = append(out, v)
			}
		}
		return out, nil
	case cLabels:
		return items(n, "label"), nil
	case cSprint:
		sps := n.ChildrenNamed("sprint")
		if len(sps) == 0 {
			return nil, nil
		}
		id, _ := sps[len(sps)-1].Attr("id")
		if _, err := strconv.Atoi(id); err != nil {
			return nil, fmt.Errorf("%s: <sprint> needs a numeric id", label)
		}
		return json.Number(id), nil
	}
	return nil, fmt.Errorf("%s (%s) is read-only in gfs", label, m.Name)
}
