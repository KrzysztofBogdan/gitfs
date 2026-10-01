// Package dnsx holds what the DNS adapters (ovh, cloudns) share: record
// names as files show them, value checks per type, and record order.
package dnsx

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// FileName is a provider's relative name as files show it: "" is "@".
func FileName(api string) string {
	if api == "" {
		return "@"
	}
	return strings.ToLower(api)
}

// APIName is FileName reversed.
func APIName(file string) string {
	if file == "@" {
		return ""
	}
	return file
}

var labelRe = regexp.MustCompile(`^(\*|[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)$`)

// CheckName accepts "@" or dot-separated labels relative to the zone; "*"
// only as the first label. A trailing dot (an absolute name) is refused.
func CheckName(name string) error {
	if name == "@" {
		return nil
	}
	if name == "" || strings.HasSuffix(name, ".") {
		return fmt.Errorf("name %q: want a name relative to the zone, or @", name)
	}
	for i, l := range strings.Split(name, ".") {
		if !labelRe.MatchString(l) || (l == "*" && i > 0) {
			return fmt.Errorf("name %q: bad label %q", name, l)
		}
	}
	return nil
}

func CheckIPv4(v string) error {
	if a, err := netip.ParseAddr(v); err != nil || !a.Is4() {
		return fmt.Errorf("%q is not an IPv4 address", v)
	}
	return nil
}

func CheckIPv6(v string) error {
	if a, err := netip.ParseAddr(v); err != nil || !a.Is6() || a.Is4In6() {
		return fmt.Errorf("%q is not an IPv6 address", v)
	}
	return nil
}

var hostRe = regexp.MustCompile(`^([A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?\.)*[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?\.?$`)

// CheckHost accepts a host name, absolute (trailing dot) or not.
func CheckHost(v string) error {
	if v == "" || len(v) > 254 || !hostRe.MatchString(v) {
		return fmt.Errorf("%q is not a host name", v)
	}
	return nil
}

// Rec is what the CNAME rules need of a record.
type Rec struct{ Name, Type, ID string }

// CNAMEConflicts returns, per record, why it breaks the CNAME rules (nil
// when it does not): no CNAME at the apex, and a CNAME is the only record
// of its name.
func CNAMEConflicts(rs []Rec) []error {
	byName := map[string][]int{}
	for i, r := range rs {
		byName[r.Name] = append(byName[r.Name], i)
	}
	errs := make([]error, len(rs))
	for i, r := range rs {
		if r.Type != "CNAME" {
			continue
		}
		if r.Name == "@" {
			errs[i] = fmt.Errorf("CNAME at the zone apex (@) is not allowed; use ALIAS where the provider has it")
			continue
		}
		if len(byName[r.Name]) > 1 {
			for _, j := range byName[r.Name] {
				if errs[j] == nil {
					errs[j] = fmt.Errorf("%s has a CNAME and other records; a CNAME must be the only record of its name", r.Name)
				}
			}
		}
	}
	return errs
}

func labelsReversed(name string) []string {
	if name == "@" {
		return nil
	}
	ls := strings.Split(name, ".")
	slices.Reverse(ls)
	return ls
}

// Less orders <record> elements: apex first, then by labels right to left
// (api before dev.api), then type, then id numerically; an element without
// id after the ones it ties with.
func Less(a, b *xmltree.Node) bool {
	an, _ := a.Attr("name")
	bn, _ := b.Attr("name")
	if c := slices.Compare(labelsReversed(an), labelsReversed(bn)); c != 0 {
		return c < 0
	}
	at, _ := a.Attr("type")
	bt, _ := b.Attr("type")
	if at != bt {
		return at < bt
	}
	ai, aok := a.Attr("id")
	bi, bok := b.Attr("id")
	if aok != bok {
		return aok
	}
	x, errA := strconv.ParseUint(ai, 10, 64)
	y, errB := strconv.ParseUint(bi, 10, 64)
	if errA == nil && errB == nil {
		return x < y
	}
	return ai < bi
}

func xmlChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

// XMLText replaces characters XML cannot hold with U+FFFD.
func XMLText(s string) string {
	return strings.Map(func(r rune) rune {
		if xmlChar(r) {
			return r
		}
		return '�'
	}, s)
}

// Lossy reports whether a value went through XMLText's replacement: such a
// value cannot be written back as it was.
func Lossy(s string) bool { return strings.ContainsRune(s, '�') }
