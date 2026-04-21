// Package aurl parses gitfs URLs into adapter.URL values.
//
// The grammar is scheme://account[/path][?query] (CLI UX §5). gitfs URLs
// are not RFC 3986 URIs — the account is a single opaque token and is
// passed to adapters verbatim.
package aurl

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

// Parse parses an gitfs URL string into an adapter.URL.
func Parse(s string) (adapter.URL, error) {
	if s == "" {
		return adapter.URL{}, fmt.Errorf("empty URL")
	}

	// Scheme.
	i := strings.Index(s, "://")
	if i <= 0 {
		return adapter.URL{}, fmt.Errorf("missing scheme (expected scheme://...)")
	}
	scheme := s[:i]
	if !isLowerScheme(scheme) {
		return adapter.URL{}, fmt.Errorf("scheme %q must be lowercase a-z/0-9", scheme)
	}

	rest := s[i+3:]

	// Split off query first so path/account searches don't cross ?.
	var rawQuery string
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rawQuery = rest[q+1:]
		rest = rest[:q]
	}

	// Account is everything up to the first '/'. Path is everything after.
	var account, path string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		account = rest[:slash]
		path = rest[slash+1:]
	} else {
		account = rest
	}

	q := map[string]string{}
	if rawQuery != "" {
		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			return adapter.URL{}, fmt.Errorf("bad query: %w", err)
		}
		for k, vs := range parsed {
			if len(vs) > 0 {
				q[k] = vs[0]
			}
		}
	}

	return adapter.URL{
		Raw:     s,
		Scheme:  scheme,
		Account: account,
		Path:    path,
		Query:   q,
	}, nil
}

func isLowerScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case (r == '+' || r == '-' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// Slugify derives a default workdir name from account + path (CLI UX §5.2).
// Lowercases, replaces each run of non-alphanumerics with '-', strips
// leading/trailing '-'.
func Slugify(parts ...string) string {
	var b strings.Builder
	lastDash := true
	for _, p := range parts {
		for _, r := range p {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
				lastDash = false
			case r >= 'A' && r <= 'Z':
				b.WriteRune(r + 32)
				lastDash = false
			default:
				if !lastDash {
					b.WriteByte('-')
					lastDash = true
				}
			}
		}
		// Separate parts with '-' even if one was empty.
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	return out
}
