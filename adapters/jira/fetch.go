package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

// Fetch enumerates every issue in the project via paged JQL.
func (a *Adapter) Fetch(
	ctx context.Context, creds adapter.Credentials, emit adapter.Emitter,
) ([]byte, error) {
	var blob credsBlob
	if err := json.Unmarshal(creds, &blob); err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString(
		[]byte(blob.Username+":"+blob.Token))

	client := &http.Client{}
	maxUpdated := ""
	startAt := 0
	const pageSize = 100

	for {
		page, err := a.searchPage(ctx, client, auth, startAt, pageSize)
		if err != nil {
			return nil, err
		}
		for _, issue := range page.Issues {
			if err := writeIssue(emit, issue); err != nil {
				return nil, err
			}
			if u := issue.Fields.Updated; u > maxUpdated {
				maxUpdated = u
			}
		}
		startAt += len(page.Issues)
		if len(page.Issues) == 0 || startAt >= page.Total {
			break
		}
	}

	head := map[string]string{"maxUpdated": maxUpdated, "project": a.project}
	return json.Marshal(head)
}

type searchResponse struct {
	StartAt    int               `json:"startAt"`
	MaxResults int               `json:"maxResults"`
	Total      int               `json:"total"`
	Issues     []issue           `json:"issues"`
	Names      map[string]string `json:"names"`
}

type issue struct {
	Key            string         `json:"key"`
	Fields         issueFields    `json:"fields"`
	RenderedFields map[string]any `json:"renderedFields"`
	RawFields      map[string]any `json:"-"`
}

type issueFields struct {
	Summary     string       `json:"summary"`
	Status      namedField   `json:"status"`
	Priority    *namedField  `json:"priority"`
	IssueType   namedField   `json:"issuetype"`
	Assignee    *userField   `json:"assignee"`
	Reporter    *userField   `json:"reporter"`
	Labels      []string     `json:"labels"`
	Components  []namedField `json:"components"`
	FixVersions []namedField `json:"fixVersions"`
	Parent      *issueRef    `json:"parent"`
	Created     string       `json:"created"`
	Updated     string       `json:"updated"`
	Resolution  *namedField  `json:"resolutiondate,omitempty"`
	Description any          `json:"description"`
}

type namedField struct {
	Name string `json:"name"`
}

type userField struct {
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	AccountID    string `json:"accountId"`
}

type issueRef struct {
	Key string `json:"key"`
}

func (a *Adapter) searchPage(
	ctx context.Context, c *http.Client, auth string, startAt, maxResults int,
) (*searchResponse, error) {
	q := url.Values{}
	q.Set("jql", "project="+a.project)
	q.Set("fields", "*all")
	q.Set("expand", "renderedFields,names,schema")
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(maxResults))

	req, err := http.NewRequestWithContext(ctx, "GET",
		a.baseURL+"/rest/api/3/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("jira %s: %s", resp.Status, truncate(string(body), 400))
	}
	var out searchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
