package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// movePlan is how one issue moves to another project (jira move spec §5.2).
type movePlan struct {
	noop                             bool // the issue is in the target project already
	id, key                          string
	from, to                         *projectMeta
	typ                              *typeMeta
	statusFrom, statusTo, statusName string // statusTo "": Jira infers
	notes, losses                    []string
	adjusted                         *xmltree.Node // the local file as the target can hold it
}

// moveIndex is the index of the file's move action, -1 if none.
func moveIndex(acts []adapter.Action) int {
	for i, a := range acts {
		if a.Verb == "move" && a.Target == "" && !a.IsAttachment() {
			return i
		}
	}
	return -1
}

// class is delete when the move cannot keep the file's type or status, or
// drops a value (jira move spec §2).
func (m *movePlan) class() string {
	if !m.noop && len(m.notes)+len(m.losses) > 0 {
		return "delete"
	}
	return "move"
}

func (m *movePlan) detail() string {
	if m.noop {
		return "already in " + m.to.Key
	}
	status := m.statusName
	if m.statusTo == "" {
		status = m.to.Key + "'s default"
	}
	return fmt.Sprintf("move to %s as %s, status %s", m.to.Key, m.typ.Name, status)
}

func (m *movePlan) warning() string {
	if m.noop {
		return ""
	}
	parts := append([]string(nil), m.notes...)
	if len(m.losses) > 0 {
		parts = append(parts, "drops "+strings.Join(m.losses, ", "))
	}
	if m.to.JSM {
		parts = append(parts, "will not appear in the customer portal: Request Type is empty")
	}
	return strings.Join(parts, "; ")
}

// moveField is a target field as createmeta reports it, with its values.
type moveField struct {
	ID, Name             string
	Required, HasDefault bool
	Allowed              []allowedValue
}

type allowedValue struct{ ID, Name, Value string }

// allows reports whether the field takes the option or named value; a field
// without a list (labels, text) takes anything.
func (f moveField) allows(id, text string) bool {
	if len(f.Allowed) == 0 {
		return true
	}
	for _, v := range f.Allowed {
		if (id != "" && v.ID == id) || strings.EqualFold(v.Name, text) || strings.EqualFold(v.Value, text) {
			return true
		}
	}
	return false
}

// moveFields reads the target type's create screen with allowed values;
// not cached: values change and a move is rare.
func (s *session) moveFields(ctx context.Context, key, typeID string) (map[string]moveField, error) {
	out := map[string]moveField{}
	for start := 0; ; {
		var resp struct {
			Fields []struct {
				FieldID    string         `json:"fieldId"`
				Name       string         `json:"name"`
				Required   bool           `json:"required"`
				HasDefault bool           `json:"hasDefaultValue"`
				Allowed    []allowedValue `json:"allowedValues"`
			} `json:"fields"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/createmeta/%s/issuetypes/%s?startAt=%d&maxResults=200", url.PathEscape(key), url.PathEscape(typeID), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, fmt.Errorf("fields of %s type %s: %w", key, typeID, err)
		}
		for _, f := range resp.Fields {
			out[f.FieldID] = moveField{ID: f.FieldID, Name: f.Name, Required: f.Required, HasDefault: f.HasDefault, Allowed: f.Allowed}
		}
		start += len(resp.Fields)
		if len(resp.Fields) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

type apiStatus struct{ ID, Name string }

// typeStatuses is the workflow statuses of key's issue type typeID.
func (s *session) typeStatuses(ctx context.Context, key, typeID string) ([]apiStatus, error) {
	var resp []struct {
		ID       string      `json:"id"`
		Statuses []apiStatus `json:"statuses"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(key)+"/statuses", nil, &resp); err != nil {
		return nil, fmt.Errorf("statuses of %s: %w", key, err)
	}
	for _, t := range resp {
		if t.ID == typeID {
			return t.Statuses, nil
		}
	}
	return nil, nil
}

// moveState is what planMove needs of the issue as Jira holds it now.
type moveState struct {
	Key    string `json:"key"`
	Fields struct {
		Project   struct{ ID, Key string } `json:"project"`
		Issuetype struct {
			ID, Name string
			Subtask  bool
		} `json:"issuetype"`
		Status struct{ ID, Name string } `json:"status"`
		Parent *struct{ Key string }     `json:"parent"`
	} `json:"fields"`
}

// planMove works out how req's issue moves to the project of its new folder
// (jira move spec §5.2). It writes nothing.
func (s *session) planMove(ctx context.Context, req adapter.ApplyRequest) (*movePlan, error) {
	from, err := s.projectFor(req.Base.Path)
	if err != nil {
		return nil, err
	}
	to, err := s.projectFor(req.Local.Path)
	if err != nil {
		return nil, err
	}
	var cur moveState
	path := "/rest/api/3/issue/" + url.PathEscape(req.Base.ID) + "?fields=project,issuetype,status,parent"
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &cur); err != nil {
		return nil, err
	}
	local, base := req.Local.Root, req.Base.Root
	m := &movePlan{id: req.Base.ID, key: cur.Key, from: from, to: to, statusFrom: cur.Fields.Status.ID, adjusted: local.Clone()}
	if strings.EqualFold(cur.Fields.Project.Key, to.Key) {
		m.noop = true
		return m, nil
	}
	if cur.Fields.Issuetype.Subtask {
		parent := ""
		if cur.Fields.Parent != nil {
			parent = cur.Fields.Parent.Key
		}
		return nil, fmt.Errorf("sub-tasks move with their parent: move %s instead", parent)
	}
	if to.Types == nil {
		if err := s.loadTypes(ctx, to); err != nil {
			return nil, err
		}
	}
	if err := m.chooseType(textOf(child(local, "type")), textOf(child(base, "type"))); err != nil {
		return nil, err
	}
	statuses, err := s.typeStatuses(ctx, to.Key, m.typ.ID)
	if err != nil {
		return nil, err
	}
	if err := m.chooseStatus(statuses, textOf(child(local, "status")), textOf(child(base, "status"))); err != nil {
		return nil, err
	}
	fields, err := s.moveFields(ctx, to.Key, m.typ.ID)
	if err != nil {
		return nil, err
	}
	m.drop(fields)
	if err := m.required(fields); err != nil {
		return nil, err
	}
	putText(m.adjusted, "type", m.typ.Name)
	if m.statusTo != "" {
		putText(m.adjusted, "status", m.statusName)
	}
	return m, nil
}

