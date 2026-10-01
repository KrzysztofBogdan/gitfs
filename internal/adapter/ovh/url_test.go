package ovh

import (
	"net/url"
	"strings"
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
		{"ovh://eu", "ovh://eu", ""},
		{"ovh://EU/", "ovh://eu", ""},
		{"ovh://eu?zones=B.pl.,a.com&exclude=c.com", "ovh://eu?zones=a.com,b.pl&exclude=c.com", ""},
		{"ovh://eu?services=dns", "ovh://eu", ""},
		{"ovh://ca?zones=x.ca", "ovh://ca?zones=x.ca", ""},
		{"ovh://asia", "", `unknown OVH endpoint "asia" (want eu, ca or us)`},
		{"ovh://eu/domain", "", "ovh://eu takes no path"},
		{"ovh://eu?services=vps", "", `unknown service "vps" (gfs supports: dns)`},
		{"ovh://eu?zone=a.com", "", `unknown parameter "zone" (want zones, exclude, services)`},
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
	if d := defaultDir(mustURL(t, "ovh://eu?zones=a.com")); d != "ovh-eu" {
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
	tg, err := parseTarget(mustURL(t, "ovh://eu?zones=a.com"), none, entries{"ovh:eu": `{"appKey":"k","appSecret":"s","consumerKey":"c"}`})
	if err != nil || tg.base != "https://eu.api.ovh.com/1.0" || tg.creds.ConsumerKey != "c" || tg.sel.zones[0] != "a.com" {
		t.Fatalf("%+v %v", tg, err)
	}
	env := map[string]string{"GFS_OVH_APP_KEY": "ek", "GFS_OVH_APP_SECRET": "es", "GFS_OVH_CONSUMER_KEY": "ec"}
	tg, _ = parseTarget(mustURL(t, "ovh://us"), func(k string) string { return env[k] }, entries{})
	if tg.base != "https://api.us.ovhcloud.com/1.0" || tg.creds.AppKey != "ek" {
		t.Fatalf("%+v", tg)
	}
	tg, _ = parseTarget(mustURL(t, "ovh://eu?base=http://127.0.0.1:9/1.0"), func(k string) string { return env[k] }, entries{})
	if tg.base != "http://127.0.0.1:9/1.0" {
		t.Fatal(tg.base)
	}
	_, err = parseTarget(mustURL(t, "ovh://ca"), none, entries{})
	if err == nil || err.Error() != "no credentials for ovh://ca: run gfs auth login ovh://ca" {
		t.Fatal(err)
	}
	_, err = parseTarget(mustURL(t, "ovh://eu"), none, entries{"ovh:eu": `not json`})
	if err == nil || !strings.Contains(err.Error(), "keyring entry ovh:eu is not valid; run gfs auth login ovh://eu") {
		t.Fatal(err)
	}
}

func TestSelection(t *testing.T) {
	sel := selection{zones: []string{"a.com"}}
	if !sel.wants("a.com") || sel.wants("b.com") {
		t.Fatal("zones")
	}
	sel = selection{exclude: []string{"b.com"}}
	if !sel.wants("a.com") || sel.wants("b.com") {
		t.Fatal("exclude")
	}
}
