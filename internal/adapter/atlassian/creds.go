// Package atlassian holds what the Atlassian Cloud adapters share: the
// identity and token of a remote, and an HTTP client that retries when the
// site rate-limits (jira spec §2, §3.3).
package atlassian

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// Creds resolves the email and API token for remote u (credentials spec §5):
// the email from env <prefix>_EMAIL, the URL user, [remote] email, the host
// default, the only stored identity; the token from env <prefix>_TOKEN, else
// the keyring. prefix is "GFS_CONFLUENCE" or "GFS_JIRA".
func Creds(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup, prefix string) (email, token string, err error) {
	host := u.Hostname()
	if email, err = resolveEmail(u, cfg, getenv, lk, prefix); err != nil {
		return "", "", err
	}
	if email == "" {
		return "", "", fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (%s://me%%40x.com@%s)", host, host, u.Scheme, host)
	}
	if token = getenv(prefix + "_TOKEN"); token != "" {
		return email, token, nil
	}
	token, err = lk.Token(email)
	if errors.Is(err, creds.ErrNotFound) {
		return "", "", fmt.Errorf("no token for %s: run gfs auth set %s", email, email)
	}
	if err != nil {
		return "", "", err
	}
	return email, token, nil
}

func resolveEmail(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup, prefix string) (string, error) {
	if e := getenv(prefix + "_EMAIL"); e != "" {
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
