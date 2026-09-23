package confluence

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type target struct{ base, host, space, email, token string }

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string) (target, error) {
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
	t.email = getenv("GFS_CONFLUENCE_EMAIL")
	if t.email == "" && u.User != nil {
		t.email = u.User.Username()
	}
	if t.email == "" {
		t.email = cfg["email"]
	}
	t.token = getenv("GFS_CONFLUENCE_TOKEN")
	if t.token == "" {
		return target{}, errors.New("set GFS_CONFLUENCE_TOKEN to an Atlassian API token")
	}
	if t.email == "" {
		return target{}, errors.New("set GFS_CONFLUENCE_EMAIL, put the email in the URL (confluence://me%40x.com@host/SPACE), or set [remote] email")
	}
	return t, nil
}
