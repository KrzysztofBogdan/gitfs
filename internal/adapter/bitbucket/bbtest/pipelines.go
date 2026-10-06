package bbtest

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

func (s *Server) pipelineRoutes(mux *http.ServeMux) {
	base := "/2.0/repositories/{ws}/{repo}/pipelines"
	mux.HandleFunc("GET "+base+"/", s.listRuns)
	mux.HandleFunc("GET "+base+"/{uuid}", s.getRun)
	mux.HandleFunc("GET "+base+"/{uuid}/steps/", s.listSteps)
	mux.HandleFunc("GET "+base+"/{uuid}/steps/{step}/log", s.stepLog)
}

func (s *Server) runOf(w http.ResponseWriter, r *http.Request) *Run {
	rp := s.repo(w, r)
	if rp == nil {
		return nil
	}
	for _, run := range rp.Runs {
		if run.UUID == r.PathValue("uuid") || strings.Trim(run.UUID, "{}") == r.PathValue("uuid") {
			return run
		}
	}
	bbError(w, 404, "Pipeline not found", "")
	return nil
}

func stateJSON(state, stage, result string) map[string]any {
	out := map[string]any{"name": state, "type": "pipeline_state_" + strings.ToLower(state)}
	if stage != "" {
		out["stage"] = map[string]any{"name": stage}
	}
	if result != "" {
		out["result"] = map[string]any{"name": result}
	}
	return out
}

func selectorJSON(sel string) map[string]any {
	typ, pattern, _ := strings.Cut(sel, ":")
	out := map[string]any{"type": typ}
	if pattern != "" {
		out["pattern"] = pattern
	}
	return out
}

func (s *Server) runJSON(run *Run) map[string]any {
	target := map[string]any{"type": run.TargetType, "commit": map[string]any{"hash": run.Commit}, "selector": selectorJSON(run.Selector)}
	switch run.TargetType {
	case "pipeline_ref_target":
		target["ref_type"], target["ref_name"] = run.RefType, run.RefName
	case "pipeline_pullrequest_target":
		target["source"], target["destination"] = run.RefName, "main"
		target["pullrequest"] = map[string]any{"id": run.PR}
	}
	result := ""
	if run.State == "COMPLETED" {
		result = run.Result
	}
	out := map[string]any{"type": "pipeline", "uuid": run.UUID, "build_number": run.Number, "creator": s.user(run.Creator),
		"state": stateJSON(run.State, run.Stage, result), "target": target,
		"trigger":    map[string]any{"name": run.Trigger, "type": "pipeline_trigger_" + strings.ToLower(run.Trigger)},
		"created_on": ts(run.Created), "completed_on": ts(run.Completed)}
	if run.State == "COMPLETED" {
		secs := int(run.Completed.Sub(run.Created).Seconds())
		out["duration_in_seconds"], out["build_seconds_used"] = secs, secs-10
	}
	return out
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repo(w, r)
	if rp == nil {
		return
	}
	hash := r.URL.Query().Get("target.commit.hash")
	var vs []any
	for i := len(rp.Runs) - 1; i >= 0; i-- { // newest first, whatever sort says
		if hash == "" || rp.Runs[i].Commit == hash {
			vs = append(vs, s.runJSON(rp.Runs[i]))
		}
	}
	s.page(w, r, vs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runOf(w, r); run != nil {
		writeJSON(w, s.runJSON(run))
	}
}

func (s *Server) listSteps(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runOf(w, r)
	if run == nil {
		return
	}
	var vs []any
	for _, st := range run.Steps {
		stage, result := "", ""
		if st.State == "IN_PROGRESS" {
			stage = "RUNNING"
		}
		if st.State == "COMPLETED" {
			result = st.Result
		}
		v := map[string]any{"type": "pipeline_step", "uuid": st.UUID, "name": st.Name,
			"image": map[string]any{"name": strings.TrimPrefix(st.Image, "par:")}, "state": stateJSON(st.State, stage, result),
			"started_on": ts(st.Started), "completed_on": ts(st.Completed)}
		if st.State == "COMPLETED" {
			v["duration_in_seconds"] = int(st.Completed.Sub(st.Started).Seconds())
		}
		vs = append(vs, v)
	}
	s.page(w, r, vs)
}

var rangeRe = regexp.MustCompile(`^bytes=(\d+)-(\d*)$`)

func (s *Server) stepLog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runOf(w, r)
	if run == nil {
		return
	}
	if s.AdvanceOnLog {
		s.advance(run)
	}
	var st *Step
	for _, x := range run.Steps {
		if x.UUID == r.PathValue("step") || strings.Trim(x.UUID, "{}") == r.PathValue("step") {
			st = x
		}
	}
	if st == nil || st.State == "PENDING" {
		bbError(w, 404, "Log not found", "")
		return
	}
	total := strconv.Itoa(len(st.Log))
	if st.State != "COMPLETED" {
		total = "*"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	m := rangeRe.FindStringSubmatch(r.Header.Get("Range"))
	if m == nil {
		w.Write(st.Log)
		return
	}
	from, _ := strconv.Atoi(m[1])
	to := len(st.Log) - 1
	if m[2] != "" {
		if n, _ := strconv.Atoi(m[2]); n < to {
			to = n
		}
	}
	if from >= len(st.Log) {
		w.Header().Set("Content-Range", "bytes */"+total)
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", from, to, total))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(st.Log[from : to+1])
}
