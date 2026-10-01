// Package ovhtest is a fake OVH API (/1.0) for tests: it checks every
// signature like OVH does and serves the DNS zone endpoints gfs uses.
package ovhtest

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rule is one access rule of a consumer key.
type Rule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Consumer is a consumer key and what it may do.
type Consumer struct {
	Key        string
	Status     string // "pendingValidation", "validated", "expired"
	Rules      []Rule
	Expiration time.Time
}

type Server struct {
	*httptest.Server
	AppKey, AppSecret string
	Skew              time.Duration  // the server clock runs this far ahead of the test's
	RateLimit         int            // the next RateLimit requests get 429 with Retry-After: 0
	Fail              map[string]int // "METHOD /1.0/path" -> status returned instead of handling
	Requests          []string       // "METHOD /1.0/path" of every request

	mu        sync.Mutex
	consumers map[string]*Consumer
	zones     map[string]*Zone
	seq       int64
}

func New() *Server {
	s := &Server{AppKey: "ak", AppSecret: "as", Fail: map[string]int{}, consumers: map[string]*Consumer{},
		zones: map[string]*Zone{}, seq: 5100000000}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /1.0/auth/time", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, time.Now().Add(s.Skew).Unix())
	})
	for _, register := range registrars {
		register(s, mux)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		key := r.Method + " " + r.URL.Path
		s.Requests = append(s.Requests, key)
		status, fail := s.Fail[key]
		limited := s.RateLimit > 0
		if limited {
			s.RateLimit--
		}
		s.mu.Unlock()
		switch {
		case limited:
			w.Header().Set("Retry-After", "0")
			writeError(w, http.StatusTooManyRequests, "Too many requests")
		case fail:
			writeError(w, status, "injected failure")
		default:
			if s.authorize(w, r) {
				mux.ServeHTTP(w, r)
			}
		}
	}))
	return s
}

var registrars []func(*Server, *http.ServeMux)

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"class": "Client::Error", "message": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// AddConsumer registers a validated consumer key with rules.
func (s *Server) AddConsumer(key string, rules ...Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumers[key] = &Consumer{Key: key, Status: "validated", Rules: rules}
}

// Validate marks a pending consumer key as validated (the user pressed
// "Authorize" on the validation page).
func (s *Server) Validate(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.consumers[key]; c != nil {
		c.Status = "validated"
	}
}

// Consumer returns a copy of a consumer key's state.
func (s *Server) Consumer(key string) (Consumer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.consumers[key]
	if !ok {
		return Consumer{}, false
	}
	return *c, true
}

// DNSRules is what gfs asks for: zones only.
var DNSRules = []Rule{{"GET", "/domain/zone"}, {"GET", "/domain/zone/*"}, {"POST", "/domain/zone/*"}, {"PUT", "/domain/zone/*"}, {"DELETE", "/domain/zone/*"}}

func unsigned(r *http.Request) bool {
	return r.URL.Path == "/1.0/auth/time" || (r.Method == http.MethodPost && r.URL.Path == "/1.0/auth/credential")
}

// authorize checks the application key, consumer key, timestamp and
// signature as OVH does, then the consumer key's rules.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if unsigned(r) {
		if r.URL.Path != "/1.0/auth/time" && r.Header.Get("X-Ovh-Application") != s.AppKey {
			writeError(w, http.StatusForbidden, "Invalid application key")
			return false
		}
		return true
	}
	if r.Header.Get("X-Ovh-Application") != s.AppKey {
		writeError(w, http.StatusForbidden, "Invalid application key")
		return false
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-Ovh-Timestamp"), 10, 64)
	now := time.Now().Add(s.Skew).Unix()
	if err != nil || ts < now-180 || ts > now+180 {
		writeError(w, http.StatusBadRequest, "Query out of time")
		return false
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	ck := r.Header.Get("X-Ovh-Consumer")
	url := "http://" + r.Host + r.URL.RequestURI()
	sum := sha1.Sum([]byte(s.AppSecret + "+" + ck + "+" + r.Method + "+" + url + "+" + string(body) + "+" + r.Header.Get("X-Ovh-Timestamp")))
	if r.Header.Get("X-Ovh-Signature") != "$1$"+hex.EncodeToString(sum[:]) {
		writeError(w, http.StatusBadRequest, "Invalid signature")
		return false
	}
	s.mu.Lock()
	c := s.consumers[ck]
	s.mu.Unlock()
	switch {
	case c == nil:
		writeError(w, http.StatusForbidden, "Invalid credential")
		return false
	case r.URL.Path == "/1.0/auth/currentCredential":
		return true // any known key may read itself, pending or not
	case c.Status != "validated":
		writeError(w, http.StatusForbidden, "This credential is not valid")
		return false
	case !allowed(c.Rules, r.Method, strings.TrimPrefix(r.URL.Path, "/1.0")):
		writeError(w, http.StatusForbidden, "This call has not been granted")
		return false
	}
	return true
}

func allowed(rules []Rule, method, p string) bool {
	for _, rl := range rules {
		if rl.Method != method {
			continue
		}
		if rl.Path == p {
			return true
		}
		if prefix, ok := strings.CutSuffix(rl.Path, "*"); ok && strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func (s *Server) nextID() int64 {
	s.seq++
	return s.seq
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
