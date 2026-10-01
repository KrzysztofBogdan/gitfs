package ovh

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// endpoints maps the ovh:// host to the API base (DNS spec §3).
var endpoints = map[string]string{
	"eu": "https://eu.api.ovh.com/1.0",
	"ca": "https://ca.api.ovh.com/1.0",
	"us": "https://api.us.ovhcloud.com/1.0",
}

// services gfs supports, by the services= parameter.
var services = []string{"dns"}

type selection struct {
	zones, exclude []string // lower-case, no trailing dot
}

func (s selection) wants(zone string) bool {
	if slices.Contains(s.exclude, zone) {
		return false
	}
	return len(s.zones) == 0 || slices.Contains(s.zones, zone)
}

func zoneList(v string) []string {
	var out []string
	for _, z := range strings.Split(v, ",") {
		if z = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(z)), "."); z != "" {
			out = append(out, z)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// normalize checks an ovh:// URL and returns its canonical form: the
// endpoint, then zones, exclude (sorted) and base (tests) in that order.
func normalize(u *url.URL) (*url.URL, error) {
	host := strings.ToLower(u.Host)
	if _, ok := endpoints[host]; !ok {
		return nil, fmt.Errorf("unknown OVH endpoint %q (want eu, ca or us)", host)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("ovh://%s takes no path", host)
	}
	q := u.Query()
	for k := range q {
		switch k {
		case "zones", "exclude", "services", "base":
		default:
			return nil, fmt.Errorf("unknown parameter %q (want zones, exclude, services)", k)
		}
	}
	for _, sv := range strings.Split(q.Get("services"), ",") {
		if sv != "" && !slices.Contains(services, sv) {
			return nil, fmt.Errorf("unknown service %q (gfs supports: %s)", sv, strings.Join(services, ", "))
		}
	}
	var parts []string
	if z := zoneList(q.Get("zones")); len(z) > 0 {
		parts = append(parts, "zones="+strings.Join(z, ","))
	}
	if z := zoneList(q.Get("exclude")); len(z) > 0 {
		parts = append(parts, "exclude="+strings.Join(z, ","))
	}
	if b := q.Get("base"); b != "" {
		parts = append(parts, "base="+url.QueryEscape(b))
	}
	return &url.URL{Scheme: "ovh", Host: host, RawQuery: strings.Join(parts, "&")}, nil
}

func defaultDir(u *url.URL) string { return "ovh-" + strings.ToLower(u.Host) }

// entryName is the keyring entry of an endpoint (DNS spec §3.1).
func entryName(endpoint string) string { return "ovh:" + endpoint }

// EntryStore reads keyring entries (creds.Store satisfies it).
type EntryStore interface {
	GetEntry(name string) (string, error)
}

type target struct {
	endpoint, base string
	creds          Creds
	sel            selection
}

// parseTarget resolves the URL, the credentials (environment first, then
// the keyring entry) and the selection.
func parseTarget(u *url.URL, getenv func(string) string, store EntryStore) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	q := n.Query()
	t := target{endpoint: n.Host, base: endpoints[n.Host],
		sel: selection{zones: zoneList(q.Get("zones")), exclude: zoneList(q.Get("exclude"))}}
	if b := q.Get("base"); b != "" {
		t.base = b
	}
	login := "gfs auth login ovh://" + n.Host
	if k, s, c := getenv("GFS_OVH_APP_KEY"), getenv("GFS_OVH_APP_SECRET"), getenv("GFS_OVH_CONSUMER_KEY"); k != "" && s != "" && c != "" {
		t.creds = Creds{AppKey: k, AppSecret: s, ConsumerKey: c}
		return t, nil
	}
	v, err := store.GetEntry(entryName(n.Host))
	if err != nil {
		return target{}, fmt.Errorf("no credentials for ovh://%s: run %s", n.Host, login)
	}
	if json.Unmarshal([]byte(v), &t.creds) != nil || t.creds.AppKey == "" || t.creds.AppSecret == "" || t.creds.ConsumerKey == "" {
		return target{}, fmt.Errorf("keyring entry %s is not valid; run %s", entryName(n.Host), login)
	}
	return t, nil
}
