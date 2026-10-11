// Package ksef mirrors the invoices of one company in KSeF 2.0, the Polish
// national e-invoicing system, read-only (ksef spec).
package ksef

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultSince = "2026-02-01"

// apiBase maps the URL host to the API (ksef spec §3).
var apiBase = map[string]string{
	"ksef.mf.gov.pl":      "https://api.ksef.mf.gov.pl/v2",
	"demo.ksef.mf.gov.pl": "https://api-demo.ksef.mf.gov.pl/v2",
	"test.ksef.mf.gov.pl": "https://api-test.ksef.mf.gov.pl/v2",
}

// qrBase is where KOD I links point for each host (ksef pdf spec §4.4).
var qrBase = map[string]string{
	"ksef.mf.gov.pl":      "https://qr.ksef.mf.gov.pl",
	"demo.ksef.mf.gov.pl": "https://qr-demo.ksef.mf.gov.pl",
	"test.ksef.mf.gov.pl": "https://qr-test.ksef.mf.gov.pl",
}

// roles in folder order; subjectOf names KSeF's subject type of each.
var roles = []string{"issued", "received", "third-party"}

var subjectOf = map[string]string{"issued": "Subject1", "received": "Subject2", "third-party": "Subject3"}

const forms = "want ksef://ksef.mf.gov.pl/<nip>[?since=<date>&roles=issued,received,third-party&pdf=no] (demo.ksef.mf.gov.pl, test.ksef.mf.gov.pl for DEMO and TEST)"

var (
	nipRe   = regexp.MustCompile(`^[0-9]{10}$`)
	sinceRe = regexp.MustCompile(`^-(\d+)([dwm])$`)
)

// validNIP checks the 10 digits and the check digit.
func validNIP(s string) bool {
	if !nipRe.MatchString(s) {
		return false
	}
	sum := 0
	for i, w := range []int{6, 5, 7, 2, 3, 4, 5, 6, 7} {
		sum += int(s[i]-'0') * w
	}
	return sum%11 != 10 && sum%11 == int(s[9]-'0')
}

// sinceTime resolves since: "" is 2026-02-01, a date, or -<n>d|w|m before now.
func sinceTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		s = defaultSince
	}
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			return now.AddDate(0, 0, -n), nil
		case "w":
			return now.AddDate(0, 0, -7*n), nil
		default:
			return now.AddDate(0, -n, 0), nil
		}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("since=%s: want a date (2026-02-01) or -90d, -2w, -6m", s)
	}
	return t, nil
}

func roleList(v string) ([]string, error) {
	if v == "" {
		return roles, nil
	}
	want := map[string]bool{}
	for _, r := range strings.Split(v, ",") {
		r = strings.ToLower(strings.TrimSpace(r))
		if !slices.Contains(roles, r) {
			return nil, fmt.Errorf("roles: unknown role %q (want issued, received, third-party)", r)
		}
		want[r] = true
	}
	var out []string
	for _, r := range roles {
		if want[r] {
			out = append(out, r)
		}
	}
	return out, nil
}

// normalize checks a ksef:// URL and gives its canonical form.
func normalize(u *url.URL) (*url.URL, error) {
	host := strings.ToLower(u.Host)
	if u.Scheme != "ksef" || apiBase[host] == "" {
		return nil, fmt.Errorf("%s: %s", u.Redacted(), forms)
	}
	nip := strings.Trim(u.Path, "/")
	if !validNIP(nip) {
		return nil, fmt.Errorf("%s: %q is not a valid NIP (10 digits with a check digit); %s", u.Redacted(), nip, forms)
	}
	q := u.Query()
	for k := range q {
		if k != "since" && k != "roles" && k != "pdf" && k != "base" {
			return nil, fmt.Errorf("unknown parameter %q (want since, roles, pdf)", k)
		}
	}
	var parts []string
	if s := q.Get("since"); s != "" && s != defaultSince {
		if _, err := sinceTime(s, time.Now()); err != nil {
			return nil, err
		}
		parts = append(parts, "since="+s)
	}
	rs, err := roleList(q.Get("roles"))
	if err != nil {
		return nil, err
	}
	if len(rs) < len(roles) {
		parts = append(parts, "roles="+strings.Join(rs, ","))
	}
	switch p := q.Get("pdf"); p {
	case "", "yes":
	case "no":
		parts = append(parts, "pdf=no")
	default:
		return nil, fmt.Errorf("pdf=%s: want yes or no", p)
	}
	if b := q.Get("base"); b != "" {
		parts = append(parts, "base="+url.QueryEscape(b))
	}
	return &url.URL{Scheme: "ksef", Host: host, Path: "/" + nip, RawQuery: strings.Join(parts, "&")}, nil
}

// defaultDir is ksef-<nip>, ksef-test-<nip> or ksef-demo-<nip>.
func defaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "ksef"
	}
	env := ""
	if e, ok := strings.CutSuffix(n.Host, ".ksef.mf.gov.pl"); ok {
		env = e + "-"
	}
	return "ksef-" + env + strings.Trim(n.Path, "/")
}

func entryName(host, nip string) string { return "ksef:" + host + "/" + nip }

func loginHint(host, nip string) string { return "gfs auth login ksef://" + host + "/" + nip }

// EntryStore reads keyring entries (creds.Store satisfies it).
type EntryStore interface {
	GetEntry(name string) (string, error)
}

type target struct {
	host, nip, base string
	token           string
	since           string // as written; "" is the default
	roles           []string
	pdf             bool // render PDFs (ksef pdf spec §5.5)
}

// String hides the token: a target may end up in %v output.
func (t target) String() string {
	return fmt.Sprintf("ksef://%s/%s roles=%v since=%q", t.host, t.nip, t.roles, t.since)
}

// parseTarget resolves the URL and the KSeF token (GFS_KSEF_TOKEN, then the
// keyring entry ksef:<host>/<nip>).
func parseTarget(u *url.URL, getenv func(string) string, store EntryStore) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	q := n.Query()
	t := target{host: n.Host, nip: strings.Trim(n.Path, "/"), base: apiBase[n.Host], since: q.Get("since"), pdf: q.Get("pdf") != "no"}
	if b := q.Get("base"); b != "" {
		t.base = b
	}
	if t.roles, err = roleList(q.Get("roles")); err != nil {
		return target{}, err
	}
	if tok := getenv("GFS_KSEF_TOKEN"); tok != "" {
		t.token = tok
		return t, nil
	}
	tok, err := store.GetEntry(entryName(t.host, t.nip))
	if err != nil || tok == "" {
		return target{}, fmt.Errorf("no KSeF token for ksef://%s/%s: run %s", t.host, t.nip, loginHint(t.host, t.nip))
	}
	t.token = tok
	return t, nil
}
