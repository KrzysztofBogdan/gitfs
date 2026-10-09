// Package kseftest is a fake KSeF 2.0 API for tests: KSeF-token login with
// real RSA-OAEP, exports with real AES-encrypted ZIP parts.
package kseftest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// NIP is the fake company's (valid) NIP.
const NIP = "6887603807"

type Server struct {
	*httptest.Server
	Token     string        // the accepted KSeF token
	AccessTTL time.Duration // access token lifetime (default 15m)

	// Exports. Set these before requests are made.
	HWM           func() time.Time // permanentStorageHwmDate; default time.Now
	TruncateAt    int              // a package holds at most this many invoices (0: no limit)
	PartSize      int              // split packages into parts of this many bytes (0: one part)
	Fail429       int              // the next n export starts answer 429 with Retry-After: 1
	ExportFail    string           // exports finish with this failure description
	ExportFailFor string           // only exports of this subject type fail ("": all)
	DamagePart    bool             // the last part of each package is served damaged
	PendingPolls  int              // status polls of each export answered "in progress" first

	mu         sync.Mutex
	requests   []string
	key        *rsa.PrivateKey
	certDER    []byte
	seq        int
	challenges map[string]int64     // challenge -> timestampMs
	auths      map[string]bool      // operation token -> the KSeF token was right
	access     map[string]time.Time // access token -> valid until
	refresh    map[string]bool
	ex         exports
}

func New() *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kseftest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	s := &Server{Token: "ksef-token-secret", AccessTTL: 15 * time.Minute, key: key, certDER: der,
		challenges: map[string]int64{}, auths: map[string]bool{}, access: map[string]time.Time{}, refresh: map[string]bool{}}
	s.ex.init()
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Requests is "METHOD /path" of every request so far, in order.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Count is how many requests start with prefix ("POST /invoices/exports").
func (s *Server) Count(prefix string) int {
	n := 0
	for _, r := range s.Requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// Decrypt undoes RSA-OAEP-SHA256 with the server key.
func (s *Server) Decrypt(b64 string) []byte {
	enc, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	out, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, s.key, enc, nil)
	if err != nil {
		return nil
	}
	return out
}

func (s *Server) next(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%d", prefix, s.seq)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]any{"title": "Unauthorized", "status": 401, "detail": "Brak lub nieprawidłowy token"})
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func tokenInfo(tok string, until time.Time) map[string]any {
	return map[string]any{"token": tok, "validUntil": until.UTC().Format(time.RFC3339Nano)}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := r.URL.Path
	s.requests = append(s.requests, r.Method+" "+p)
	switch {
	case r.Method == "GET" && p == "/security/public-key-certificates":
		c := base64.StdEncoding.EncodeToString(s.certDER)
		writeJSON(w, 200, []map[string]any{
			{"certificate": c, "usage": []string{"KsefTokenEncryption"}},
			{"certificate": c, "usage": []string{"SymmetricKeyEncryption"}},
		})
	case r.Method == "POST" && p == "/auth/challenge":
		now := time.Now()
		ch := s.next("C")
		s.challenges[ch] = now.UnixMilli()
		writeJSON(w, 200, map[string]any{"challenge": ch, "timestamp": now.UTC().Format(time.RFC3339Nano), "timestampMs": now.UnixMilli(), "clientIp": "127.0.0.1"})
	case r.Method == "POST" && p == "/auth/ksef-token":
		var req struct {
			Challenge         string
			ContextIdentifier struct{ Type, Value string }
			EncryptedToken    string
		}
		json.NewDecoder(r.Body).Decode(&req)
		ms, ok := s.challenges[req.Challenge]
		if !ok || req.ContextIdentifier.Type != "Nip" || req.ContextIdentifier.Value != NIP {
			writeJSON(w, 400, map[string]any{"exception": map[string]any{"exceptionDetailList": []map[string]any{
				{"exceptionCode": 21405, "exceptionDescription": "Błąd walidacji danych wejściowych.", "details": []string{"unknown challenge or context"}}}}})
			return
		}
		op := s.next("op")
		s.auths[op] = string(s.Decrypt(req.EncryptedToken)) == fmt.Sprintf("%s|%d", s.Token, ms)
		writeJSON(w, 202, map[string]any{"referenceNumber": s.next("AU"), "authenticationToken": tokenInfo(op, time.Now().Add(10*time.Minute))})
	case r.Method == "GET" && strings.HasPrefix(p, "/auth/") && !strings.HasPrefix(p, "/auth/token"):
		good, ok := s.auths[bearer(r)]
		switch {
		case !ok:
			unauthorized(w)
		case good:
			writeJSON(w, 200, map[string]any{"status": map[string]any{"code": 200, "description": "Uwierzytelnianie zakończone sukcesem"}})
		default:
			writeJSON(w, 200, map[string]any{"status": map[string]any{"code": 450,
				"description": "Uwierzytelnianie zakończone niepowodzeniem z powodu błędnego tokenu", "details": []string{"Invalid token"}}})
		}
	case r.Method == "POST" && p == "/auth/token/redeem":
		if !s.auths[bearer(r)] {
			unauthorized(w)
			return
		}
		delete(s.auths, bearer(r))
		acc, ref := s.next("acc"), s.next("ref")
		s.access[acc] = time.Now().Add(s.AccessTTL)
		s.refresh[ref] = true
		writeJSON(w, 200, map[string]any{"accessToken": tokenInfo(acc, s.access[acc]), "refreshToken": tokenInfo(ref, time.Now().Add(7*24*time.Hour))})
	case r.Method == "POST" && p == "/auth/token/refresh":
		if !s.refresh[bearer(r)] {
			unauthorized(w)
			return
		}
		acc := s.next("acc")
		s.access[acc] = time.Now().Add(s.AccessTTL)
		writeJSON(w, 200, map[string]any{"accessToken": tokenInfo(acc, s.access[acc])})
	case strings.HasPrefix(p, "/storage/"):
		s.ex.part(w, p)
	default:
		if until, ok := s.access[bearer(r)]; !ok || time.Now().After(until) {
			unauthorized(w)
			return
		}
		s.api(w, r)
	}
}
