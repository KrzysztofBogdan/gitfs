package jtest

import (
	"encoding/json"
	"net/http"
	"slices"
)

func init() { registrars = append(registrars, (*Server).subRoutes) }

// subRoutes serves comments, worklogs and links.
func (s *Server) subRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue/{id}/comment", s.addComment)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}/comment/{cid}", s.editComment)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}/comment/{cid}", s.deleteComment)
	mux.HandleFunc("POST /rest/api/3/issue/{id}/worklog", s.addWorklog)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}/worklog/{wid}", s.editWorklog)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}/worklog/{wid}", s.deleteWorklog)
	mux.HandleFunc("POST /rest/api/3/issueLink", s.addLink)
	mux.HandleFunc("DELETE /rest/api/3/issueLink/{lid}", s.deleteLink)
}

// Author is the account the fake treats as the caller.
const Author = "me"

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body       json.RawMessage `json:"body"`
		Properties []struct {
			Key   string `json:"key"`
			Value struct {
				Internal bool `json:"internal"`
			} `json:"value"`
		} `json:"properties"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	pub := true
	for _, p := range body.Properties {
		if p.Key == "sd.public.comment" && p.Value.Internal {
			pub = false
		}
	}
	c := &Comment{ID: s.nextID(), Author: Author, Body: body.Body, Created: s.now(), Updated: s.now(), Public: &pub}
	is.Comments = append(is.Comments, c)
	is.Updated = s.now()
	writeJSON(w, s.commentJSON(is, c))
}

func (s *Server) findComment(w http.ResponseWriter, r *http.Request) (*Issue, int) {
	is := s.lookup(r.PathValue("id"))
	if is != nil {
		for i, c := range is.Comments {
			if c.ID == r.PathValue("cid") {
				if c.Author != Author {
					jiraError(w, 403, "You do not have the permission to edit this comment.", nil)
					return nil, -1
				}
				return is, i
			}
		}
	}
	jiraError(w, 404, "no comment", nil)
	return nil, -1
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body json.RawMessage `json:"body"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findComment(w, r)
	if is == nil {
		return
	}
	is.Comments[i].Body, is.Comments[i].Updated = body.Body, s.now()
	is.Updated = s.now()
	writeJSON(w, s.commentJSON(is, is.Comments[i]))
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findComment(w, r)
	if is == nil {
		return
	}
	is.Comments = slices.Delete(is.Comments, i, i+1)
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}

type worklogBody struct {
	Started   string          `json:"started"`
	TimeSpent string          `json:"timeSpent"`
	Comment   json.RawMessage `json:"comment"`
}

func (s *Server) addWorklog(w http.ResponseWriter, r *http.Request) {
	var body worklogBody
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	if r.URL.Query().Get("adjustEstimate") != "auto" {
		jiraError(w, 400, "gfs always sends adjustEstimate=auto", nil)
		return
	}
	wl := &Worklog{ID: s.nextID(), Author: Author, Started: body.Started, Spent: body.TimeSpent, Comment: body.Comment,
		Created: s.now(), Updated: s.now()}
	is.Worklogs = append(is.Worklogs, wl)
	is.Updated = s.now()
	writeJSON(w, s.worklogJSON(wl))
}

func (s *Server) findWorklog(w http.ResponseWriter, r *http.Request) (*Issue, int) {
	if is := s.lookup(r.PathValue("id")); is != nil {
		for i, wl := range is.Worklogs {
			if wl.ID == r.PathValue("wid") {
				return is, i
			}
		}
	}
	jiraError(w, 404, "no worklog", nil)
	return nil, -1
}

func (s *Server) editWorklog(w http.ResponseWriter, r *http.Request) {
	var body worklogBody
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findWorklog(w, r)
	if is == nil {
		return
	}
	wl := is.Worklogs[i]
	wl.Started, wl.Spent, wl.Comment, wl.Updated = body.Started, body.TimeSpent, body.Comment, s.now()
	is.Updated = s.now()
	writeJSON(w, s.worklogJSON(wl))
}

func (s *Server) deleteWorklog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findWorklog(w, r)
	if is == nil {
		return
	}
	is.Worklogs = slices.Delete(is.Worklogs, i, i+1)
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type         struct{ Name string } `json:"type"`
		InwardIssue  struct{ Key string }  `json:"inwardIssue"`
		OutwardIssue struct{ Key string }  `json:"outwardIssue"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to := s.lookup(body.InwardIssue.Key), s.lookup(body.OutwardIssue.Key)
	if from == nil || to == nil {
		jiraError(w, 404, "Issue does not exist", nil)
		return
	}
	s.links = append(s.links, &Link{ID: s.nextID(), Type: body.Type.Name, From: from.Key, To: to.Key})
	from.Updated, to.Updated = s.now(), s.now()
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.ID == r.PathValue("lid") {
			for _, k := range []string{l.From, l.To} {
				if is := s.lookup(k); is != nil {
					is.Updated = s.now()
				}
			}
			s.links = slices.Delete(s.links, i, i+1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	jiraError(w, 404, "no link", nil)
}
