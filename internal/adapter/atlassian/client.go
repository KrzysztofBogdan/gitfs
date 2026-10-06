package atlassian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

// Target is where and as whom a client talks.
type Target struct{ Base, Email, Token string }

// APIError is an HTTP error response from an Atlassian API.
type APIError struct {
	Product string // "Confluence", "Jira"; the message starts with it lower-cased
	Status  int
	Body    string
	Fields  map[string]string // Jira "errors": field id -> message
	Hint    string            // 401: the command that gets a new token
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s: HTTP %d: %s", strings.ToLower(e.Product), e.Status, e.Message())
	if e.Status == http.StatusUnauthorized && e.Hint != "" {
		msg += " (run " + e.Hint + ")"
	}
	return msg
}

// Message is the service's own text: Confluence "message", JSM
// "errorMessage", Jira "errorMessages" and "errors"; else the raw body.
func (e *APIError) Message() string {
	var m struct {
		Message       string   `json:"message"`
		ErrorMessage  string   `json:"errorMessage"`
		ErrorMessages []string `json:"errorMessages"`
		Error         struct {
			Message string              `json:"message"`
			Detail  string              `json:"detail"`
			Fields  map[string][]string `json:"fields"`
		} `json:"error"` // Bitbucket
	}
	var parts []string
	if json.Unmarshal([]byte(e.Body), &m) == nil {
		for _, s := range append([]string{m.Message, m.ErrorMessage}, m.ErrorMessages...) {
			if s != "" {
				parts = append(parts, s)
			}
		}
		if bb := m.Error; bb.Message != "" {
			msg := bb.Message
			if bb.Detail != "" {
				msg += ": " + bb.Detail
			}
			parts = append(parts, msg)
			keys := make([]string, 0, len(bb.Fields))
			for k := range bb.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k+": "+strings.Join(bb.Fields[k], ", "))
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
	MaxAttempts  = httpx.MaxAttempts
	MaxTotalWait = httpx.MaxTotalWait
	MaxBackoff   = httpx.MaxBackoff
)

type Client struct {
	Target  Target
	Product string      // "Confluence", "Jira": in messages
	Header  http.Header // added to every request, e.g. X-ExperimentalApi
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
	OnWait  func(msg string) // told before each retry wait; never nil
	// LoginHint, e.g. "gfs auth login jira://acme.atlassian.net", is added
	// to 401 errors.
	LoginHint string
	hc        *http.Client // JSON calls, with a timeout
	xfer      *http.Client // attachment bytes: no timeout, the context cancels
	nearLimit atomic.Bool  // the last response said X-RateLimit-NearLimit: true (Bitbucket)
}

// NearLimit reports whether the last response said fewer than 20% of the
// hour's requests remain (Bitbucket).
func (c *Client) NearLimit() bool { return c.nearLimit.Load() }

func New(t Target, product string) *Client {
	return &Client{Target: t, Product: product, Header: http.Header{}, Sleep: httpx.SleepCtx, Now: time.Now,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{}}
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
	ae := &APIError{Product: c.Product, Status: resp.StatusCode, Body: string(data), Hint: c.LoginHint}
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

// send runs one request with the shared retry policy (httpx).
func (c *Client) send(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	return c.sendRead(ctx, hc, false, build)
}

// sendRead is send; read says the request only reads, whatever its method
// (Jira search is a POST), so transient errors are retried as for GET.
func (c *Client) sendRead(ctx context.Context, hc *http.Client, read bool, build func() (*http.Request, error)) (*http.Response, error) {
	r := &httpx.Retrier{Service: c.Product, Sleep: c.Sleep, Now: c.Now, OnWait: c.OnWait}
	resp, err := r.Send(ctx, hc, read, build)
	if resp != nil {
		c.nearLimit.Store(resp.Header.Get("X-RateLimit-NearLimit") == "true")
	}
	return resp, err
}

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, false, method, path, in, out)
}

// DoRead is Do for a request that only reads though its method is not GET.
func (c *Client) DoRead(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, true, method, path, in, out)
}

func (c *Client) do(ctx context.Context, read bool, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	resp, err := c.sendRead(ctx, c.hc, read, func() (*http.Request, error) {
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

var contentRangeRe = regexp.MustCompile(`^bytes (?:\d+-\d+|\*)/(\d+|\*)$`)

// Range writes bytes from..to of GET path into w (to < 0: to the end) and
// returns the full size, -1 when unknown (bitbucket spec §8.2). Nothing at or
// past from (416) and a 404 (a log not started yet) are not errors.
func (c *Client) Range(ctx context.Context, path string, from, to int64, w io.Writer) (int64, error) {
	spec := fmt.Sprintf("bytes=%d-", from)
	if to >= 0 {
		spec += strconv.FormatInt(to, 10)
	}
	resp, err := c.send(ctx, c.xfer, func() (*http.Request, error) {
		req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
		if err == nil {
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Range", spec)
		}
		return req, err
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	total := func() int64 {
		m := contentRangeRe.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if m == nil || m[1] == "*" {
			return -1
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		_, err := io.Copy(w, resp.Body)
		return total(), err
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return total(), nil
	case resp.StatusCode == http.StatusNotFound:
		return -1, nil
	case resp.StatusCode >= 400:
		return 0, c.apiError(resp)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, from); err != nil && err != io.EOF {
		return 0, err
	}
	var body io.Reader = resp.Body
	if to >= 0 {
		body = io.LimitReader(resp.Body, to-from+1)
	}
	_, err = io.Copy(w, body)
	return resp.ContentLength, err
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
