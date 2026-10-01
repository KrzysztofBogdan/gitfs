package dnsx

import (
	"sort"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestNames(t *testing.T) {
	if FileName("") != "@" || FileName("WWW") != "www" || APIName("@") != "" || APIName("www") != "www" {
		t.Fatal(FileName(""), FileName("WWW"), APIName("@"))
	}
	for _, ok := range []string{"@", "www", "_sip._tcp", "*", "*.dev", "a-b.c1"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "www.", "a..b", "a b", "-a", "x.*"} {
		if CheckName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValues(t *testing.T) {
	cases := []struct {
		check func(string) error
		in    string
		ok    bool
	}{
		{CheckIPv4, "203.0.113.10", true}, {CheckIPv4, "2001:db8::1", false}, {CheckIPv4, "1.2.3", false},
		{CheckIPv6, "2001:db8::1", true}, {CheckIPv6, "203.0.113.10", false},
		{CheckHost, "mx1.example.com.", true}, {CheckHost, "example", true}, {CheckHost, "a b", false}, {CheckHost, "", false},
		{CheckHost, "http://x", false},
	}
	for _, c := range cases {
		if err := c.check(c.in); (err == nil) != c.ok {
			t.Errorf("%q: %v", c.in, err)
		}
	}
}

func rec(name, typ, id string) Rec { return Rec{Name: name, Type: typ, ID: id} }

// CNAME may not sit at the apex nor share its name with other records.
func TestCNAMEConflicts(t *testing.T) {
	rs := []Rec{rec("@", "CNAME", "1"), rec("www", "CNAME", "2"), rec("www", "TXT", "3"), rec("api", "CNAME", "4")}
	errs := CNAMEConflicts(rs)
	if len(errs) != 4 || !strings.Contains(errs[0].Error(), "apex") || errs[1] == nil || errs[2] == nil || errs[3] != nil {
		t.Fatalf("%v", errs)
	}
}

// Records sort apex first, then by labels right to left, then type, then
// id numerically; new records (no id) after their equals.
func TestLess(t *testing.T) {
	var ns []*xmltree.Node
	for _, s := range []string{`www A 9`, `dev.api A 1`, `api TXT 5`, `api A 10`, `api A 2`, `@ MX 7`, `api A -`} {
		f := strings.Fields(s)
		n := &xmltree.Node{Kind: xmltree.Element, Name: "record"}
		n.SetAttr("name", f[0])
		n.SetAttr("type", f[1])
		if f[2] != "-" {
			n.SetAttr("id", f[2])
		}
		ns = append(ns, n)
	}
	sort.SliceStable(ns, func(i, j int) bool { return Less(ns[i], ns[j]) })
	var got []string
	for _, n := range ns {
		a, _ := n.Attr("name")
		b, _ := n.Attr("type")
		c, _ := n.Attr("id")
		got = append(got, a+" "+b+" "+c)
	}
	want := "@ MX 7|api A 2|api A 10|api A |api TXT 5|dev.api A 1|www A 9"
	if strings.Join(got, "|") != want {
		t.Fatal(strings.Join(got, "|"))
	}
}
