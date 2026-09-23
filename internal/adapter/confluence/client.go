package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	t  target
	hc *http.Client
}

func newClient(t target) *client { return &client{t: t, hc: &http.Client{Timeout: 60 * time.Second}} }

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.t.base+path, body)
	if err != nil {
		return err
	}
	if c.t.email != "" || c.t.token != "" {
		req.SetBasicAuth(c.t.email, c.t.token)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		ae := &APIError{Status: resp.StatusCode, Body: string(data)}
		switch resp.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
		case http.StatusConflict:
			return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
		}
		return ae
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
