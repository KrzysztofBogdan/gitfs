package jira

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want, err string }{
		{"site", "jira://acme.atlassian.net", "jira://acme.atlassian.net", ""},
		{"path key", "jira://acme.atlassian.net/GEN", "jira://acme.atlassian.net?filter=GEN", ""},
		{"lower key", "jira://acme.atlassian.net/gen", "jira://acme.atlassian.net?filter=GEN", ""},
		{"params ordered", "jira://me%40x.com@acme.atlassian.net?limit=5k&since=-90d&exclude=OLD&filter=GEN,SUP", "jira://me%40x.com@acme.atlassian.net?filter=GEN,SUP&exclude=OLD&since=-90d&limit=5k", ""},
		{"browse issue", "jira:https://acme.atlassian.net/browse/GEN-123", "jira://acme.atlassian.net?filter=GEN", ""},
		{"browse project", "jira:https://acme.atlassian.net/browse/GEN", "jira://acme.atlassian.net?filter=GEN", ""},
		{"company board", "jira:https://acme.atlassian.net/jira/software/c/projects/GEN/boards/1?selectedIssue=GEN-5", "jira://acme.atlassian.net?filter=GEN", ""},
		{"team board", "jira:https://acme.atlassian.net/jira/software/projects/SG/boards/2", "jira://acme.atlassian.net?filter=SG", ""},
		{"queues", "jira:https://acme.atlassian.net/jira/servicedesk/projects/SUP/queues/custom/1", "jira://acme.atlassian.net?filter=SUP", ""},
		{"projects page", "jira:https://acme.atlassian.net/projects/GEN/issues", "jira://acme.atlassian.net?filter=GEN", ""},
		{"your work", "jira:https://acme.atlassian.net/jira/your-work", "jira://acme.atlassian.net", ""},
		{"browser base kept", "jira:https://acme.atlassian.net/browse/GEN-1?base=http://127.0.0.1:9", "jira://acme.atlassian.net?base=http%3A%2F%2F127.0.0.1%3A9&filter=GEN", ""},
		{"wiki is not jira", "jira:https://acme.atlassian.net/wiki/spaces/HF", "", "want jira://<site>"},
		{"deep path", "jira://acme.atlassian.net/GEN/x", "", "want jira://<site>"},
		{"key twice", "jira://acme.atlassian.net/GEN?filter=SUP", "", "not both"},
		{"unknown param", "jira://acme.atlassian.net?type=all", "", `unknown parameter "type"`},
		{"bad since", "jira://acme.atlassian.net?since=yesterday", "", "since=yesterday: want YYYY-MM-DD"},
		{"bad limit", "jira://acme.atlassian.net?limit=lots", "", "limit=lots: want a number"},
		{"bad key", "jira://acme.atlassian.net?filter=1ABC", "", `"1ABC" is not a project key`},
		{"customer site", "jira+customer://ecosystem.atlassian.net", "jira+customer://ecosystem.atlassian.net", ""},
		{"customer params", "jira+customer://ecosystem.atlassian.net?status=open&desk=34,12&ownership=owned", "jira+customer://ecosystem.atlassian.net?desk=34,12&ownership=owned&status=open", ""},
		{"customer requests page", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/user/requests?page=2", "jira+customer://ecosystem.atlassian.net", ""},
		{"customer portal", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/34/group/41", "jira+customer://ecosystem.atlassian.net?desk=34", ""},
		{"customer bad portal", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/x", "", "want jira+customer://<site>"},
		{"customer path", "jira+customer://ecosystem.atlassian.net/ECOHELP", "", "want jira+customer://<site>"},
		{"customer bad ownership", "jira+customer://e.atlassian.net?ownership=mine", "", "ownership=mine: want owned or all"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := normalize(u)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%s: got %v %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestParseSelection(t *testing.T) {
	q, _ := url.ParseQuery("filter=gen,SUP&exclude=OLD&since=2023-01-01&limit=5k")
	got, err := parseSelection(q)
	want := selection{keys: []string{"GEN", "SUP"}, exclude: []string{"OLD"}, since: "2023-01-01", limit: 5000}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	q, _ = url.ParseQuery("desk=34")
	cs, err := parseCustSelection(q)
	if err != nil || !reflect.DeepEqual(cs, custSelection{desks: []string{"34"}, ownership: "all", status: "all"}) {
		t.Fatalf("%+v %v", cs, err)
	}
}

func TestSinceDate(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"2023-01-01": "2023-01-01", "-90d": "-90d", "-2w": "-2w", "-6m": "2026-03-29"} {
		if got := sinceDate(in, now); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

type lookup struct{}

func (lookup) Token(e string) (string, error) {
	if e == "me@x.com" {
		return "tok", nil
	}
	return "", creds.ErrNotFound
}
func (lookup) HostEmail(string) (string, error) { return "me@x.com", nil }
func (lookup) SoleIdentity() string             { return "" }

func TestParseTargetAndDefaultDir(t *testing.T) {
	u, _ := url.Parse("jira:https://acme.atlassian.net/browse/GEN-1")
	tg, err := parseTarget(u, map[string]string{"base": "http://b/"}, func(string) string { return "" }, lookup{})
	if err != nil || tg.base != "http://b" || tg.email != "me@x.com" || tg.token != "tok" || !reflect.DeepEqual(tg.sel.keys, []string{"GEN"}) {
		t.Fatalf("%+v %v", tg, err)
	}
	u, _ = url.Parse("jira+customer://ecosystem.atlassian.net?desk=34")
	tg, err = parseTarget(u, nil, func(k string) string { return map[string]string{"GFS_JIRA_TOKEN": "e"}[k] }, lookup{})
	if err != nil || tg.base != "https://ecosystem.atlassian.net" || tg.token != "e" || tg.csel.ownership != "all" {
		t.Fatalf("%+v %v", tg, err)
	}
	for in, want := range map[string]string{
		"jira://acme.atlassian.net/GEN":                  "gen",
		"jira://acme.atlassian.net?filter=GEN,SUP":       "acme",
		"jira+customer://ecosystem.atlassian.net?desk=3": "ecosystem",
		"jira://": "jira",
	} {
		u, _ := url.Parse(in)
		if got := defaultDir(u); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
