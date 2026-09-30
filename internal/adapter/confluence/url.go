package confluence

import (
	"errors"
	"fmt"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
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
		// /wiki/spaces/KEY/… and legacy /wiki/display/KEY/… name a space; only the
		// site's own pages mean the whole site. A share link (/wiki/x/…) names a page
		// whose space gfs cannot tell, so it is refused rather than widened.
		switch {
		case len(segs) >= 3 && (segs[1] == "spaces" || segs[1] == "display"):
			q.Set("filter", segs[2])
		case len(segs) == 1 || (len(segs) == 2 && (segs[1] == "home" || segs[1] == "spaces")):
		default:
			return bad()
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
	email, token, err := atlassian.Creds(n, cfg, getenv, lk, "GFS_CONFLUENCE")
	if err != nil {
		return target{}, err
	}
	t.email, t.token = email, token
	return t, nil
}
