package jira

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// searchOverlap widens every change search, in minutes, for clock skew and
// search indexing delay (jira spec §6.2).
const searchOverlap = 10

// searchPage is the page size of every search.
const searchPage = 100

type searchReq struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

// search pages through POST /rest/api/3/search/jql, calling each for at most
// limit issues (0: all).
func (s *session) search(ctx context.Context, jql string, fields []string, limit int, each func(apiIssue) error) error {
	req := searchReq{JQL: jql, Fields: fields, MaxResults: searchPage}
	n := 0
	for {
		var resp struct {
			Issues        []apiIssue `json:"issues"`
			NextPageToken string     `json:"nextPageToken"`
			IsLast        bool       `json:"isLast"`
		}
		if err := s.c.DoRead(ctx, http.MethodPost, "/rest/api/3/search/jql", req, &resp); err != nil {
			return err
		}
		for _, is := range resp.Issues {
			if limit > 0 && n >= limit {
				return nil
			}
			if err := each(is); err != nil {
				return err
			}
			n++
		}
		if resp.IsLast || resp.NextPageToken == "" || (limit > 0 && n >= limit) {
			return nil
		}
		req.NextPageToken = resp.NextPageToken
	}
}

// quoteKeys renders project keys for a JQL "in" list.
func quoteKeys(ps []*projectMeta) string {
	q := make([]string, len(ps))
	for i, p := range ps {
		q[i] = strconv.Quote(p.Key)
	}
	return strings.Join(q, ", ")
}

// formatCursor writes the time each project was last listed:
// "GEN=2026-09-29T21:00:00Z,SUP=…" (jira spec §6.4).
func formatCursor(m map[string]time.Time) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k].UTC().Format(time.RFC3339)
	}
	return strings.Join(parts, ",")
}

// parseCursor reads formatCursor's output; anything unreadable is empty, and
// an empty cursor means list everything.
func parseCursor(c string) map[string]time.Time {
	if c == "" {
		return nil
	}
	out := map[string]time.Time{}
	for _, part := range strings.Split(c, ",") {
		k, v, ok := strings.Cut(part, "=")
		t, err := time.Parse(time.RFC3339, v)
		if !ok || err != nil || !keyRe.MatchString(k) {
			return nil
		}
		out[k] = t
	}
	return out
}

// windowMinutes is how far back a change search reaches: the time since
// since, rounded up, plus searchOverlap. JQL reads absolute dates in the
// account's time zone, so gfs only sends relative ones.
func windowMinutes(since, now time.Time) int {
	m := int(math.Ceil(now.Sub(since).Minutes()))
	return max(m, 0) + searchOverlap
}
