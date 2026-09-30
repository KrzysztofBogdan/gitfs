package atlassian

import (
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

type lookup struct {
	tokens map[string]string
	hosts  map[string]string
	sole   string
}

func (l lookup) Token(e string) (string, error) {
	if t, ok := l.tokens[e]; ok {
		return t, nil
	}
	return "", creds.ErrNotFound
}
func (l lookup) HostEmail(h string) (string, error) { return l.hosts[h], nil }
func (l lookup) SoleIdentity() string               { return l.sole }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestCreds(t *testing.T) {
	stored := lookup{tokens: map[string]string{"a@x.com": "ta", "h@x.com": "th", "s@x.com": "ts"},
		hosts: map[string]string{"acme.atlassian.net": "h@x.com"}, sole: "s@x.com"}
	cases := []struct {
		name, url string
		cfg       map[string]string
		env       map[string]string
		lk        lookup
		email     string
		token     string
		err       string
	}{
		{"env beats all", "jira://u%40x.com@acme.atlassian.net", map[string]string{"email": "c@x.com"},
			map[string]string{"GFS_JIRA_EMAIL": "e@x.com", "GFS_JIRA_TOKEN": "te"}, stored, "e@x.com", "te", ""},
		{"url user", "jira://a%40x.com@acme.atlassian.net", nil, nil, stored, "a@x.com", "ta", ""},
		{"remote email", "jira://acme.atlassian.net", map[string]string{"email": "a@x.com"}, nil, stored, "a@x.com", "ta", ""},
		{"host default", "jira://acme.atlassian.net", nil, nil, stored, "h@x.com", "th", ""},
		{"sole identity", "jira://other.atlassian.net", nil, nil, stored, "s@x.com", "ts", ""},
		{"env token", "jira://acme.atlassian.net", nil, map[string]string{"GFS_JIRA_TOKEN": "te"}, stored, "h@x.com", "te", ""},
		{"other prefix ignored", "jira://acme.atlassian.net", nil, map[string]string{"GFS_CONFLUENCE_TOKEN": "tc"}, stored, "h@x.com", "th", ""},
		{"no identity", "jira://other.atlassian.net", nil, nil, lookup{}, "", "", "no identity for other.atlassian.net: run gfs auth set <email> --host other.atlassian.net, or put the email in the URL (jira://me%40x.com@other.atlassian.net)"},
		{"no token", "jira://acme.atlassian.net", map[string]string{"email": "z@x.com"}, nil, stored, "", "", "no token for z@x.com: run gfs auth set z@x.com"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		email, token, err := Creds(u, c.cfg, env(c.env), c.lk, "GFS_JIRA")
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || email != c.email || token != c.token {
			t.Errorf("%s: got %q %q %v, want %q %q", c.name, email, token, err, c.email, c.token)
		}
	}
}
