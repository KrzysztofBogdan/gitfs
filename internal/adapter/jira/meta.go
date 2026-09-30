package jira

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// fieldMeta is one field of an issue type's create screen (createmeta).
type fieldMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`   // schema.type: string, number, array, user, option, …
	Items    string `json:"items,omitempty"`  // schema.items for arrays
	Custom   string `json:"custom,omitempty"` // schema.custom: the custom field type key
	Required bool   `json:"required,omitempty"`
}

type typeMeta struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Subtask bool                 `json:"subtask,omitempty"`
	Fields  map[string]fieldMeta `json:"fields"`
}

type projectMeta struct {
	Key   string               `json:"key"`
	ID    string               `json:"id"`
	JSM   bool                 `json:"jsm,omitempty"`
	Types map[string]*typeMeta `json:"types,omitempty"` // by issue type id; nil until loaded
}

func (p *projectMeta) dir() string { return strings.ToLower(p.Key) }

// typeNamed finds an issue type by name, case-insensitively.
func (p *projectMeta) typeNamed(name string) *typeMeta {
	for _, t := range p.Types {
		if strings.EqualFold(t.Name, name) {
			return t
		}
	}
	return nil
}

// metaCache is .gfs/cache/jira/meta.json: project, type and field metadata.
type metaCache struct {
	Projects map[string]*projectMeta `json:"projects"` // by key
}

func loadMeta(path string) (*metaCache, error) {
	m := &metaCache{Projects: map[string]*projectMeta{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, m); err != nil {
		return &metaCache{Projects: map[string]*projectMeta{}}, nil // a broken cache is rebuilt
	}
	if m.Projects == nil {
		m.Projects = map[string]*projectMeta{}
	}
	return m, nil
}

func (m *metaCache) save(path string) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// systemElems maps Jira system field ids to their elements (jira spec §5.1).
var systemElems = map[string]string{
	"summary": "summary", "issuetype": "type", "status": "status", "resolution": "resolution",
	"priority": "priority", "assignee": "assignee", "reporter": "reporter", "creator": "creator",
	"parent": "parent", "labels": "labels", "components": "components", "fixVersions": "fixVersions",
	"versions": "affectsVersions", "duedate": "due", "timetracking": "timetracking",
	"environment": "environment", "description": "description",
}

// elemFields is systemElems inverted: element name to field id.
var elemFields = func() map[string]string {
	m := map[string]string{}
	for id, e := range systemElems {
		m[e] = id
	}
	return m
}()

// mappedApart are shown by their own elements, never as <field> (jira spec §5.2).
var mappedApart = map[string]bool{"comment": true, "attachment": true, "issuelinks": true, "worklog": true, "project": true}

// alwaysFields are requested for every issue, whatever its screens.
var alwaysFields = []string{"summary", "issuetype", "status", "resolution", "reporter", "creator", "created", "updated",
	"resolutiondate", "timetracking", "project", "comment", "worklog", "attachment", "issuelinks"}

// fieldSet is what an issue of type t shows (jira spec §5.2): the create
// screen's fields plus environment, minus the separately mapped ones.
func fieldSet(t *typeMeta) map[string]fieldMeta {
	out := map[string]fieldMeta{}
	for id, m := range t.Fields {
		if !mappedApart[id] {
			out[id] = m
		}
	}
	if _, ok := out["environment"]; !ok {
		out["environment"] = fieldMeta{ID: "environment", Name: "Environment", Type: "string"}
	}
	return out
}

// projectFieldSet is the union of every type's field set in p: the fallback
// for an issue whose type has no create screen.
func projectFieldSet(p *projectMeta) map[string]fieldMeta {
	out := map[string]fieldMeta{}
	for _, t := range p.Types {
		for id, m := range fieldSet(t) {
			out[id] = m
		}
	}
	if len(out) == 0 {
		out["environment"] = fieldMeta{ID: "environment", Name: "Environment", Type: "string"}
	}
	return out
}
