package bbtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

func (s *Server) writeRoutes(mux *http.ServeMux) {
	base := "/2.0/repositories/{ws}/{repo}/pullrequests"
	mux.HandleFunc("POST "+base, s.createPR)
	mux.HandleFunc("PUT "+base+"/{id}", s.updatePR)
	mux.HandleFunc("POST "+base+"/{id}/approve", s.review("approved", true))
	mux.HandleFunc("DELETE "+base+"/{id}/approve", s.review("approved", false))
	mux.HandleFunc("POST "+base+"/{id}/request-changes", s.review("changes_requested", true))
	mux.HandleFunc("DELETE "+base+"/{id}/request-changes", s.review("changes_requested", false))
	mux.HandleFunc("POST "+base+"/{id}/merge", s.merge)
	mux.HandleFunc("GET "+base+"/{id}/merge/task-status/{task}", s.taskStatus)
	mux.HandleFunc("POST "+base+"/{id}/decline", s.decline)
	mux.HandleFunc("POST "+base+"/{id}/comments", s.createComment)
	mux.HandleFunc("PUT "+base+"/{id}/comments/{cid}", s.updateComment)
	mux.HandleFunc("DELETE "+base+"/{id}/comments/{cid}", s.deleteComment)
	mux.HandleFunc("POST "+base+"/{id}/comments/{cid}/resolve", s.resolve(true))
	mux.HandleFunc("DELETE "+base+"/{id}/comments/{cid}/resolve", s.resolve(false))
}

type prBody struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Source      *struct {
		Branch struct{ Name string } `json:"branch"`
	} `json:"source"`
	Destination *struct {
		Branch struct{ Name string } `json:"branch"`
	} `json:"destination"`
	Reviewers *[]struct {
		AccountID string `json:"account_id"`
	} `json:"reviewers"`
	CloseSourceBranch *bool `json:"close_source_branch"`
	Draft             *bool `json:"draft"`
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		bbError(w, 400, "Bad request", err.Error())
		return false
	}
	return true
}

// apply sets b's fields on p; a failed check answers 400 and returns false.
func (s *Server) apply(w http.ResponseWriter, rp *Repo, p *PR, b prBody) bool {
	if b.Destination != nil {
		if !rp.Branches[b.Destination.Branch.Name] {
			bbError(w, 400, "destination: branch not found: "+b.Destination.Branch.Name, "")
			return false
		}
		p.Dest = b.Destination.Branch.Name
	}
	if b.Reviewers != nil {
		var rs []string
		for _, rv := range *b.Reviewers {
			if rv.AccountID == p.Author {
				bbError(w, 400, s.user(rv.AccountID)["display_name"].(string)+" is the author and cannot be included as a reviewer.", "")
				return false
			}
			rs = append(rs, rv.AccountID)
		}
		p.Reviewers = rs
	}
	if b.Title != nil {
		p.Title = *b.Title
	}
	if b.Description != nil {
		p.Description = *b.Description
	}
	if b.CloseSourceBranch != nil {
		p.CloseSource = *b.CloseSourceBranch
	}
	if b.Draft != nil {
		p.Draft = *b.Draft
	}
	return true
}

func (s *Server) createPR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.repo(w, r)
	var b prBody
	if rp == nil || !decode(w, r, &b) {
		return
	}
	if b.Title == nil || *b.Title == "" {
		bbError(w, 400, "title: This field is required.", "")
		return
	}
	if b.Source == nil || !rp.Branches[b.Source.Branch.Name] {
		bbError(w, 400, "source: branch not found", "")
		return
	}
	p := &PR{ID: len(rp.PRs) + 1, State: "OPEN", Source: b.Source.Branch.Name, Dest: rp.Main, Author: Me,
		Strategies: []string{"merge_commit", "squash", "fast_forward"}, DefaultStrategy: "merge_commit",
		DestCommit: "0f9e8d7c6b5a"}
	p.SourceCommit = fmt.Sprintf("c0ffee%04d", p.ID)
	if !s.apply(w, rp, p, b) {
		return
	}
	p.Created = s.now()
	p.Updated = p.Created
	rp.PRs = append(rp.PRs, p)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, s.prJSON(rp, p))
}

func (s *Server) updatePR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, p := s.prOf(w, r)
	var b prBody
	if p == nil || !decode(w, r, &b) {
		return
	}
	if p.State != "OPEN" {
		bbError(w, 400, "You can't edit a pull request that is not open.", "")
		return
	}
	if !s.apply(w, rp, p, b) {
		return
	}
	p.Updated = s.now()
	writeJSON(w, s.prJSON(rp, p))
}

func (s *Server) review(state string, on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, p := s.prOf(w, r)
		if p == nil {
			return
		}
		if p.Author == Me && state == "approved" && on {
			bbError(w, 400, "You can't approve your own pull request.", "")
			return
		}
		i := slices.IndexFunc(p.Participants, func(pa Participant) bool { return pa.Account == Me })
		if i < 0 {
			role := "PARTICIPANT"
			if slices.Contains(p.Reviewers, Me) {
				role = "REVIEWER"
			}
			p.Participants = append(p.Participants, Participant{Account: Me, Role: role})
			i = len(p.Participants) - 1
		}
		switch {
		case on:
			p.Participants[i].State = state
		case p.Participants[i].State != state:
			bbError(w, 404, "No "+state+" review to remove", "")
			return
		default:
			p.Participants[i].State = ""
		}
		p.Updated = s.now()
		if !on {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"type": "participant", "user": s.user(Me), "state": state, "approved": state == "approved"})
	}
}

