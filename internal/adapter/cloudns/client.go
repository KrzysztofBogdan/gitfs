// Package cloudns mirrors the DNS zones of a ClouDNS API user as XML files:
// cloudns://<auth-id> or cloudns://sub-<sub-auth-id> (DNS spec).
package cloudns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

// Auth is a ClouDNS API user: exactly one of AuthID and SubAuthID.
type Auth struct {
	AuthID, SubAuthID string
	Password          string
}

// APIError is a "status":"Failed" answer (HTTP 200) or an HTTP error.
type APIError struct {
	Action      string
	Description string
	Status      int    // HTTP status when the answer was not 200
	Hint        string // auth failures: the command that stores a new password
}

func (e *APIError) Error() string {
	msg := "cloudns: "
	if e.Action != "login" && e.Action != "" {
		msg += e.Action + ": "
	}
	if e.Status != 0 {
		msg += fmt.Sprintf("HTTP %d: ", e.Status)
	}
	msg += e.Description
	if e.Hint != "" && strings.Contains(e.Description, "Invalid authentication") {
		msg += " (run " + e.Hint + ")"
	}
	return msg
}

// Client calls the ClouDNS API: POST <base>/dns/<action>.json, form-encoded,
// credentials in the body only.
type Client struct {
	Base string // https://api.cloudns.net
	Auth
	LoginHint string
	Sleep     func(context.Context, time.Duration) error
	Now       func() time.Time
	OnWait    func(msg string)
	hc        *http.Client
}

func NewClient(base string, a Auth) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Auth: a, Sleep: httpx.SleepCtx, Now: time.Now,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 60 * time.Second}}
}

var readPrefixes = []string{"login", "list-", "get-", "is-", "records", "soa-details", "mail-forwards", "failover-settings"}

// isRead reports whether an action only reads, so a transient error may
// be retried.
func isRead(action string) bool {
	for _, p := range readPrefixes {
		if action == p || (strings.HasSuffix(p, "-") && strings.HasPrefix(action, p)) {
			return true
		}
	}
	return false
}

func rateLimited(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "too many") || strings.Contains(d, "limit is reached") || strings.Contains(d, "rate limit")
}

// Do calls action with params; out receives the JSON answer. A Failed
// answer is an *APIError; a rate-limit answer is waited out first.
func (c *Client) Do(ctx context.Context, action string, params url.Values, out any) error {
	data, err := c.Raw(ctx, action, params)
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("cloudns: %s: unexpected answer: %w", action, err)
		}
	}
	return nil
}

// Raw is Do returning the answer's bytes (lists may be {} or []).
func (c *Client) Raw(ctx context.Context, action string, params url.Values) ([]byte, error) {
	form := url.Values{"auth-password": {c.Password}}
	if c.SubAuthID != "" {
		form.Set("sub-auth-id", c.SubAuthID)
	} else {
		form.Set("auth-id", c.AuthID)
	}
	for k, vs := range params {
		form[k] = vs
	}
	body := form.Encode()
	r := &httpx.Retrier{Service: "ClouDNS", Sleep: c.Sleep, Now: c.Now, OnWait: c.OnWait}
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		resp, err := r.Send(ctx, c.hc, isRead(action), func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/dns/"+action+".json", strings.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Accept", "application/json")
			}
			return req, err
		})
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &APIError{Action: action, Status: resp.StatusCode, Description: strings.TrimSpace(string(data))}
		}
		var st struct {
			Status            string `json:"status"`
			StatusDescription string `json:"statusDescription"`
		}
		if json.Unmarshal(data, &st) == nil && st.Status == "Failed" {
			if rateLimited(st.StatusDescription) {
				if err := r.Pause(ctx, attempt, &waited, resp, "Rate limited by ClouDNS"); err != nil {
					return nil, &APIError{Action: action, Description: st.StatusDescription + " (" + err.Error() + ")"}
				}
				continue
			}
			return nil, &APIError{Action: action, Description: st.StatusDescription, Hint: c.LoginHint}
		}
		return data, nil
	}
}
