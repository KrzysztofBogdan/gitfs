package confluence

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// selection is which spaces a working tree holds (site clone spec §2.2).
type selection struct {
	keys    []string // filter: exactly these spaces
	typ     string   // a Confluence space type, or all; "" (no filter) is every type but personal
	exclude []string
}

type target struct {
	base, host, email, token string
	sel                      selection
}

const urlForms = "want confluence://<site>[?filter=K1,K2&type=<space type>|all&exclude=K1,K2], confluence://<site>/<KEY>, or confluence:https://<site>/wiki/spaces/<KEY>/…"

// normalize rewrites every accepted remote form as
// confluence://[user@]<site>?<parameters> (site clone spec §2.1).
func normalize(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), urlForms) }
	out := &url.URL{Scheme: "confluence"}
	q := url.Values{}
	if u.Opaque != "" {
		// a browser URL: only /wiki/spaces/<KEY> counts; its own query (atlOrigin=…) is not ours
		in, err := url.Parse(u.Opaque)
		if err != nil || (in.Scheme != "https" && in.Scheme != "http") || in.Host == "" {
			return bad()
		}
		segs := strings.Split(strings.Trim(in.Path, "/"), "/")
		if segs[0] != "wiki" {
			return bad()
		}
		for i := 1; i+1 < len(segs); i++ {
			if segs[i] == "spaces" {
				q.Set("filter", segs[i+1])
				break
			}
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User, out.Host = in.User, in.Host
	} else {
		if u.Host == "" {
			return bad()
		}
		for k, vs := range u.Query() {
			switch k {
			case "base", "filter", "type", "exclude":
				q.Set(k, vs[len(vs)-1])
			default:
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
		}
		switch key := strings.Trim(u.Path, "/"); {
		case key == "":
		case strings.Contains(key, "/"):
			return bad()
		case q.Has("filter"):
			return nil, fmt.Errorf("bad remote %q: name the space in the path or in filter, not both", u.String())
		default:
			q.Set("filter", key)
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q)
	return out, nil
}

// encodeQuery writes the parameters in a fixed order and leaves commas readable.
func encodeQuery(q url.Values) string {
	var parts []string
	for _, k := range []string{"base", "filter", "type", "exclude"} {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v), "%2C", ","))
		}
	}
	return strings.Join(parts, "&")
}

var spaceTypes = map[string]bool{"global": true, "collaboration": true, "knowledge_base": true, "personal": true, "all": true}

func parseSelection(q url.Values) (selection, error) {
	split := func(v string) []string {
		var out []string
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	sel := selection{keys: split(q.Get("filter")), typ: q.Get("type"), exclude: split(q.Get("exclude"))}
	switch {
	case len(sel.keys) > 0 && sel.typ != "":
		return selection{}, errors.New("type and filter cannot be combined: filter already names the spaces")
	case len(sel.keys) > 0, sel.typ == "":
	case !spaceTypes[sel.typ]:
		return selection{}, fmt.Errorf("type=%s: want global, collaboration, knowledge_base, personal or all", sel.typ)
	}
	return sel, nil
}

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	sel, err := parseSelection(n.Query())
	if err != nil {
		return target{}, fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	t := target{host: n.Hostname(), sel: sel, base: "https://" + n.Host}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := n.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	email, err := resolveEmail(n, cfg, getenv, lk)
	if err != nil {
		return target{}, err
	}
	if email == "" {
		return target{}, fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (confluence://me%%40x.com@%s)", t.host, t.host, t.host)
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
