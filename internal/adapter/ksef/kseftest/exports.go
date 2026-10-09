package kseftest

import "net/http"

// exports is the fake's invoice store and export jobs.
type exports struct{}

func (e *exports) init() {}

// part serves an export part from its pre-signed URL (no bearer token).
func (e *exports) part(w http.ResponseWriter, p string) { http.NotFound(w, nil) }

// api serves the endpoints that need an access token.
func (s *Server) api(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
