package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var m struct{ Message string }
	if json.Unmarshal([]byte(e.Body), &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return fmt.Sprintf("confluence: HTTP %d: %s", e.Status, msg)
}

func codeOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return strconv.Itoa(ae.Status)
	}
	return ""
}

type client struct {
	t    target
	hc   *http.Client // JSON calls, with a timeout
	xfer *http.Client // attachment bytes: no timeout, the context cancels

	sleep  func(context.Context, time.Duration) error // waits between retries
	now    func() time.Time
	onWait func(msg string) // told before each retry wait; never nil
}

func newClient(t target) *client {
	return &client{t: t, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{},
		sleep: sleepCtx, now: time.Now, onWait: func(string) {}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

const (
	maxAttempts  = 6
	maxTotalWait = 5 * time.Minute
	maxBackoff   = time.Minute
)

// send runs one request with retries. Confluence answers 429 when an account
// sends too much; the request was not processed, so any method is retried
// after Retry-After. 502/503/504 and network errors are retried only for GET:
// a write may already have been applied. build makes a fresh request per attempt.
func (c *client) send(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		msg, retry := retryReason(req.Method, status, err)
		if !retry || attempt == maxAttempts {
			return resp, err
		}
		d := c.retryWait(attempt, resp)
		if waited+d > maxTotalWait {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		c.onWait(fmt.Sprintf("%s, retrying in %s…", msg, d.Round(time.Second)))
		if err := c.sleep(ctx, d); err != nil {
			return nil, err
		}
		waited += d
	}
}

func retryReason(method string, status int, err error) (string, bool) {
	switch {
	case status == http.StatusTooManyRequests:
		return "Rate limited by Confluence", true
	case method != http.MethodGet:
		return "", false
	case err != nil:
		return "Connection error (" + err.Error() + ")", true
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return fmt.Sprintf("Confluence unavailable (HTTP %d)", status), true
	}
	return "", false
}

// retryWait is Retry-After (seconds or an HTTP date) when the server sent it,
// else exponential backoff from 1s with up to 25% jitter, capped at maxBackoff.
func (c *client) retryWait(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				return time.Duration(n) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				return max(t.Sub(c.now()), 0)
			}
		}
	}
	d := min(time.Second<<(attempt-1), maxBackoff)
	return d + time.Duration(rand.Int64N(int64(d)/4+1))
}

func (c *client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.t.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.t.email != "" || c.t.token != "" {
		req.SetBasicAuth(c.t.email, c.t.token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// apiError maps an HTTP error response: 404 wraps adapter.ErrNotFound and 409
// adapter.ErrLock, both also wrapping the *APIError.
func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ae := &APIError{Status: resp.StatusCode, Body: string(data)}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
	}
	return ae
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	resp, err := c.send(ctx, c.hc, func() (*http.Request, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := c.newRequest(ctx, method, path, body)
		if err == nil && payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *client) paginate(ctx context.Context, path string, each func(json.RawMessage) error) error {
	for path != "" {
		var resp struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		for _, r := range resp.Results {
			if err := each(r); err != nil {
				return err
			}
		}
		path = resp.Links.Next
		if path != "" && !strings.HasPrefix(path, "/wiki/") {
			path = "/wiki" + path // REST v1 links are relative to the /wiki context
		}
	}
	return nil
}

// download streams the body of GET path into w; redirects are followed.
func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, c.xfer, func() (*http.Request, error) {
		req, err := c.newRequest(ctx, http.MethodGet, path, nil)
		if err == nil {
			req.Header.Set("Accept", "*/*")
		}
		return req, err
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, apiError(resp)
	}
	return io.Copy(w, resp.Body)
}

// upload POSTs r as the multipart "file" part named filename, streaming it,
// with the XSRF header Confluence requires, and decodes the JSON reply into out.
func (c *client) upload(ctx context.Context, path, filename string, r io.Reader, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() { pw.CloseWithError(writeMultipart(mw, filename, r)) }()
	req, err := c.newRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	resp, err := c.xfer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func writeMultipart(mw *multipart.Writer, filename string, r io.Reader) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	if err := mw.WriteField("minorEdit", "true"); err != nil {
		return err
	}
	return mw.Close()
}

// VerifyToken checks email and token against the site at base and returns
// the account's display name.
func VerifyToken(ctx context.Context, base, email, token string) (string, error) {
	c := newClient(target{base: strings.TrimRight(base, "/"), email: email, token: token})
	var u struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/wiki/rest/api/user/current", nil, &u); err != nil {
		return "", err
	}
	return u.DisplayName, nil
}
