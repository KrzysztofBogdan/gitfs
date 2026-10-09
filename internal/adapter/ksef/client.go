package ksef

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

// Client calls KSeF 2.0: JSON over HTTPS, bearer access tokens from a KSeF
// token (ksef spec §7), the shared retry policy (httpx).
type Client struct {
	Base, NIP string
	Token     string // the KSeF token: a secret, never printed
	LoginHint string // "gfs auth login ksef://<host>/<nip>"
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	OnWait    func(msg string)
	hc        *http.Client

	keys        map[string]*rsa.PublicKey
	accessTok   string
	accessUntil time.Time
	refreshTok  string
}

func NewClient(base, nip, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), NIP: nip, Token: token, Now: time.Now, Sleep: httpx.SleepCtx,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 5 * time.Minute}}
}

// APIError is a KSeF refusal. Msg comes from KSeF; Hint is the command that
// fixes credentials, shown for 401 and 403.
type APIError struct {
	Status  int
	Op, Msg string
	Hint    string
}

func (e *APIError) Error() string {
	s := "ksef: " + e.Op + ": " + e.Msg
	if e.Hint != "" && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) {
		s += " (run " + e.Hint + ")"
	}
	return s
}

// apiMessage reads KSeF's three error shapes: ExceptionResponse (400),
// problem+json (401, 403) and the status object (429).
func apiMessage(status int, body []byte) string {
	var v struct {
		Exception *struct {
			List []struct {
				Code    int      `json:"exceptionCode"`
				Desc    string   `json:"exceptionDescription"`
				Details []string `json:"details"`
			} `json:"exceptionDetailList"`
		} `json:"exception"`
		Title  string          `json:"title"`
		Detail string          `json:"detail"`
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(body, &v) != nil {
		return fmt.Sprintf("HTTP %d", status)
	}
	if v.Exception != nil && len(v.Exception.List) > 0 {
		var parts []string
		for _, d := range v.Exception.List {
			p := fmt.Sprintf("%s (%d)", d.Desc, d.Code)
			if len(d.Details) > 0 {
				p += ": " + strings.Join(d.Details, "; ")
			}
			parts = append(parts, p)
		}
		return strings.Join(parts, "; ")
	}
	if v.Title != "" {
		if v.Detail != "" {
			return v.Title + ": " + v.Detail
		}
		return v.Title
	}
	var st struct {
		Description string   `json:"description"`
		Details     []string `json:"details"`
	}
	if json.Unmarshal(v.Status, &st) == nil && st.Description != "" {
		if len(st.Details) > 0 {
			return st.Description + ": " + strings.Join(st.Details, "; ")
		}
		return st.Description
	}
	return fmt.Sprintf("HTTP %d", status)
}

func (c *Client) retrier() *httpx.Retrier {
	return &httpx.Retrier{Service: "KSeF", Sleep: c.Sleep, Now: c.Now, OnWait: c.OnWait}
}

// opName is a URL's path, for messages: never a pre-signed query.
func opName(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return "request"
}

// do runs one request with the given bearer ("" for none). An absolute
// rawURL (export parts) is used as is.
func (c *Client) do(ctx context.Context, method, rawURL, bearer string, body []byte, read bool) (*http.Response, []byte, error) {
	u := rawURL
	if strings.HasPrefix(u, "/") {
		u = c.Base + u
	}
	resp, err := c.retrier().Send(ctx, c.hc, read, func() (*http.Request, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return req, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ksef: %s %s: %w", method, opName(u), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("ksef: %s %s: %w", method, opName(u), err)
	}
	if resp.StatusCode >= 400 {
		return resp, data, &APIError{Status: resp.StatusCode, Op: method + " " + opName(u), Msg: apiMessage(resp.StatusCode, data), Hint: c.LoginHint}
	}
	return resp, data, nil
}

// send is do with the access token when auth is set.
func (c *Client) send(ctx context.Context, method, rawURL string, auth bool, body []byte, read bool) (*http.Response, []byte, error) {
	var bearer string
	if auth {
		var err error
		if bearer, err = c.access(ctx); err != nil {
			return nil, nil, err
		}
	}
	return c.do(ctx, method, rawURL, bearer, body, read)
}

// isRead reports whether a call only reads, so transient errors may be retried.
func isRead(method, path string) bool {
	return method == http.MethodGet || strings.HasPrefix(path, "/invoices/query/metadata")
}

func (c *Client) json(ctx context.Context, method, path string, auth bool, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	_, data, err := c.send(ctx, method, path, auth, body, isRead(method, path))
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// withBearer is a JSON call with an explicit bearer (an operation or refresh
// token) instead of the access token.
func (c *Client) withBearer(ctx context.Context, method, path, bearer string, out any) error {
	_, data, err := c.do(ctx, method, path, bearer, nil, isRead(method, path))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *Client) raw(ctx context.Context, method, rawURL string, auth bool) ([]byte, error) {
	_, data, err := c.send(ctx, method, rawURL, auth, nil, true)
	return data, err
}

func b64sha(b []byte) string {
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func encryptOAEP(pub *rsa.PublicKey, b []byte) (string, error) {
	enc, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, b, nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}
