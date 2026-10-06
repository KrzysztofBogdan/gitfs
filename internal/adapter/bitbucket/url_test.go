package bitbucket

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
		{"workspace", "bitbucket://bitbucket.org/acme", "bitbucket://bitbucket.org/acme", ""},
		{"repo path", "bitbucket://bitbucket.org/acme/api", "bitbucket://bitbucket.org/acme?filter=api", ""},
		{"repo path upper", "bitbucket://bitbucket.org/Acme/API", "bitbucket://bitbucket.org/acme?filter=api", ""},
		{"params ordered", "bitbucket://me%40x.com@bitbucket.org/acme?pipelineLimitSize=64kb&limit=5k&since=-90d&exclude=old&filter=api,web",
			"bitbucket://me%40x.com@bitbucket.org/acme?filter=api,web&exclude=old&since=-90d&limit=5k&pipelineLimitSize=64kb", ""},
		{"browser PR", "bitbucket:https://bitbucket.org/acme/api/pull-requests/142", "bitbucket://bitbucket.org/acme?filter=api", ""},
		{"browser run", "bitbucket:https://bitbucket.org/acme/api/pipelines/results/1873", "bitbucket://bitbucket.org/acme?filter=api", ""},
		{"browser workspace", "bitbucket:https://bitbucket.org/acme/", "bitbucket://bitbucket.org/acme", ""},
		{"browser base kept", "bitbucket:https://bitbucket.org/acme/api?base=http://127.0.0.1:9", "bitbucket://bitbucket.org/acme?base=http%3A%2F%2F127.0.0.1%3A9&filter=api", ""},
		{"no workspace", "bitbucket://bitbucket.org/", "", "want bitbucket://bitbucket.org/<workspace>"},
		{"other host", "bitbucket://example.com/acme", "", "want bitbucket://bitbucket.org/<workspace>"},
		{"deep path", "bitbucket://bitbucket.org/acme/api/x", "", "want bitbucket://bitbucket.org/<workspace>"},
		{"repo twice", "bitbucket://bitbucket.org/acme/api?filter=web", "", "not both"},
		{"unknown param", "bitbucket://bitbucket.org/acme?colour=red", "", `unknown parameter "colour"`},
		{"bad size", "bitbucket://bitbucket.org/acme?pipelineLimitSize=2gb", "", "pipelineLimitSize=2gb: want"},
		{"bad since", "bitbucket://bitbucket.org/acme?since=yesterday", "", "since=yesterday: want YYYY-MM-DD"},
		{"bad limit", "bitbucket://bitbucket.org/acme?limit=lots", "", "limit=lots: want a number"},
		{"bad repo", "bitbucket://bitbucket.org/acme?filter=a/b", "", `"a/b" is not a repository slug`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			n, err := normalize(u)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || n.String() != c.want {
				t.Fatalf("%v %v, want %s", n, err, c.want)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"": 1024, "0": 0, "512": 512, "1kb": 1024, "64KB": 65536, "1mb": 1 << 20} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"2gb", "x", "-1", "1.5kb"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestSelectionAndDir(t *testing.T) {
	u, _ := url.Parse("bitbucket://bitbucket.org/acme?filter=api,web&exclude=web&since=-2w&limit=5k&pipelineLimitSize=0")
	n, _ := normalize(u)
	sel, err := parseSelection(n)
	want := selection{workspace: "acme", repos: []string{"api", "web"}, exclude: []string{"web"}, since: "-2w", limit: 5000, logLimit: 0}
	if err != nil || !reflect.DeepEqual(sel, want) {
		t.Fatalf("%+v %v", sel, err)
	}
	if !sel.wants("api") || sel.wants("web") || sel.wants("other") {
		t.Fatal("wants")
	}
	if all := (selection{workspace: "acme"}); !all.wants("anything") {
		t.Fatal("no filter wants all")
	}
	u1, _ := url.Parse("bitbucket://bitbucket.org/acme/api")
	u2, _ := url.Parse("bitbucket://bitbucket.org/acme")
	if defaultDir(u1) != "api" || defaultDir(u2) != "acme" {
		t.Fatal(defaultDir(u1), defaultDir(u2))
	}
}

func TestSinceTime(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"":           {},
		"-2w":        now.AddDate(0, 0, -14),
		"-90d":       now.AddDate(0, 0, -90),
		"-6m":        now.AddDate(0, -6, 0),
		"2026-01-01": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		if got := sinceTime(in, now); !got.Equal(want) {
			t.Errorf("%q: %v", in, got)
		}
	}
}

type lookup struct{}

func (lookup) Token(string) (string, error)     { return "", creds.ErrNotFound }
func (lookup) HostEmail(string) (string, error) { return "", nil }
func (lookup) SoleIdentity() string             { return "" }

func TestParseTarget(t *testing.T) {
	env := map[string]string{"GFS_BITBUCKET_EMAIL": "me@x.com", "GFS_BITBUCKET_TOKEN": "tok"}
	u, _ := url.Parse("bitbucket://bitbucket.org/acme")
	tg, err := parseTarget(u, nil, func(k string) string { return env[k] }, lookup{}, noEntries{})
	if err != nil || tg.base != "https://api.bitbucket.org/2.0" || tg.email != "me@x.com" || tg.token != "tok" || tg.sel.workspace != "acme" {
		t.Fatalf("%+v %v", tg, err)
	}
	u, _ = url.Parse("bitbucket://bitbucket.org/acme?base=http://127.0.0.1:9/2.0/")
	if tg, _ = parseTarget(u, nil, func(k string) string { return env[k] }, lookup{}, noEntries{}); tg.base != "http://127.0.0.1:9/2.0" {
		t.Fatal(tg.base)
	}
	if _, err := parseTarget(u, nil, func(string) string { return "" }, lookup{}, noEntries{}); err == nil || !strings.Contains(err.Error(), "no identity for bitbucket.org") {
		t.Fatal(err)
	}
	onlyEmail := func(k string) string { return map[string]string{"GFS_BITBUCKET_EMAIL": "Me@x.com"}[k] }
	tg, err = parseTarget(u, nil, onlyEmail, lookup{}, noEntries{"bitbucket:me@x.com": "scoped"})
	if err != nil || tg.token != "scoped" {
		t.Fatal(tg, err)
	}
	if _, err := parseTarget(u, nil, onlyEmail, lookup{}, noEntries{}); err == nil || !strings.Contains(err.Error(), "no Bitbucket token for Me@x.com: run gfs auth login bitbucket://bitbucket.org/acme") {
		t.Fatal(err)
	}
}

type noEntries map[string]string

func (e noEntries) GetEntry(name string) (string, error) {
	if v, ok := e[name]; ok {
		return v, nil
	}
	return "", creds.ErrNotFound
}
