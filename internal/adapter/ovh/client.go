package ovh

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

// MaxInFlight is how many requests one client runs at once (DNS spec §6.1).
const MaxInFlight = 8

// Creds are the three OVH keys, stored as JSON in the keyring entry
// ovh:<endpoint>.
type Creds struct {
	AppKey      string `json:"appKey"`
	AppSecret   string `json:"appSecret"`
	ConsumerKey string `json:"consumerKey"`
}

// APIError is an OVH error answer: {"class": …, "message": …}.
type APIError struct {
	Status  int
	Class   string
	Message string
	Hint    string // 401/403: the command that gets new keys
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("ovh: HTTP %d: %s", e.Status, e.Message)
	if (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) && e.Hint != "" {
		msg += " (run " + e.Hint + ")"
	}
	return msg
}

// Client signs and sends OVH API requests (the AK/AS/CK scheme).
type Client struct {
	Base      string // https://eu.api.ovh.com/1.0
	Creds     Creds
	LoginHint string // e.g. "gfs auth login ovh://eu", added to 401/403 errors
	Sleep     func(context.Context, time.Duration) error
	Now       func() time.Time
	OnWait    func(msg string)

	hc     *http.Client
	sem    chan struct{}
	mu     sync.Mutex
	synced bool
	offset time.Duration // server clock minus ours
}

func NewClient(base string, cr Creds) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Creds: cr, Sleep: httpx.SleepCtx, Now: time.Now,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 60 * time.Second}, sem: make(chan struct{}, MaxInFlight)}
}

func (c *Client) retrier() *httpx.Retrier {
	return &httpx.Retrier{Service: "OVH", Sleep: c.Sleep, Now: c.Now, OnWait: c.OnWait}
}

// sync reads the server's clock once; signatures carry its time.
func (c *Client) sync(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.synced {
		return nil
	}
	resp, err := c.retrier().Send(ctx, c.hc, true, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/auth/time", nil)
	})
	if err != nil {
		return fmt.Errorf("ovh: reading the server time: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ovh: reading the server time: HTTP %d %q", resp.StatusCode, b)
	}
	c.offset = time.Unix(sec, 0).Sub(c.Now())
	c.synced = true
	return nil
}

func (c *Client) sign(method, url, body, ts string) string {
	sum := sha1.Sum([]byte(c.Creds.AppSecret + "+" + c.Creds.ConsumerKey + "+" + method + "+" + url + "+" + body + "+" + ts))
	return "$1$" + hex.EncodeToString(sum[:])
}

// Do sends a signed request; in is JSON-encoded, out decoded. 404 wraps
// adapter.ErrNotFound.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, true, method, path, in, out)
}

// DoApp sends a request with only the application key (POST
// /auth/credential, which creates a consumer key).
func (c *Client) DoApp(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, false, method, path, in, out)
}

func (c *Client) do(ctx context.Context, signed bool, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	if signed {
		if err := c.sync(ctx); err != nil {
			return err
		}
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.sem }()
	url := c.Base + path
	resp, err := c.retrier().Send(ctx, c.hc, method == http.MethodGet, func() (*http.Request, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Ovh-Application", c.Creds.AppKey)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if signed {
			c.mu.Lock()
			ts := strconv.FormatInt(c.Now().Add(c.offset).Unix(), 10)
			c.mu.Unlock()
			req.Header.Set("X-Ovh-Consumer", c.Creds.ConsumerKey)
			req.Header.Set("X-Ovh-Timestamp", ts)
			req.Header.Set("X-Ovh-Signature", c.sign(method, url, string(payload), ts))
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		ae := &APIError{Status: resp.StatusCode, Hint: c.LoginHint}
		var m struct{ Class, Message string }
		if json.Unmarshal(data, &m) == nil && m.Message != "" {
			ae.Class, ae.Message = m.Class, m.Message
		} else {
			ae.Message = strings.TrimSpace(string(data))
		}
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
		}
		return ae
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
