package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
)

// APIError is the shared Atlassian error type; its text still starts "confluence: HTTP …".
type APIError = atlassian.APIError

func codeOf(err error) string { return atlassian.Code(err) }

// client is the shared Atlassian client plus Confluence's own pagination.
type client struct {
	*atlassian.Client
	t target
}

func newClient(t target) *client {
	return &client{Client: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Confluence"), t: t}
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	return c.Do(ctx, method, path, in, out)
}

// download streams the body of GET path into w; redirects are followed.
func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error) {
	return c.Download(ctx, path, w)
}

// upload POSTs r as a new attachment version without notifying watchers.
func (c *client) upload(ctx context.Context, path, filename string, r io.Reader, out any) error {
	return c.Upload(ctx, path, filename, r, map[string]string{"minorEdit": "true"}, out)
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
