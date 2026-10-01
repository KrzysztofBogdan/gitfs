// Package cloudnstest is a fake ClouDNS API (/dns/<action>.json) for tests,
// with the real one's quirks: refusals are HTTP 200 with "status":"Failed",
// lists are objects keyed by id (or [] when empty), numbers are strings.
package cloudnstest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Server struct {
	*httptest.Server
	AuthID    string // accepted as auth-id
	SubAuthID string // accepted as sub-auth-id
	Password  string
	RateLimit int               // the next RateLimit calls answer Failed with a rate-limit text
	Fail      map[string]string // action -> statusDescription answered as Failed
	Requests  []string          // action of every call, in order

	mu       sync.Mutex
	handlers map[string]func(s *Server, f form) any
	zones    map[string]*Zone
	seq      int
}

// RateLimitText is what the fake answers when RateLimit is set; ClouDNS's
// real text is confirmed by the real-site checks.
const RateLimitText = "Too many requests. Your limit is reached, please try again later."

type form struct{ r *http.Request }

func (f form) get(k string) string { return f.r.PostFormValue(k) }

func New() *Server {
	s := &Server{AuthID: "1234", SubAuthID: "95884", Password: "pw", Fail: map[string]string{},
		handlers: map[string]func(*Server, form) any{}, zones: map[string]*Zone{}, seq: 499153688}
	for _, register := range registrars {
		register(s)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

var registrars []func(*Server)

func (s *Server) handle(action string, h func(*Server, form) any) { s.handlers[action] = h }

func failed(desc string) map[string]string {
	return map[string]string{"status": "Failed", "statusDescription": desc}
}

func success(desc string) map[string]any {
	return map[string]any{"status": "Success", "statusDescription": desc}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	action, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/dns/"), ".json")
	if r.Method != http.MethodPost || !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, action)
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	switch {
	case s.RateLimit > 0:
		s.RateLimit--
		enc.Encode(failed(RateLimitText))
		return
	case r.URL.Query().Has("auth-password") || r.URL.Query().Has("auth-id") || r.URL.Query().Has("sub-auth-id"):
		enc.Encode(failed("Credentials must be sent in the body"))
		return
	case !s.authorized(r):
		enc.Encode(failed("Invalid authentication, incorrect auth-id or auth-password."))
		return
	case s.Fail[action] != "":
		enc.Encode(failed(s.Fail[action]))
		return
	}
	h := s.handlers[action]
	if h == nil {
		enc.Encode(failed("Invalid request."))
		return
	}
	enc.Encode(h(s, form{r}))
}

func (s *Server) authorized(r *http.Request) bool {
	if r.PostFormValue("auth-password") != s.Password {
		return false
	}
	id, sub := r.PostFormValue("auth-id"), r.PostFormValue("sub-auth-id")
	return (id != "" && id == s.AuthID && sub == "") || (sub != "" && sub == s.SubAuthID && id == "")
}

func init() {
	registrars = append(registrars, func(s *Server) {
		s.handle("login", func(*Server, form) any { return success("Success login.") })
	})
}

func (s *Server) nextID() string {
	s.seq++
	return itoa(s.seq)
}