// chooseType picks the target type: the file's if the target has it, else
// (type not edited) Task or the standard type with the lowest id.
func (m *movePlan) chooseType(want, was string) error {
	if t := m.to.typeNamed(want); t != nil && !t.Subtask {
		m.typ = t
		return nil
	}
	var std []*typeMeta
	for _, t := range m.to.Types {
		if !t.Subtask {
			std = append(std, t)
		}
	}
	sort.Slice(std, func(i, j int) bool {
		a, b := std[i].ID, std[j].ID
		return len(a) < len(b) || (len(a) == len(b) && a < b)
	})
	if !strings.EqualFold(want, was) || len(std) == 0 {
		var names []string
		for _, t := range std {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		return fmt.Errorf("%s has no issue type %q (types: %s)", m.to.Key, want, strings.Join(names, ", "))
	}
	m.typ = std[0]
	if t := m.to.typeNamed("Task"); t != nil && !t.Subtask {
		m.typ = t
	}
	m.notes = append(m.notes, fmt.Sprintf("type %s -> %s (%s has no %s)", want, m.typ.Name, m.to.Key, want))
	return nil
}

// chooseStatus maps to the file's status when the target workflow has it,
// else (status not edited) lets Jira pick the default.
func (m *movePlan) chooseStatus(sts []apiStatus, want, was string) error {
	var names []string
	for _, st := range sts {
		if strings.EqualFold(st.Name, want) {
			m.statusTo, m.statusName = st.ID, st.Name
			return nil
		}
		names = append(names, st.Name)
	}
	if !strings.EqualFold(want, was) {
		return fmt.Errorf("%s's %s workflow has no status %q (statuses: %s)", m.to.Key, m.typ.Name, want, strings.Join(names, ", "))
	}
	m.notes = append(m.notes, fmt.Sprintf("status %s -> %s's default", want, m.to.Key))
	return nil
}

// moveKept are elements the move itself sets or keeps.
var moveKept = map[string]bool{"summary": true, "type": true, "status": true, "reporter": true, "creator": true,
	"parent": true, "resolution": true, "comment": true, "worklog": true, "link": true, "attachment": true, "transitions": true}

// drop removes from the adjusted file what the target cannot hold and
// records each loss.
func (m *movePlan) drop(fields map[string]moveField) {
	var kept []*xmltree.Node
	for _, c := range m.adjusted.Children {
		if c.Kind != xmltree.Element || moveKept[c.Name] {
			kept = append(kept, c)
			continue
		}
		if c.Name == "field" {
			if m.dropField(c, fields) {
				kept = append(kept, c)
			}
			continue
		}
		id, ok := elemFields[c.Name]
		if !ok {
			kept = append(kept, c)
			continue
		}
		f, on := fields[id]
		switch {
		case c.Name == "components" || c.Name == "fixVersions" || c.Name == "affectsVersions":
			item := "version"
			if c.Name == "components" {
				item = "component"
			}
			var items []*xmltree.Node
			for _, it := range c.ChildrenNamed(item) {
				if on && f.allows("", textOf(it)) {
					items = append(items, it)
				} else {
					m.losses = append(m.losses, item+" "+textOf(it))
				}
			}
			if len(items) == 0 {
				continue
			}
			c.Children = itemsOnly(c, items)
		case !on:
			m.losses = append(m.losses, "<"+c.Name+">")
			continue
		case c.Name == "priority":
			if !f.allows("", textOf(c)) {
				m.losses = append(m.losses, "priority "+textOf(c))
				continue
			}
		}
		kept = append(kept, c)
	}
	m.adjusted.Children = kept
}

// itemsOnly keeps c's children that are not elements, plus items.
func itemsOnly(c *xmltree.Node, items []*xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, ch := range c.Children {
		if ch.Kind != xmltree.Element {
			out = append(out, ch)
		}
	}
	return append(out, items...)
}

// dropField reports whether <field> c survives, dropping its options the
// target does not allow.
func (m *movePlan) dropField(c *xmltree.Node, fields map[string]moveField) bool {
	id, _ := c.Attr("id")
	name, _ := c.Attr("name")
	if name == "" {
		name = id
	}
	f, on := fields[id]
	if !on {
		m.losses = append(m.losses, "<field "+name+">")
		return false
	}
	opts := c.ChildrenNamed("option")
	if len(opts) == 0 {
		return true
	}
	var kept []*xmltree.Node
	for _, o := range opts {
		oid, _ := o.Attr("id")
		if f.allows(oid, textOf(o)) {
			kept = append(kept, o)
		} else {
			m.losses = append(m.losses, fmt.Sprintf("option %s of <field %s>", textOf(o), name))
		}
	}
	if len(kept) == 0 {
		return false
	}
	c.Children = itemsOnly(c, kept)
	return true
}

// required refuses a move that leaves a required target field without a
// value Jira could fill.
func (m *movePlan) required(fields map[string]moveField) error {
	var ids []string
	for id, f := range fields {
		switch id {
		case "project", "issuetype", "summary", "reporter", "parent":
			continue
		}
		if f.Required && !f.HasDefault && !hasValue(m.adjusted, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return fmt.Errorf("%s requires %s (%s) for %s issues; add it to the file", m.to.Key, elemName(ids[0]), fields[ids[0]].Name, m.typ.Name)
}

func hasValue(root *xmltree.Node, id string) bool {
	n := fieldsByID(root)[id]
	if e, ok := systemElems[id]; ok {
		n = child(root, e)
	}
	return n != nil && (textOf(n) != "" || len(n.Children) > 0)
}

// putText sets the text of root's first child named name, adding it if missing.
func putText(root *xmltree.Node, name, text string) {
	n := root.Child(name)
	if n == nil {
		n = el(name)
		root.Children = append(root.Children, n)
	}
	n.Attrs = nil
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

// moveTimeout is how long gfs waits for Jira's move task.
var moveTimeout = 2 * time.Minute

// applyMove moves the issue, then applies the rest of the file against the
// moved issue (jira move spec §5.4).
func (s *session) applyMove(ctx context.Context, req adapter.ApplyRequest, mi int) []adapter.Result {
	out := results(req.Actions)
	m, err := s.planMove(ctx, req)
	if err != nil {
		return failAll(out, err)
	}
	var refresh []string
	if !m.noop {
		if refresh, err = s.bulkMove(ctx, m); err != nil {
			return failAll(out, err)
		}
	}
	is, err := s.getIssue(ctx, m.id)
	if err != nil {
		return failAll(out, err)
	}
	post, err := s.resource(ctx, is, true)
	if err != nil {
		return failAll(out, err)
	}
	adj := m.adjusted
	adj.SetAttr("key", is.Key)
	if !m.noop && m.statusTo == "" {
		putText(adj, "status", textOf(child(post.Root, "status")))
	}
	acts := changes.ResolveActions(&Adapter{}, post.Root, &envelope.Doc{Content: adj}, post.Path, post.Path)
	for _, a := range req.Actions {
		if a.IsAttachment() {
			acts = append(acts, a)
		}
	}
	inner := req
	inner.Local = &adapter.Resource{ID: m.id, Path: post.Path, Root: adj}
	inner.Base, inner.Actions = post, acts
	done := s.applyIssue(ctx, inner, results(acts))
	out[mi].Detail, out[mi].Refresh = is.Key, refresh
	matched := make([]bool, len(done))
	for i, a := range req.Actions {
		if i == mi {
			continue
		}
		for j, d := range done {
			if !matched[j] && sameAction(a, d.Action) {
				matched[j] = true
				out[i].Err, out[i].Code, out[i].ID, out[i].Version = d.Err, d.Code, d.ID, d.Version
				break
			}
		}
	}
	for j, d := range done { // a value re-set by name after the move, not asked for by the file's own changes
		if !matched[j] && d.Err != nil {
			out = append(out, d)
		}
	}
	return out
}

func sameAction(a, b adapter.Action) bool {
	return a.Verb == b.Verb && a.Target == b.Target && a.Group == b.Group && a.File == b.File
}

// bulkMove submits the move and waits for it; it returns the ids of the
// other issues the move took along (its sub-tasks).
func (s *session) bulkMove(ctx context.Context, m *movePlan) ([]string, error) {
	group := map[string]any{
		"inferClassificationDefaults": true, "inferFieldDefaults": true, "inferSubtaskTypeDefault": true,
		"inferStatusDefaults": m.statusTo == "", "issueIdsOrKeys": []string{m.id},
	}
	if m.statusTo != "" {
		group["targetStatus"] = []any{map[string]any{"statuses": map[string][]string{m.statusTo: {m.statusFrom}}}}
	}
	body := map[string]any{"sendBulkNotification": false,
		"targetToSourcesMapping": map[string]any{m.to.ID + "," + m.typ.ID: group}}
	var resp struct {
		TaskID string `json:"taskId"`
	}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/bulk/issues/move", body, &resp); err != nil {
		return nil, fmt.Errorf("move %s to %s: %w", m.key, m.to.Key, err)
	}
	return s.waitMove(ctx, m, resp.TaskID)
}

// waitMove polls the move task until it ends or moveTimeout passes.
func (s *session) waitMove(ctx context.Context, m *movePlan, task string) ([]string, error) {
	var waited time.Duration
	for {
		var q struct {
			Status    string                     `json:"status"`
			Processed []json.Number              `json:"processedAccessibleIssues"`
			Failed    map[string]json.RawMessage `json:"failedAccessibleIssues"`
		}
		if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/bulk/queue/"+url.PathEscape(task), nil, &q); err != nil {
			return nil, fmt.Errorf("move %s to %s (task %s): %w", m.key, m.to.Key, task, err)
		}
		switch q.Status {
		case "COMPLETE":
			if msg, ok := q.Failed[m.id]; ok {
				return nil, fmt.Errorf("move %s to %s failed: %s", m.key, m.to.Key, failText(msg))
			}
			var others []string
			for _, n := range q.Processed {
				if n.String() != m.id {
					others = append(others, n.String())
				}
			}
			return others, nil
		case "FAILED", "CANCELLED", "DEAD":
			var msgs []string
			for _, raw := range q.Failed {
				msgs = append(msgs, failText(raw))
			}
			sort.Strings(msgs)
			return nil, fmt.Errorf("move %s to %s: task %s %s: %s", m.key, m.to.Key, task, strings.ToLower(q.Status), strings.Join(msgs, "; "))
		}
		if waited >= moveTimeout {
			return nil, fmt.Errorf("move still running in Jira (task %s); run gfs pull before committing again", task)
		}
		d := 500 * time.Millisecond
		if waited >= 5*time.Second {
			d = time.Second
		}
		s.report(adapter.Progress{Phase: "wait", Item: fmt.Sprintf("moving %s to %s", m.key, m.to.Key)})
		if err := s.c.Sleep(ctx, d); err != nil {
			return nil, err
		}
		waited += d
	}
}

// failText is a failed issue's messages: a list of strings, or {"errors":[…]}.
func failText(raw json.RawMessage) string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, "; ")
	}
	var obj struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(raw, &obj) == nil && len(obj.Errors) > 0 {
		return strings.Join(obj.Errors, "; ")
	}
	return string(raw)
}
