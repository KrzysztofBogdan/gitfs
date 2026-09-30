package jtest

import (
	"io"
	"net/http"
	"slices"
)

func init() { registrars = append(registrars, (*Server).attachRoutes) }

func (s *Server) attachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue/{id}/attachments", s.upload)
	mux.HandleFunc("DELETE /rest/api/3/attachment/{aid}", s.deleteAttachment)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		jiraError(w, 403, "XSRF check failed", nil)
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	data, _ := io.ReadAll(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	a := &Attachment{ID: s.nextID(), Filename: h.Filename, Mime: h.Header.Get("Content-Type"), Author: Author, Data: data, Created: s.now()}
	is.Attachments = append(is.Attachments, a)
	is.Updated = s.now()
	writeJSON(w, []any{map[string]any{"id": a.ID, "filename": a.Filename, "size": len(data), "mimeType": a.Mime}})
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		for i, a := range is.Attachments {
			if a.ID == r.PathValue("aid") {
				is.Attachments = slices.Delete(is.Attachments, i, i+1)
				is.Updated = s.now()
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}
