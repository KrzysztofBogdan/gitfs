package ovhtest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func init() {
	registrars = append(registrars, func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /1.0/auth/currentCredential", s.currentCredential)
		mux.HandleFunc("POST /1.0/auth/credential", s.newCredential)
	})
}

func (s *Server) currentCredential(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.consumers[r.Header.Get("X-Ovh-Consumer")]
	body := map[string]any{"status": c.Status, "rules": c.Rules, "applicationId": 1}
	if !c.Expiration.IsZero() {
		body["expiration"] = c.Expiration.Format("2006-01-02T15:04:05-07:00")
	}
	writeJSON(w, body)
}

// newCredential is POST /auth/credential: a pending consumer key with the
// requested rules and the page where the user approves it.
func (s *Server) newCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessRules []Rule `json:"accessRules"`
		Redirection string `json:"redirection"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.AccessRules) == 0 {
		writeError(w, http.StatusBadRequest, "accessRules required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("ck%d", len(s.consumers)+1)
	s.consumers[key] = &Consumer{Key: key, Status: "pendingValidation", Rules: body.AccessRules}
	writeJSON(w, map[string]string{"consumerKey": key, "state": "pendingValidation",
		"validationUrl": "https://eu.api.ovh.com/auth/?credentialToken=tok-" + key})
}

// Refuse marks a pending consumer key as refused by the user.
func (s *Server) Refuse(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.consumers[key]; c != nil {
		c.Status = "refused"
	}
}