func (s *Server) merge(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, p := s.prOf(w, r)
	var b struct {
		MergeStrategy     string `json:"merge_strategy"`
		Message           string `json:"message"`
		CloseSourceBranch *bool  `json:"close_source_branch"`
	}
	if p == nil || !decode(w, r, &b) {
		return
	}
	if p.State != "OPEN" {
		bbError(w, 400, "You can't merge a pull request that is not open.", "")
		return
	}
	strategy := b.MergeStrategy
	if strategy == "" {
		strategy = p.DefaultStrategy
	}
	if !slices.Contains(p.Strategies, strategy) {
		bbError(w, 400, "merge_strategy: "+strategy+" is not allowed", "")
		return
	}
	if s.MergeAsync && !p.merged {
		p.merged = true
		task := "task-" + strconv.Itoa(s.id())
		s.tasks[task] = p.ID
		w.Header().Set("Location", s.srv.URL+r.URL.Path+"/task-status/"+task)
		w.WriteHeader(http.StatusAccepted)
		p.MergeUsed = strategy
		return
	}
	s.finishMerge(p, strategy, b.CloseSourceBranch)
	writeJSON(w, s.prJSON(rp, p))
}

func (s *Server) finishMerge(p *PR, strategy string, closeSource *bool) {
	p.State, p.MergeUsed, p.MergeCommit, p.Updated = "MERGED", strategy, "feedface"+strconv.Itoa(p.ID), s.now()
	if closeSource != nil {
		p.CloseSource = *closeSource
	}
}

func (s *Server) taskStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, p := s.prOf(w, r)
	if p == nil {
		return
	}
	if p.State == "OPEN" {
		s.finishMerge(p, p.MergeUsed, nil)
		writeJSON(w, map[string]any{"task_status": "PENDING"})
		return
	}
	writeJSON(w, map[string]any{"task_status": "SUCCESS", "merge_result": s.prJSON(rp, p)})
}

func (s *Server) decline(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, p := s.prOf(w, r)
	if p == nil {
		return
	}
	if p.State != "OPEN" {
		bbError(w, 400, "You can't decline a pull request that is not open.", "")
		return
	}
	p.State, p.Updated = "DECLINED", s.now()
	writeJSON(w, s.prJSON(rp, p))
}

func (s *Server) touch(p *PR) {
	if s.CommentsBumpUpdated {
		p.Updated = s.clock
	}
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p := s.prOf(w, r)
	var b struct {
		Content struct{ Raw string } `json:"content"`
		Parent  *struct{ ID int }    `json:"parent"`
		Inline  *struct {
			Path string `json:"path"`
			From *int   `json:"from"`
			To   *int   `json:"to"`
		} `json:"inline"`
	}
	if p == nil || !decode(w, r, &b) {
		return
	}
	c := &Comment{ID: s.id(), Account: Me, Raw: b.Content.Raw}
	if b.Parent != nil {
		if s.comment(p, b.Parent.ID) == nil {
			bbError(w, 400, "parent: comment not found", "")
			return
		}
		c.Parent = b.Parent.ID
	}
	if b.Inline != nil {
		c.Path = b.Inline.Path
		if b.Inline.From != nil {
			c.From = *b.Inline.From
		}
		if b.Inline.To != nil {
			c.To = *b.Inline.To
		}
	}
	c.Created = s.now()
	c.Updated = c.Created
	p.Comments = append(p.Comments, c)
	s.touch(p)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, s.commentJSON(p, c))
}

func (s *Server) comment(p *PR, id int) *Comment {
	for _, c := range p.Comments {
		if c.ID == id && !c.Deleted {
			return c
		}
	}
	return nil
}

// ownComment finds the path's comment, answering 404 or 403 (not Me's).
func (s *Server) ownComment(w http.ResponseWriter, r *http.Request, own bool) (*PR, *Comment) {
	_, p := s.prOf(w, r)
	if p == nil {
		return nil, nil
	}
	id, _ := strconv.Atoi(r.PathValue("cid"))
	c := s.comment(p, id)
	switch {
	case c == nil:
		bbError(w, 404, "Comment not found", "")
		return nil, nil
	case own && c.Account != Me:
		bbError(w, 403, "You are not the author of this comment", "")
		return nil, nil
	}
	return p, c
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b struct {
		Content struct{ Raw string } `json:"content"`
	}
	p, c := s.ownComment(w, r, true)
	if c == nil || !decode(w, r, &b) {
		return
	}
	c.Raw, c.Updated = b.Content.Raw, s.now()
	s.touch(p)
	writeJSON(w, s.commentJSON(p, c))
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, c := s.ownComment(w, r, true)
	if c == nil {
		return
	}
	c.Deleted, c.Updated = true, s.now()
	s.touch(p)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resolve(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p, c := s.ownComment(w, r, false)
		if c == nil {
			return
		}
		if c.Parent != 0 {
			bbError(w, 400, "Only top-level comments can be resolved", "")
			return
		}
		c.Resolved, c.Updated = on, s.now()
		s.touch(p)
		if !on {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"type": "comment_resolution", "user": s.user(Me), "created_on": ts(c.Updated)})
	}
}
