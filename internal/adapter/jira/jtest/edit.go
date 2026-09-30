package jtest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func init() { registrars = append(registrars, (*Server).editRoutes) }

// editRoutes serves field edits and transitions.
func (s *Server) editRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /rest/api/3/issue/{id}/editmeta", s.editmeta)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}", s.editIssue)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/transitions", s.getTransitions)
	mux.HandleFunc("POST /rest/api/3/issue/{id}/transitions", s.doTransition)
}

// editFields is the edit screen of is: its create screen minus EditHidden and
// the issue type, which the edit screen shows without operations.
func (s *Server) editFields(is *Issue) map[string]Field {
	out := map[string]Field{}
	for _, f := range s.fields[is.Project+"/"+is.Type] {
		if f.ID != "issuetype" && !contains(s.EditHidden, f.ID) {
			out[f.ID] = f
		}
	}
	return out
}

func (s *Server) editmeta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	out := map[string]any{}
	for id, f := range s.editFields(is) {
		m := fieldJSON(f)
		m["operations"] = []string{"set"}
		out[id] = m
	}
	writeJSON(w, map[string]any{"fields": out})
}

// setField stores one field value the way Jira would return it; a message
// is a field error.
func (s *Server) setField(is *Issue, f Field, raw json.RawMessage) string {
	var v any
	json.Unmarshal(raw, &v)
	switch f.ID {
	case "summary":
		str, _ := v.(string)
		if str == "" {
			return "You must specify a summary of the issue."
		}
		is.Summary = str
		return ""
	case "assignee", "reporter":
		acc := ""
		if m, ok := v.(map[string]any); ok {
			acc, _ = m["accountId"].(string)
			if s.users[acc] == nil {
				return fmt.Sprintf("User '%s' does not exist.", acc)
			}
		}
		if f.ID == "assignee" {
			is.Assignee = acc
		} else {
			is.Reporter = acc
		}
		return ""
	}
	if f.Type == "option" && v != nil {
		m, _ := v.(map[string]any)
		for _, o := range f.Options {
			if o.ID == m["id"] || o.Value == m["value"] {
				is.Fields[f.ID] = map[string]any{"id": o.ID, "value": o.Value}
				return ""
			}
		}
		return fmt.Sprintf("Option value '%v' is not valid", m["value"])
	}
	if v == nil {
		delete(is.Fields, f.ID)
	} else {
		is.Fields[f.ID] = v
	}
	return ""
}

func (s *Server) editIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	screen := s.editFields(is)
	errs := map[string]string{}
	for id := range body.Fields {
		if _, ok := screen[id]; !ok {
			errs[id] = fmt.Sprintf("Field '%s' cannot be set. It is not on the appropriate screen, or unknown.", id)
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	next := *is
	next.Fields = map[string]any{}
	for k, v := range is.Fields {
		next.Fields[k] = v
	}
	for id, raw := range body.Fields {
		if msg := s.setField(&next, screen[id], raw); msg != "" {
			errs[id] = msg
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	next.Updated = s.now()
	*is = next
	w.WriteHeader(http.StatusNoContent)
}

// available is the transitions of is's workflow that start from its status.
func (s *Server) available(is *Issue) []Transition {
	var out []Transition
	for _, t := range s.workflow(is).Transitions {
		if len(t.From) == 0 || contains(t.From, is.Status) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) fieldMeta(is *Issue, id string) Field {
	for _, f := range s.fields[is.Project+"/"+is.Type] {
		if f.ID == id {
			return f
		}
	}
	return Field{ID: id, Name: id}
}

func (s *Server) getTransitions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	var out []any
	for _, t := range s.available(is) {
		cat := "new"
		for _, st := range s.workflow(is).Statuses {
			if st.Name == t.To {
				cat = st.Category
			}
		}
		fields := map[string]any{}
		for _, sf := range t.Screen {
			f := s.fieldMeta(is, sf.ID)
			m := map[string]any{"required": sf.Required, "name": f.Name}
			if len(f.Options) > 0 {
				var vals []any
				for _, o := range f.Options {
					vals = append(vals, map[string]any{"id": o.ID, "name": o.Value, "value": o.Value})
				}
				m["allowedValues"] = vals
			}
			fields[sf.ID] = m
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "hasScreen": len(t.Screen) > 0,
			"to": map[string]any{"name": t.To, "statusCategory": map[string]any{"key": cat}}, "fields": fields})
	}
	writeJSON(w, map[string]any{"transitions": out})
}

func (s *Server) doTransition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	var tr *Transition
	for _, t := range s.available(is) {
		if t.ID == body.Transition.ID {
			tr = &t
		}
	}
	if tr == nil {
		jiraError(w, 400, "Transition id '"+body.Transition.ID+"' is not valid for this issue.", nil)
		return
	}
	onScreen := map[string]bool{}
	errs := map[string]string{}
	for _, sf := range tr.Screen {
		onScreen[sf.ID] = true
		if v, ok := body.Fields[sf.ID]; sf.Required && (!ok || string(v) == "null") {
			errs[sf.ID] = sf.ID + " is required."
		}
	}
	for id := range body.Fields {
		if !onScreen[id] {
			errs[id] = fmt.Sprintf("Field '%s' cannot be set. It is not on the appropriate screen, or unknown.", id)
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	for id, raw := range body.Fields {
		if msg := s.setField(is, s.fieldMeta(is, id), raw); msg != "" {
			jiraError(w, 400, "", map[string]string{id: msg})
			return
		}
	}
	is.Status = tr.To
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}
