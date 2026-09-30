package jira

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiIssue struct {
	ID     string                     `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type apiComment struct {
	ID        string          `json:"id"`
	Author    apiUser         `json:"author"`
	Body      json.RawMessage `json:"body"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
	JSDPublic *bool           `json:"jsdPublic"`
}

type apiWorklog struct {
	ID        string          `json:"id"`
	Author    apiUser         `json:"author"`
	Comment   json.RawMessage `json:"comment"`
	Started   string          `json:"started"`
	TimeSpent string          `json:"timeSpent"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
}

type issueRef struct {
	Key string `json:"key"`
}

type apiLink struct {
	ID   string `json:"id"`
	Type struct {
		Name    string `json:"name"`
		Inward  string `json:"inward"`
		Outward string `json:"outward"`
	} `json:"type"`
	InwardIssue  *issueRef `json:"inwardIssue"`
	OutwardIssue *issueRef `json:"outwardIssue"`
}

type apiAttachment struct {
	ID       string  `json:"id"`
	Filename string  `json:"filename"`
	MimeType string  `json:"mimeType"`
	Created  string  `json:"created"`
	Size     int64   `json:"size"`
	Author   apiUser `json:"author"`
}

// str reads a string field; "" when absent, null or not a string.
func (is apiIssue) str(id string) string {
	var s string
	json.Unmarshal(is.Fields[id], &s)
	return s
}

// named reads the "name" of an object field (priority, status, …).
func (is apiIssue) named(id string) string {
	var v struct {
		Name string `json:"name"`
	}
	json.Unmarshal(is.Fields[id], &v)
	return v.Name
}

// idOf reads the "id" of an object field (issuetype, project).
func (is apiIssue) idOf(id string) string {
	var v struct {
		ID string `json:"id"`
	}
	json.Unmarshal(is.Fields[id], &v)
	return v.ID
}

func (is apiIssue) project() (key, typeID string) {
	var p struct {
		Key string `json:"key"`
	}
	json.Unmarshal(is.Fields["project"], &p)
	return p.Key, is.idOf("issuetype")
}

// page reads an inline comment or worklog page: its items and total.
func page[T any](raw json.RawMessage, key string) ([]T, int, error) {
	if isEmptyJSON(raw) {
		return nil, 0, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, 0, err
	}
	var items []T
	if err := json.Unmarshal(m[key], &items); err != nil && m[key] != nil {
		return nil, 0, err
	}
	total := len(items)
	if t, ok := m["total"]; ok {
		json.Unmarshal(t, &total)
	}
	return items, total, nil
}

func sanitize(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "untitled"
	}
	if s[0] == '.' {
		s = "_" + s[1:]
	}
	if strings.HasSuffix(s, attach.SidecarSuffix) {
		s += "_" // a file name must never look like a sidecar folder
	}
	return s
}

// maxName keeps file names under the usual 255-byte limit, with room for
// ".xml", ".files" and conflict-copy suffixes.
const maxName = 200

// issuePath is <dir>/<KEY> <summary>.xml (jira spec §4); a long summary is
// cut at a character boundary.
func issuePath(dir, key, summary string) string {
	name := key
	if s := strings.TrimSpace(summary); s != "" {
		name += " " + s
	}
	if len(name) > maxName {
		i := maxName
		for i > 0 && !utf8.RuneStart(name[i]) {
			i--
		}
		name = name[:i]
	}
	return dir + "/" + sanitize(name) + ".xml"
}

// decoder turns issue JSON into an <issue> root (jira spec §5.1).
type decoder struct {
	reg *registry
	jsm bool // the project is a service project: comments carry internal/public
}

func (d decoder) user(name string, raw json.RawMessage) *xmltree.Node {
	var u apiUser
	if isEmptyJSON(raw) || json.Unmarshal(raw, &u) != nil || u.AccountID == "" {
		return nil
	}
	d.reg.see(u)
	return userNode(name, u)
}

func (d decoder) body(name string, raw json.RawMessage) (*xmltree.Node, error) {
	kids, err := adfToNodes(raw)
	if err != nil || len(kids) == 0 {
		return nil, err
	}
	d.reg.seeMentions(kids)
	n := el(name, "type", adfType)
	n.Children = kids
	return n, nil
}

func names(raw json.RawMessage, item string) []*xmltree.Node {
	var vs []struct {
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &vs)
	var out []*xmltree.Node
	for _, v := range vs {
		out = append(out, textEl(item, v.Name))
	}
	return out
}

func list(name string, items []*xmltree.Node) *xmltree.Node {
	if len(items) == 0 {
		return nil
	}
	n := el(name)
	n.Children = items
	return n
}

// issue decodes is. set is the issue type's field set; comments and worklogs
// are complete (topped up by the caller when search cut them short).
func (d decoder) issue(is apiIssue, set map[string]fieldMeta, comments []apiComment, worklogs []apiWorklog) (*xmltree.Node, error) {
	f := is.Fields
	root := el("issue", "id", is.ID, "key", is.Key, "created", is.str("created"), "updated", is.str("updated"),
		"resolved", is.str("resolutiondate"))
	add := func(n *xmltree.Node) {
		if n != nil {
			root.Children = append(root.Children, n)
		}
	}
	add(textEl("summary", is.str("summary")))
	add(textEl("type", is.named("issuetype")))
	add(textEl("status", is.named("status")))
	if r := is.named("resolution"); r != "" {
		add(textEl("resolution", r))
	}
	if p := is.named("priority"); p != "" {
		add(textEl("priority", p))
	}
	add(d.user("assignee", f["assignee"]))
	add(d.user("reporter", f["reporter"]))
	add(d.user("creator", f["creator"]))
	var parent issueRef
	if json.Unmarshal(f["parent"], &parent) == nil && parent.Key != "" {
		add(textEl("parent", parent.Key))
	}
	var labels []string
	json.Unmarshal(f["labels"], &labels)
	var ls []*xmltree.Node
	for _, l := range labels {
		ls = append(ls, textEl("label", l))
	}
	add(list("labels", ls))
	add(list("components", names(f["components"], "component")))
	add(list("fixVersions", names(f["fixVersions"], "version")))
	add(list("affectsVersions", names(f["versions"], "version")))
	if due := is.str("duedate"); due != "" {
		add(textEl("due", due))
	}
	var tt struct {
		Original  string `json:"originalEstimate"`
		Remaining string `json:"remainingEstimate"`
		Spent     string `json:"timeSpent"`
	}
	if json.Unmarshal(f["timetracking"], &tt) == nil && (tt.Original != "" || tt.Remaining != "" || tt.Spent != "") {
		n := el("timetracking", "spent", tt.Spent)
		if tt.Original != "" {
			n.Children = append(n.Children, textEl("original", tt.Original))
		}
		if tt.Remaining != "" {
			n.Children = append(n.Children, textEl("remaining", tt.Remaining))
		}
		add(n)
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, sys := systemElems[id]; sys {
			continue
		}
		n, err := decodeField(set[id], f[id], d.reg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", is.Key, err)
		}
		add(n)
	}
	for _, b := range []struct{ elem, field string }{{"environment", "environment"}, {"description", "description"}} {
		if _, shown := set[b.field]; !shown && b.field == "environment" {
			continue
		}
		n, err := d.body(b.elem, f[b.field])
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", is.Key, b.field, err)
		}
		add(n)
	}
	var links []apiLink
	json.Unmarshal(f["issuelinks"], &links)
	for _, l := range links {
		switch {
		case l.OutwardIssue != nil:
			add(textEl2("link", l.OutwardIssue.Key, "id", l.ID, "type", l.Type.Outward))
		case l.InwardIssue != nil:
			add(textEl2("link", l.InwardIssue.Key, "id", l.ID, "type", l.Type.Inward))
		}
	}
	var atts []apiAttachment
	json.Unmarshal(f["attachment"], &atts)
	for _, a := range atts {
		d.reg.see(a.Author)
		add(el("attachment", "id", a.ID, "name", a.Filename, "size", strconv.FormatInt(a.Size, 10), "mime", a.MimeType,
			"created", a.Created, "author", a.Author.DisplayName))
	}
	for _, c := range comments {
		n, err := d.comment(c)
		if err != nil {
			return nil, fmt.Errorf("%s comment %s: %w", is.Key, c.ID, err)
		}
		add(n)
	}
	for _, w := range worklogs {
		n, err := d.worklog(w)
		if err != nil {
			return nil, fmt.Errorf("%s worklog %s: %w", is.Key, w.ID, err)
		}
		add(n)
	}
	return root, nil
}

// textEl2 is an element with attributes and text.
func textEl2(name, text string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(text)}}
	}
	return n
}

func (d decoder) comment(c apiComment) (*xmltree.Node, error) {
	d.reg.see(c.Author)
	kids, err := adfToNodes(c.Body)
	if err != nil {
		return nil, err
	}
	d.reg.seeMentions(kids)
	n := el("comment", "id", c.ID, "account", c.Author.AccountID, "author", c.Author.DisplayName,
		"created", c.Created, "updated", c.Updated)
	if d.jsm && c.JSDPublic != nil {
		if *c.JSDPublic {
			n.SetAttr("public", "true")
		} else {
			n.SetAttr("internal", "true")
		}
	}
	n.Children = kids
	return n, nil
}

func (d decoder) worklog(w apiWorklog) (*xmltree.Node, error) {
	d.reg.see(w.Author)
	n := el("worklog", "id", w.ID, "account", w.Author.AccountID, "author", w.Author.DisplayName,
		"created", w.Created, "updated", w.Updated)
	n.Children = append(n.Children, textEl("started", w.Started), textEl("spent", w.TimeSpent))
	c, err := d.body("comment", w.Comment)
	if err != nil {
		return nil, err
	}
	if c != nil {
		n.Children = append(n.Children, c)
	}
	return n, nil
}
