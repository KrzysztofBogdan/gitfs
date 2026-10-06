package bbtest

import (
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /2.0/user", s.getUser)
	mux.HandleFunc("GET /2.0/workspaces/{ws}/members", s.getMembers)
	mux.HandleFunc("GET /2.0/repositories/{ws}", s.getRepos)
	mux.HandleFunc("GET /2.0/repositories/{ws}/{repo}/refs/branches/{name...}", s.getBranch)
	mux.HandleFunc("GET /2.0/repositories/{ws}/{repo}/pullrequests", s.listPRs)
	mux.HandleFunc("GET /2.0/repositories/{ws}/{repo}/pullrequests/{id}", s.getPR)
	mux.HandleFunc("GET /2.0/repositories/{ws}/{repo}/pullrequests/{id}/comments", s.listComments)
	mux.HandleFunc("GET /2.0/repositories/{ws}/{repo}/pullrequests/{id}/statuses", s.listStatuses)
	s.writeRoutes(mux)
	s.pipelineRoutes(mux)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.user(Me))
}

func (s *Server) getMembers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var vs []any
	for _, m := range s.members {
		vs = append(vs, map[string]any{"type": "workspace_membership", "user": s.user(m.Account)})
	}
	s.page(w, r, vs)
}

func (s *Server) getRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var vs []any
	for _, slug := range s.order {
		rp := s.repos[slug]
		vs = append(vs, map[string]any{"type": "repository", "slug": slug, "name": slug, "full_name": r.PathValue("ws") + "/" + slug,
			"mainbranch": map[string]any{"name": rp.Main}})
	}
	s.page(w, r, vs)
}

// repo looks up the path's repository, answering 404 when it is missing.
func (s *Server) repo(w http.ResponseWriter, r *http.Request) *Repo {
	rp := s.repos[r.PathValue("repo")]
	if rp == nil {
		bbError(w, 404, "Repository "+r.PathValue("ws")+"/"+r.PathValue("repo")+" not found", "")
	}
	return rp
}

func (s *Server) prOf(w http.ResponseWriter, r *http.Request) (*Repo, *PR) {
	rp := s.repo(w, r)
	if rp == nil {
		return nil, nil
	}
	id, _ := strconv.Atoi(r.PathValue("id"))
	p := s.pr(rp.Slug, id)
	if p == nil {
		bbError(w, 404, "Pull request not found", "")
	}
	return rp, p
}

func (s *Server) getBranch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repo(w, r)
	if rp == nil {
		return
	}
	if !rp.Branches[r.PathValue("name")] {
		bbError(w, 404, "Branch not found", "")
		return
	}
	writeJSON(w, map[string]any{"type": "branch", "name": r.PathValue("name")})
}

var qUpdatedRe = regexp.MustCompile(`updated_on\s*(>=|>)\s*"([^"]+)"`)

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t, _ = time.Parse("2006-01-02", v)
	}
	return t
}

func (s *Server) listPRs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repo(w, r)
	if rp == nil {
		return
	}
	q := r.URL.Query()
	states := q["state"]
	if len(states) == 0 {
		states = []string{"OPEN"}
	}
	var after time.Time
	strict := false
	if m := qUpdatedRe.FindStringSubmatch(q.Get("q")); m != nil {
		after, strict = parseTime(m[2]), m[1] == ">"
	}
	var prs []*PR
	for _, p := range rp.PRs {
		if !slices.Contains(states, p.State) {
			continue
		}
		if !after.IsZero() && (p.Updated.Before(after) || (strict && p.Updated.Equal(after))) {
			continue
		}
		prs = append(prs, p)
	}
	switch q.Get("sort") {
	case "-updated_on":
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].Updated.After(prs[j].Updated) })
	case "updated_on":
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].Updated.Before(prs[j].Updated) })
	default:
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].ID > prs[j].ID })
	}
	vs := make([]any, len(prs))
	for i, p := range prs {
		vs[i] = s.prJSON(rp, p)
	}
	s.page(w, r, vs)
}

func (s *Server) getPR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rp, p := s.prOf(w, r); p != nil {
		writeJSON(w, s.prJSON(rp, p))
	}
}

func (s *Server) prJSON(rp *Repo, p *PR) map[string]any {
	var reviewers, participants []any
	for _, a := range p.Reviewers {
		reviewers = append(reviewers, s.user(a))
	}
	for _, pa := range p.Participants {
		var state any
		if pa.State != "" {
			state = pa.State
		}
		participants = append(participants, map[string]any{"type": "participant", "user": s.user(pa.Account), "role": pa.Role,
			"approved": pa.State == "approved", "state": state})
	}
	out := map[string]any{
		"type": "pullrequest", "id": p.ID, "title": p.Title, "description": p.Description,
		"summary": map[string]any{"raw": p.Description, "markup": "markdown"},
		"state":   p.State, "draft": p.Draft, "close_source_branch": p.CloseSource,
		"author": s.user(p.Author),
		"source": map[string]any{"branch": map[string]any{"name": p.Source}, "commit": map[string]any{"hash": p.SourceCommit},
			"repository": map[string]any{"full_name": "acme/" + rp.Slug}},
		"destination": map[string]any{"branch": map[string]any{"name": p.Dest, "merge_strategies": p.Strategies,
			"default_merge_strategy": p.DefaultStrategy}, "commit": map[string]any{"hash": p.DestCommit}},
		"reviewers": reviewers, "participants": participants,
		"created_on": ts(p.Created), "updated_on": ts(p.Updated), "merge_commit": nil,
	}
	if p.MergeCommit != "" {
		out["merge_commit"] = map[string]any{"hash": p.MergeCommit}
	}
	return out
}

func (s *Server) commentJSON(p *PR, c *Comment) map[string]any {
	out := map[string]any{"type": "pullrequest_comment", "id": c.ID, "content": map[string]any{"raw": c.Raw, "markup": "markdown"},
		"user": s.user(c.Account), "created_on": ts(c.Created), "updated_on": ts(c.Updated), "deleted": c.Deleted,
		"pullrequest": map[string]any{"id": p.ID}}
	if c.Parent != 0 {
		out["parent"] = map[string]any{"id": c.Parent}
	}
	if c.Path != "" {
		in := map[string]any{"path": c.Path, "from": nil, "to": nil}
		if c.From != 0 {
			in["from"] = c.From
		}
		if c.To != 0 {
			in["to"] = c.To
		}
		out["inline"] = in
	}
	if c.Resolved {
		out["resolution"] = map[string]any{"type": "comment_resolution", "user": s.user(Me), "created_on": ts(c.Updated)}
	}
	return out
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.prOf(w, r)
	if p == nil {
		return
	}
	var vs []any
	for _, c := range p.Comments {
		vs = append(vs, s.commentJSON(p, c))
	}
	s.page(w, r, vs)
}

func (s *Server) listStatuses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, p := s.prOf(w, r)
	if p == nil {
		return
	}
	var vs []any
	for _, st := range rp.Statuses[p.SourceCommit] {
		vs = append(vs, map[string]any{"type": "build", "key": st.Key, "state": st.State, "name": st.Name, "url": st.URL})
	}
	s.page(w, r, vs)
}
