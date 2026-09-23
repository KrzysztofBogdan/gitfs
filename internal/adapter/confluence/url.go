package confluence

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

type target struct{ base, host, space, email, token string }

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	space := strings.Trim(u.Path, "/")
	if u.Host == "" || space == "" || strings.Contains(space, "/") {
		return target{}, fmt.Errorf("bad remote %q: want confluence://<host>/<SPACEKEY>", u.String())
	}
	t := target{host: u.Hostname(), space: space, base: "https://" + u.Host}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := u.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	email, err := resolveEmail(u, cfg, getenv, lk)
	if err != nil {
		return target{}, err
	}
	if email == "" {
		return target{}, fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (confluence://me%%40x.com@%s/%s)", t.host, t.host, t.host, space)
	}
	t.email = email
	if t.token = getenv("GFS_CONFLUENCE_TOKEN"); t.token == "" {
		tok, err := lk.Token(email)
		if errors.Is(err, creds.ErrNotFound) {
			return target{}, fmt.Errorf("no token for %s: run gfs auth set %s", email, email)
		}
		if err != nil {
			return target{}, err
		}
		t.token = tok
	}
	return t, nil
}

// resolveEmail follows credentials spec §5: env, URL user, [remote] email,
// host default, sole identity.
func resolveEmail(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (string, error) {
	if e := getenv("GFS_CONFLUENCE_EMAIL"); e != "" {
		return e, nil
	}
	if u.User != nil && u.User.Username() != "" {
		return u.User.Username(), nil
	}
	if e := cfg["email"]; e != "" {
		return e, nil
	}
	if e, err := lk.HostEmail(u.Hostname()); err != nil || e != "" {
		return e, err
	}
	return lk.SoleIdentity(), nil
}
