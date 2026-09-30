package jira

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// selection is which projects an agent-mode tree holds (jira spec §3.1).
type selection struct {
	keys, exclude []string
	since         string // "", "2023-01-01", "-90d", "-2w", "-6m"
	limit         int    // most recently updated issues per project; 0 = all
}

// custSelection is which requests a customer-mode tree holds (jira spec §3.2).
type custSelection struct {
	desks     []string // portal ids; none = every desk
	ownership string   // "owned" or "all"
	status    string   // "open" or "all"
}

type target struct {
	base, host, email, token string
	sel                      selection
	csel                     custSelection
}

const (
	agentForms    = "want jira://<site>[?filter=K1,K2&exclude=K3&since=<date>&limit=<n>], jira://<site>/<KEY>, or a browser URL jira:https://<site>/browse/<KEY>-<n>"
	customerForms = "want jira+customer://<site>[?desk=<id>,<id>&ownership=owned|all&status=open|all], or jira+customer:https://<site>/servicedesk/customer/…"
)

var (
	keyRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	issueRe = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)-\d+$`)
	sinceRe = regexp.MustCompile(`^-(\d+)([dwm])$`)
	limitRe = regexp.MustCompile(`^(\d+)(k?)$`)
	deskRe  = regexp.MustCompile(`^\d+$`)
)

func split(v string) []string {
	var out []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// encodeQuery writes the parameters in a fixed order and leaves commas readable.
func encodeQuery(q url.Values, order ...string) string {
	var parts []string
	for _, k := range order {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v), "%2C", ","))
		}
	}
	return strings.Join(parts, "&")
}

// browserURL parses the https URL inside jira:https://… or jira+customer:https://….
func browserURL(u *url.URL) (*url.URL, []string, bool) {
	in, err := url.Parse(u.Opaque)
	if err != nil || (in.Scheme != "https" && in.Scheme != "http") || in.Host == "" {
		return nil, nil, false
	}
	return in, strings.Split(strings.Trim(in.Path, "/"), "/"), true
}

// normalizeAgent rewrites every accepted agent-mode form as
// jira://[user@]<site>?<parameters> (jira spec §3.1).
func normalizeAgent(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), agentForms) }
	out := &url.URL{Scheme: "jira"}
	q := url.Values{}
	if u.Opaque != "" {
		in, segs, ok := browserURL(u)
		if !ok {
			return bad()
		}
		key, ok := browserProject(segs)
		if !ok {
			return bad()
		}
		if key != "" {
			q.Set("filter", key)
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User, out.Host = in.User, in.Host
	} else {
		if u.Host == "" {
			return bad()
		}
		for k, vs := range u.Query() {
			switch k {
			case "base", "filter", "exclude", "since", "limit":
				q.Set(k, vs[len(vs)-1])
			default:
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
		}
		switch key := strings.Trim(u.Path, "/"); {
		case key == "":
		case strings.Contains(key, "/"):
			return bad()
		case q.Has("filter"):
			return nil, fmt.Errorf("bad remote %q: name the project in the path or in filter, not both", u.String())
		default:
			q.Set("filter", strings.ToUpper(key))
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q, "base", "filter", "exclude", "since", "limit")
	return out, nil
}

// browserProject reads the project key from a Jira page path; "" means the
// whole site, false a path that names neither.
func browserProject(segs []string) (string, bool) {
	switch {
	case len(segs) == 2 && segs[0] == "browse":
		up := strings.ToUpper(segs[1])
		if m := issueRe.FindStringSubmatch(up); m != nil {
			return m[1], true
		}
		return up, keyRe.MatchString(up)
	case len(segs) >= 2 && segs[0] == "projects":
		up := strings.ToUpper(segs[1])
		return up, keyRe.MatchString(up)
	case segs[0] == "jira":
		for i := 1; i+1 < len(segs); i++ {
			if segs[i] == "projects" {
				up := strings.ToUpper(segs[i+1])
				return up, keyRe.MatchString(up)
			}
		}
		return "", true
	}
	return "", false
}

func parseSelection(q url.Values) (selection, error) {
	up := func(ks []string) []string {
		for i := range ks {
			ks[i] = strings.ToUpper(ks[i])
		}
		return ks
	}
	sel := selection{keys: up(split(q.Get("filter"))), exclude: up(split(q.Get("exclude"))), since: q.Get("since")}
	for _, k := range append(append([]string(nil), sel.keys...), sel.exclude...) {
		if !keyRe.MatchString(k) {
			return selection{}, fmt.Errorf("%q is not a project key", k)
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
			return selection{}, fmt.Errorf("limit=%s: want a number of issues, e.g. 5000 or 5k", l)
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
	return sel, nil
}

// sinceDate is the JQL date for since at now: relative days and weeks stay
// relative, months (which JQL lacks) become a date.
func sinceDate(since string, now time.Time) string {
	m := sinceRe.FindStringSubmatch(since)
	if m == nil {
		return since
	}
	if m[2] == "m" {
		n, _ := strconv.Atoi(m[1])
		return now.AddDate(0, -n, 0).Format("2006-01-02")
	}
	return since
}

// normalizeCustomer rewrites every accepted customer-mode form as
// jira+customer://[user@]<site>?<parameters> (jira spec §3.2).
func normalizeCustomer(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), customerForms) }
	out := &url.URL{Scheme: "jira+customer"}
	q := url.Values{}
	if u.Opaque != "" {
		in, segs, ok := browserURL(u)
		if !ok || len(segs) < 2 || segs[0] != "servicedesk" || segs[1] != "customer" {
			return bad()
		}
		if len(segs) >= 3 && segs[2] == "portal" {
			if len(segs) < 4 || !deskRe.MatchString(segs[3]) {
				return bad()
			}
			q.Set("desk", segs[3])
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User, out.Host = in.User, in.Host
	} else {
		if u.Host == "" || strings.Trim(u.Path, "/") != "" {
			return bad()
		}
		for k, vs := range u.Query() {
			switch k {
			case "base", "desk", "ownership", "status":
				q.Set(k, vs[len(vs)-1])
			default:
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q, "base", "desk", "ownership", "status")
	return out, nil
}

func parseCustSelection(q url.Values) (custSelection, error) {
	sel := custSelection{desks: split(q.Get("desk")), ownership: q.Get("ownership"), status: q.Get("status")}
	for _, d := range sel.desks {
		if !deskRe.MatchString(d) {
			return custSelection{}, fmt.Errorf("desk=%s: want portal ids, e.g. desk=34", d)
		}
	}
	switch sel.ownership {
	case "":
		sel.ownership = "all"
	case "owned", "all":
	default:
		return custSelection{}, fmt.Errorf("ownership=%s: want owned or all", sel.ownership)
	}
	switch sel.status {
	case "":
		sel.status = "all"
	case "open", "all":
	default:
		return custSelection{}, fmt.Errorf("status=%s: want open or all", sel.status)
	}
	return sel, nil
}

// normalize dispatches on the scheme and checks the parameters.
func normalize(u *url.URL) (*url.URL, error) {
	var n *url.URL
	var err error
	if u.Scheme == "jira+customer" {
		if n, err = normalizeCustomer(u); err == nil {
			_, err = parseCustSelection(n.Query())
		}
	} else {
		if n, err = normalizeAgent(u); err == nil {
			_, err = parseSelection(n.Query())
		}
	}
	if err != nil && n != nil {
		return nil, fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	return n, err
}

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	t := target{host: n.Hostname(), base: "https://" + n.Host}
	if n.Scheme == "jira+customer" {
		t.csel, _ = parseCustSelection(n.Query())
	} else {
		t.sel, _ = parseSelection(n.Query())
	}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := n.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if t.email, t.token, err = atlassian.Creds(n, cfg, getenv, lk, "GFS_JIRA"); err != nil {
		return target{}, err
	}
	return t, nil
}

// defaultDir is the project folder for a one-project agent selection, else
// the site's first host label.
func defaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "jira"
	}
	if n.Scheme == "jira" {
		if sel, err := parseSelection(n.Query()); err == nil && len(sel.keys) == 1 {
			return strings.ToLower(sel.keys[0])
		}
	}
	return strings.SplitN(n.Hostname(), ".", 2)[0]
}
