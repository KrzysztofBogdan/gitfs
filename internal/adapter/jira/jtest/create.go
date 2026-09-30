package jtest

import (
	"encoding/json"
	"net/http"
)

func init() { registrars = append(registrars, (*Server).createRoutes) }

func (s *Server) createRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue", s.createIssue)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}", s.deleteIssue)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	var project struct{ Key string }
	var itype struct{ ID string }
	json.Unmarshal(body.Fields["project"], &project)
	json.Unmarshal(body.Fields["issuetype"], &itype)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projects[project.Key] == nil {
		jiraError(w, 400, "", map[string]string{"project": "valid project is required"})
		return
	}
	screen := map[string]Field{}
	for _, f := range s.fields[project.Key+"/"+itype.ID] {
		screen[f.ID] = f
	}
	if len(screen) == 0 {
		jiraError(w, 400, "", map[string]string{"issuetype": "valid issue type is required"})
		return
	}
	errs := map[string]string{}
	for id, f := range screen {
		if _, ok := body.Fields[id]; f.Required && !ok && id != "project" && id != "issuetype" {
			errs[id] = f.Name + " is required."
		}
	}
	for id := range body.Fields {
		if _, ok := screen[id]; !ok && id != "project" && id != "issuetype" {
			errs[id] = "Field '" + id + "' cannot be set. It is not on the appropriate screen, or unknown."
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	is := &Issue{Project: project.Key, Type: itype.ID, Reporter: Author, Fields: map[string]any{}}
	for id, raw := range body.Fields {
		if id == "project" || id == "issuetype" {
			continue
		}
		if msg := s.setField(is, screen[id], raw); msg != "" {
			errs[id] = msg
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	is = s.addIssue(*is)
	writeJSON(w, map[string]any{"id": is.ID, "key": is.Key})
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	for _, c := range s.issues {
		if par, ok := c.Fields["parent"].(map[string]any); ok && par["key"] == is.Key && r.URL.Query().Get("deleteSubtasks") != "true" {
			jiraError(w, 400, "The issue has subtasks and cannot be deleted without them.", nil)
			return
		}
	}
	delete(s.issues, is.ID)
	w.WriteHeader(http.StatusNoContent)
}
