package ovhtest

import "net/http"

func init() {
	registrars = append(registrars, func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /1.0/auth/currentCredential", s.currentCredential)
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
