package bitbucket

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

const (
	host        = "bitbucket.org"
	defaultBase = "https://api.bitbucket.org/2.0"
	forms       = "want bitbucket://bitbucket.org/<workspace>[?filter=r1,r2&exclude=r3&since=<date>&limit=<n>&pipelineLimitSize=<size>], bitbucket://bitbucket.org/<workspace>/<repo>, or a browser URL bitbucket:https://bitbucket.org/<workspace>/<repo>/…"
	sizeForms   = "want 0, 512, 1kb, 64kb, 1mb"
	queryOrder  = "base filter exclude since limit pipelineLimitSize"
)

// selection is which repositories a tree holds and how much of each (bitbucket spec §3).
type selection struct {
	workspace      string
	repos, exclude []string
	since          string // "", "2026-01-01", "-90d", "-2w", "-6m"
	limit          int    // newest PRs and runs per repository; 0 = all
	logLimit       int64  // pipelineLimitSize in bytes
}

type target struct {
	base, email, token string
	sel                selection
}

var (
	slugRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	sinceRe = regexp.MustCompile(`^-(\d+)([dwm])$`)
	limitRe = regexp.MustCompile(`^(\d+)(k?)$`)
	sizeRe  = regexp.MustCompile(`^(\d+)(kb|mb)?$`)
)

func split(v string) []string {
	var out []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, strings.ToLower(k))
		}
	}
	return out
}

// encodeQuery writes the parameters in a fixed order and leaves commas readable.
func encodeQuery(q url.Values) string {
	var parts []string
	for _, k := range strings.Fields(queryOrder) {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v), "%2C", ","))
		}
	}
	return strings.Join(parts, "&")
}

// normalize rewrites every accepted form as
// bitbucket://[user@]bitbucket.org/<workspace>?<parameters> and checks them.
func normalize(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), forms) }
	out := &url.URL{Scheme: "bitbucket"}
	q := url.Values{}
	var segs []string
	if u.Opaque != "" {
		in, err := url.Parse(u.Opaque)
		if err != nil || (in.Scheme != "https" && in.Scheme != "http") || in.Hostname() != host {
			return bad()
		}
		segs = strings.Split(strings.Trim(in.Path, "/"), "/")
		if len(segs) > 2 {
			segs = segs[:2] // /<ws>/<repo>/pull-requests/142 names the repository
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User = in.User
	} else {
		if u.Hostname() != host {
			return bad()
		}
		for k, vs := range u.Query() {
			if !slices.Contains(strings.Fields(queryOrder), k) {
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
			q.Set(k, vs[len(vs)-1])
		}
		segs = strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) > 2 {
			return bad()
		}
		out.User = u.User
	}
	if segs[0] == "" {
		return bad()
	}
	out.Host, out.Path = host, "/"+strings.ToLower(segs[0])
	if len(segs) == 2 && segs[1] != "" {
		if q.Has("filter") {
			return nil, fmt.Errorf("bad remote %q: name the repository in the path or in filter, not both", u.String())
		}
		q.Set("filter", strings.ToLower(segs[1]))
	}
	out.RawQuery = encodeQuery(q)
	if _, err := parseSelection(out); err != nil {
		return nil, fmt.Errorf("bad remote %q: %w", out.String(), err)
	}
	return out, nil
}

// parseSelection reads a normalised URL.
func parseSelection(n *url.URL) (selection, error) {
	q := n.Query()
	sel := selection{workspace: strings.Trim(n.Path, "/"), repos: split(q.Get("filter")), exclude: split(q.Get("exclude")), since: q.Get("since")}
	for _, r := range append(append([]string(nil), sel.repos...), sel.exclude...) {
		if !slugRe.MatchString(r) {
			return selection{}, fmt.Errorf("%q is not a repository slug", r)
		}
	}
	if s := sel.since; s != "" && !sinceRe.MatchString(s) {
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return selection{}, fmt.Errorf("since=%s: want YYYY-MM-DD, or -<n>d, -<n>w, -<n>m (days, weeks, months)", s)
		}
	}
	if l := q.Get("limit"); l != "" {
		m := limitRe.FindStringSubmatch(l)
		if m == nil {
			return selection{}, fmt.Errorf("limit=%s: want a number of pull requests and runs, e.g. 500 or 5k", l)
		}
		n, _ := strconv.Atoi(m[1])
		if m[2] == "k" {
			n *= 1000
		}
		if n == 0 {
			return selection{}, errors.New("limit=0: want at least 1")
		}
		sel.limit = n
	}
	var err error
	if sel.logLimit, err = parseSize(q.Get("pipelineLimitSize")); err != nil {
		return selection{}, err
	}
	return sel, nil
}

// parseSize reads pipelineLimitSize; "" is the default, 1kb.
func parseSize(s string) (int64, error) {
	if s == "" {
		return 1024, nil
	}
	m := sizeRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return 0, fmt.Errorf("pipelineLimitSize=%s: %s", s, sizeForms)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "kb":
		n <<= 10
	case "mb":
		n <<= 20
	}
	if n > 1<<20 {
		return 0, fmt.Errorf("pipelineLimitSize=%s: at most 1mb; %s", s, sizeForms)
	}
	return n, nil
}

func (s selection) wants(repo string) bool {
	return (len(s.repos) == 0 || slices.Contains(s.repos, repo)) && !slices.Contains(s.exclude, repo)
}

// sinceTime is the earliest time since admits at now; zero for none.
func sinceTime(since string, now time.Time) time.Time {
	if since == "" {
		return time.Time{}
	}
	if m := sinceRe.FindStringSubmatch(since); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			return now.AddDate(0, 0, -n)
		case "w":
			return now.AddDate(0, 0, -7*n)
		}
		return now.AddDate(0, -n, 0)
	}
	t, _ := time.Parse("2006-01-02", since)
	return t
}

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	t := target{base: defaultBase}
	t.sel, _ = parseSelection(n)
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := n.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if t.email, t.token, err = atlassian.Creds(n, cfg, getenv, lk, "GFS_BITBUCKET"); err != nil {
		return target{}, err
	}
	return t, nil
}

// defaultDir is the repository for a one-repository selection, else the workspace.
func defaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "bitbucket"
	}
	if sel, err := parseSelection(n); err == nil && len(sel.repos) == 1 {
		return sel.repos[0]
	}
	return strings.Trim(n.Path, "/")
}
