package confluence

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
}

func newClient(t target) *client {
	return &client{t: t, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{}}
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
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
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
	}
	return nil
}

// download streams the body of GET path into w; redirects are followed.
func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.xfer.Do(req)
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
