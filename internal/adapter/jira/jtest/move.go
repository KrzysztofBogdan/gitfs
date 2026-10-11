package jtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

func init() { registrars = append(registrars, (*Server).moveRoutes) }

// moveTask is a bulk move as GET /bulk/queue reports it.
type moveTask struct {
	running   int
	fail      string
	processed []int
}

func (s *Server) moveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/bulk/issues/move", s.bulkMove)
	mux.HandleFunc("GET /rest/api/3/bulk/queue/{id}", s.moveQueue)
}

type moveGroup struct {
	InferStatus  bool `json:"inferStatusDefaults"`
	TargetStatus []struct {
		Statuses map[string][]string `json:"statuses"`
	} `json:"targetStatus"`
	Issues []string `json:"issueIdsOrKeys"`
}

// bulkMove validates the whole request, then moves every issue and its
// sub-tasks at once; the task only reports.
func (s *Server) bulkMove(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Mapping map[string]moveGroup `json:"targetToSourcesMapping"`
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MoveBodies = append(s.MoveBodies, string(raw))
	if err := json.Unmarshal(raw, &body); err != nil {
		moveError(w, err.Error())
		return
	}
	type job struct {
		is          *Issue
		p           *Project
		typ, status string
	}
	var jobs []job
	for key, g := range body.Mapping {
		pid, tid, _ := strings.Cut(key, ",")
		p := s.projectByID(pid)
		if p == nil || !slices.ContainsFunc(s.types[p.Key], func(t IssueType) bool { return t.ID == tid }) {
			moveError(w, "Invalid target group "+key)
			return
		}
		for _, k := range g.Issues {
			is := s.lookup(k)
			if is == nil {
				moveError(w, "Issue "+k+" not found")
				return
			}
			if s.isSubtask(is) {
				moveError(w, "Parent issue should also be moved or target Parent issue should be specified")
				return
			}
			status := ""
			if !g.InferStatus {
				wf := s.workflow(&Issue{Project: p.Key, Type: tid})
				for _, ts := range g.TargetStatus {
					for to := range ts.Statuses {
						i := slices.IndexFunc(wf.Statuses, func(st Status) bool { return st.ID == to })
						if i < 0 {
							moveError(w, fmt.Sprintf("You provided a target status that doesn't exist in the target workflow for the target group %s:%s.", pid, tid))
							return
						}
						status = wf.Statuses[i].Name
					}
				}
				if status == "" {
					moveError(w, fmt.Sprintf("When inferStatusDefaults is false for the target group %s:%s, you must provide a target status mapping.", pid, tid))
					return
				}
			}
			jobs = append(jobs, job{is, p, tid, status})
		}
	}
	id := s.nextID()
	t := &moveTask{running: s.MoveRunning, fail: s.MoveFail}
	if t.fail == "" {
		for _, j := range jobs {
			t.processed = append(t.processed, s.moveIssue(j.is, j.p, j.typ, j.status)...)
		}
	}
	s.tasks[id] = t
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"taskId": id})
}

func moveError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
}

func (s *Server) projectByID(id string) *Project {
	for _, p := range s.projects {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Server) isSubtask(is *Issue) bool {
	for _, t := range s.types[is.Project] {
		if t.ID == is.Type {
			return t.Subtask
		}
	}
	return false
}

// moveIssue moves is and its sub-tasks to p as type typ; status "" keeps
// the status by name, else the workflow's first. It returns the moved ids.
func (s *Server) moveIssue(is *Issue, p *Project, typ, status string) []int {
	old := is.Key
	s.relocate(is, p, typ, status)
	ids := []int{atoi(is.ID)}
	subType := ""
	for _, t := range s.types[p.Key] {
		if t.Subtask {
			subType = t.ID
			break
		}
	}
	for _, c := range s.issues {
		if par, ok := c.Fields["parent"].(map[string]any); ok && par["key"] == old {
			c.Fields["parent"] = map[string]any{"key": is.Key}
			if s.isSubtask(c) {
				s.relocate(c, p, subType, "")
				ids = append(ids, atoi(c.ID))
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// relocate gives is its new project, key, type and status, and drops the
// fields and options the target type's screen does not have.
func (s *Server) relocate(is *Issue, p *Project, typ, status string) {
	is.OldKeys = append(is.OldKeys, is.Key)
	s.counters[p.Key]++
	is.Project, is.Key, is.Type = p.Key, fmt.Sprintf("%s-%d", p.Key, s.counters[p.Key]), typ
	wf := s.workflow(is)
	switch {
	case status != "":
		is.Status = status
	case !slices.ContainsFunc(wf.Statuses, func(st Status) bool { return st.Name == is.Status }):
		is.Status = wf.Statuses[0].Name
	}
	fields := map[string]Field{}
	for _, f := range s.fields[p.Key+"/"+typ] {
		fields[f.ID] = f
	}
	for id, v := range is.Fields {
		f, ok := fields[id]
		switch {
		case id == "parent":
		case !ok:
			delete(is.Fields, id)
		case len(f.Options) > 0:
			if v = keepAllowed(v, f.Options); v == nil {
				delete(is.Fields, id)
			} else {
				is.Fields[id] = v
			}
		}
	}
	is.Updated = s.now()
}

// keepAllowed is v without the options or named values opts does not allow.
func keepAllowed(v any, opts []Option) any {
	ok := func(m map[string]any) bool {
		return slices.ContainsFunc(opts, func(o Option) bool {
			return o.ID == m["id"] || o.Value == m["value"] || o.Value == m["name"]
		})
	}
	switch x := v.(type) {
	case map[string]any:
		if ok(x) {
			return x
		}
		return nil
	case []any:
		var kept []any
		for _, e := range x {
			if m, isMap := e.(map[string]any); isMap && ok(m) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return kept
	}
	return v
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (s *Server) moveQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[r.PathValue("id")]
	if t == nil {
		jiraError(w, 404, "no task", nil)
		return
	}
	out := map[string]any{"taskId": r.PathValue("id"), "processedAccessibleIssues": t.processed, "failedAccessibleIssues": map[string]any{}}
	switch {
	case t.running > 0:
		t.running--
		out["status"] = "RUNNING"
	case t.fail != "":
		out["status"] = "FAILED"
		out["failedAccessibleIssues"] = map[string]any{"0": []string{t.fail}}
	default:
		out["status"] = "COMPLETE"
		out["progressPercent"] = 100
	}
	writeJSON(w, out)
}
