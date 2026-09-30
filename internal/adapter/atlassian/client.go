package atlassian

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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// Target is where and as whom a client talks.
type Target struct{ Base, Email, Token string }

// APIError is an HTTP error response from an Atlassian API.
type APIError struct {
	Product string // "Confluence", "Jira"; the message starts with it lower-cased
	Status  int
	Body    string
	Fields  map[string]string // Jira "errors": field id -> message
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", strings.ToLower(e.Product), e.Status, e.Message())
}

// Message is the service's own text: Confluence "message", JSM
// "errorMessage", Jira "errorMessages" and "errors"; else the raw body.
func (e *APIError) Message() string {
	var m struct {
		Message       string   `json:"message"`
		ErrorMessage  string   `json:"errorMessage"`
		ErrorMessages []string `json:"errorMessages"`
	}
	var parts []string
	if json.Unmarshal([]byte(e.Body), &m) == nil {
		for _, s := range append([]string{m.Message, m.ErrorMessage}, m.ErrorMessages...) {
			if s != "" {
				parts = append(parts, s)
			}
		}
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	if len(parts) == 0 {
		return strings.TrimSpace(e.Body)
	}
	return strings.Join(parts, "; ")
}

// Code is the HTTP status of err as text, or "".
func Code(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return strconv.Itoa(ae.Status)
	}
	return ""
}

const (
	MaxAttempts  = 6
	MaxTotalWait = 5 * time.Minute
	MaxBackoff   = time.Minute
)

type Client struct {
	Target  Target
	Product string      // "Confluence", "Jira": in messages
	Header  http.Header // added to every request, e.g. X-ExperimentalApi
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
	OnWait  func(msg string) // told before each retry wait; never nil
	hc      *http.Client     // JSON calls, with a timeout
	xfer    *http.Client     // attachment bytes: no timeout, the context cancels
}

func New(t Target, product string) *Client {
	return &Client{Target: t, Product: product, Header: http.Header{}, Sleep: sleepCtx, Now: time.Now,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{}}
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

func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Target.Base+path, body)
	if err != nil {
		return nil, err
	}
	if c.Target.Email != "" || c.Target.Token != "" {
		req.SetBasicAuth(c.Target.Email, c.Target.Token)
	}
	req.Header.Set("Accept", "application/json")
	for k, vs := range c.Header {
		req.Header[k] = vs
	}
	return req, nil
}

// apiError maps an HTTP error response: 404 wraps adapter.ErrNotFound and 409
// adapter.ErrLock, both also wrapping the *APIError.
func (c *Client) apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ae := &APIError{Product: c.Product, Status: resp.StatusCode, Body: string(data)}
	var f struct {
		Errors map[string]string `json:"errors"`
	}
	if json.Unmarshal(data, &f) == nil && len(f.Errors) > 0 {
		ae.Fields = f.Errors
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
	}
	return ae
}

// send runs one request with retries. Atlassian answers 429 when an account
// sends too much; the request was not processed, so any method is retried
// after Retry-After. 502/503/504 and network errors are retried only for GET:
// a write may already have been applied. build makes a fresh request per attempt.
func (c *Client) send(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
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
		msg, retry := c.retryReason(req.Method, resp, err)
		if !retry || attempt == MaxAttempts {
			return resp, err
		}
		d := c.retryWait(attempt, resp)
		if waited+d > MaxTotalWait {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		c.OnWait(fmt.Sprintf("%s, retrying in %s…", msg, d.Round(time.Second)))
		if err := c.Sleep(ctx, d); err != nil {
			return nil, err
		}
		waited += d
	}
}

func (c *Client) retryReason(method string, resp *http.Response, err error) (string, bool) {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	switch {
	case status == http.StatusTooManyRequests:
		if why := resp.Header.Get("RateLimit-Reason"); why != "" {
			return "Rate limited by " + c.Product + " (" + why + ")", true
		}
		return "Rate limited by " + c.Product, true
	case method != http.MethodGet:
		return "", false
	case err != nil:
		return "Connection error (" + err.Error() + ")", true
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return fmt.Sprintf("%s unavailable (HTTP %d)", c.Product, status), true
	}
	return "", false
}

// retryWait is Retry-After (seconds or an HTTP date) when the server sent it,
// else the end of Jira's rate-limit window (X-RateLimit-Reset, ISO 8601, or
// Beta-Retry-After, seconds), else exponential backoff from 1s with up to 25%
// jitter, capped at MaxBackoff.
func (c *Client) retryWait(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				return time.Duration(n) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				return max(t.Sub(c.Now()), 0)
			}
		}
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if t, err := time.Parse(time.RFC3339, reset); err == nil {
				return min(max(t.Sub(c.Now()), 0), MaxTotalWait)
			}
		}
		if n, err := strconv.Atoi(resp.Header.Get("Beta-Retry-After")); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	d := min(time.Second<<(attempt-1), MaxBackoff)
	return d + time.Duration(rand.Int64N(int64(d)/4+1))
}

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
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
		req, err := c.NewRequest(ctx, method, path, body)
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
		return c.apiError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Download streams the body of GET path into w; redirects are followed.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, c.xfer, func() (*http.Request, error) {
		req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
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
		return 0, c.apiError(resp)
	}
	return io.Copy(w, resp.Body)
}

// Upload POSTs r as the multipart part "file" named filename, streaming it,
// then fields, with the XSRF header Atlassian requires, and decodes the JSON
// reply into out. Uploads are not retried: the stream cannot be replayed.
func (c *Client) Upload(ctx context.Context, path, filename string, r io.Reader, fields map[string]string, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() { pw.CloseWithError(writeMultipart(mw, filename, r, fields)) }()
	req, err := c.NewRequest(ctx, http.MethodPost, path, pr)
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
		return c.apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func writeMultipart(mw *multipart.Writer, filename string, r io.Reader, fields map[string]string) error {
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
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}
	return mw.Close()
}
