package cloudns

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const defaultBase = "https://api.cloudns.net"

type selection struct{ zones, exclude []string }

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

var userRe = regexp.MustCompile(`^(sub-)?[0-9]+$`)

// normalize checks a cloudns:// URL: the host is the API user (a number for
// auth-id, sub-<number> for sub-auth-id).
func normalize(u *url.URL) (*url.URL, error) {
	host := strings.ToLower(u.Host)
	if !userRe.MatchString(host) {
		return nil, fmt.Errorf("cloudns://%s: want cloudns://<auth-id> or cloudns://sub-<sub-auth-id> (numbers)", host)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("cloudns://%s takes no path", host)
	}
	q := u.Query()
	for k := range q {
		if k != "zones" && k != "exclude" && k != "base" {
			return nil, fmt.Errorf("unknown parameter %q (want zones, exclude)", k)
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
	return &url.URL{Scheme: "cloudns", Host: host, RawQuery: strings.Join(parts, "&")}, nil
}

func defaultDir(u *url.URL) string { return "cloudns-" + strings.ToLower(u.Host) }

func entryName(host string) string { return "cloudns:" + host }

// EntryStore reads keyring entries (creds.Store satisfies it).
type EntryStore interface {
	GetEntry(name string) (string, error)
}

type target struct {
	host, base string
	auth       Auth
	sel        selection
}

// parseTarget resolves the URL, the password (GFS_CLOUDNS_PASSWORD, then
// the keyring entry cloudns:<host>) and the selection.
func parseTarget(u *url.URL, getenv func(string) string, store EntryStore) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	q := n.Query()
	t := target{host: n.Host, base: defaultBase, sel: selection{zones: zoneList(q.Get("zones")), exclude: zoneList(q.Get("exclude"))}}
	if b := q.Get("base"); b != "" {
		t.base = b
	}
	if id, ok := strings.CutPrefix(n.Host, "sub-"); ok {
		t.auth.SubAuthID = id
	} else {
		t.auth.AuthID = n.Host
	}
	login := "gfs auth login cloudns://" + n.Host
	if pw := getenv("GFS_CLOUDNS_PASSWORD"); pw != "" {
		t.auth.Password = pw
		return t, nil
	}
	v, err := store.GetEntry(entryName(n.Host))
	if err != nil {
		return target{}, fmt.Errorf("no credentials for cloudns://%s: run %s", n.Host, login)
	}
	var e struct{ Password string }
	if json.Unmarshal([]byte(v), &e) != nil || e.Password == "" {
		return target{}, fmt.Errorf("keyring entry %s is not valid; run %s", entryName(n.Host), login)
	}
	t.auth.Password = e.Password
	return t, nil
}
