package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
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
