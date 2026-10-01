package cloudns

import (
	"net/url"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, out, err string }{
		{"cloudns://1234", "cloudns://1234", ""},
		{"cloudns://SUB-95884/", "cloudns://sub-95884", ""},
		{"cloudns://sub-95884?zones=QA1.pl.,b.com&exclude=c.com", "cloudns://sub-95884?zones=b.com,qa1.pl&exclude=c.com", ""},
		{"cloudns://me", "", `cloudns://me: want cloudns://<auth-id> or cloudns://sub-<sub-auth-id> (numbers)`},
		{"cloudns://sub-", "", `cloudns://sub-: want cloudns://<auth-id> or cloudns://sub-<sub-auth-id> (numbers)`},
		{"cloudns://1234/zone", "", "cloudns://1234 takes no path"},
		{"cloudns://1234?user=x", "", `unknown parameter "user" (want zones, exclude)`},
	}
	for _, c := range cases {
		n, err := normalize(mustURL(t, c.in))
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%s: err %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || n.String() != c.out {
			t.Errorf("%s: %v %v, want %s", c.in, n, err, c.out)
		}
	}
	if d := defaultDir(mustURL(t, "cloudns://sub-95884")); d != "cloudns-sub-95884" {
		t.Fatal(d)
	}
}

type entries map[string]string

func (e entries) GetEntry(name string) (string, error) {
	if v, ok := e[name]; ok {
		return v, nil
	}
	return "", creds.ErrNotFound
}

func TestParseTarget(t *testing.T) {
	none := func(string) string { return "" }
	tg, err := parseTarget(mustURL(t, "cloudns://sub-95884?zones=qa1.pl"), none, entries{"cloudns:sub-95884": `{"password":"pw"}`})
	if err != nil || tg.auth.SubAuthID != "95884" || tg.auth.AuthID != "" || tg.auth.Password != "pw" || tg.base != "https://api.cloudns.net" || !tg.sel.wants("qa1.pl") {
		t.Fatalf("%+v %v", tg, err)
	}
	tg, _ = parseTarget(mustURL(t, "cloudns://1234?base=http://127.0.0.1:9"), func(k string) string {
		return map[string]string{"GFS_CLOUDNS_PASSWORD": "env"}[k]
	}, entries{})
	if tg.auth.AuthID != "1234" || tg.auth.Password != "env" || tg.base != "http://127.0.0.1:9" {
		t.Fatalf("%+v", tg)
	}
	if _, err := parseTarget(mustURL(t, "cloudns://1234"), none, entries{}); err == nil || err.Error() != "no credentials for cloudns://1234: run gfs auth login cloudns://1234" {
		t.Fatal(err)
	}
}
