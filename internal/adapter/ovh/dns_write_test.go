package ovh

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh/ovhtest"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestRegistered(t *testing.T) {
	ad, u, err := adapter.ForURL("ovh://eu?zones=a.com")
	if err != nil || ad.Name() != "ovh" || ad.DefaultDir(u) != "ovh-eu" {
		t.Fatal(ad, err)
	}
	if s, err := ad.(adapter.Normalizer).Normalize(u); err != nil || s != "ovh://eu?zones=a.com" {
		t.Fatal(s, err)
	}
}

// change fetches a.com, edits a copy and resolves the actions the engine
// would send for it.
func change(t *testing.T, s *session, edit func(root *xmltree.Node)) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, "a.com")
	if err != nil {
		t.Fatal(err)
	}
	local := base.Root.Clone()
	edit(local)
	acts := changes.ResolveActions(&Adapter{}, base.Root, &envelope.Doc{Content: local}, base.Path, base.Path)
	return adapter.ApplyRequest{Local: &adapter.Resource{ID: "a.com", Path: base.Path, Root: local}, Base: base, Actions: acts, Lock: base.Version}
}

// find returns the child named name whose attribute k is v.
func find(root *xmltree.Node, name, k, v string) *xmltree.Node {
	for _, c := range root.ChildrenNamed(name) {
		if x, _ := c.Attr(k); x == v {
			return c
		}
	}
	return nil
}

func record(root *xmltree.Node, name, typ string) *xmltree.Node {
	for _, c := range root.ChildrenNamed("record") {
		n, _ := c.Attr("name")
		ty, _ := c.Attr("type")
		if n == name && ty == typ {
			return c
		}
	}
	return nil
}

func setText(n *xmltree.Node, text string) {
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

func drop(root, n *xmltree.Node) {
	root.Children = slices.DeleteFunc(root.Children, func(c *xmltree.Node) bool { return c == n })
}

func add(root *xmltree.Node, xml string) {
	n, err := xmltree.ParseString(xml)
	if err != nil {
		panic(err)
	}
	root.Children = append(root.Children, n)
}

func errsOf(rs []adapter.Result) []string {
	var out []string
	for _, r := range rs {
		if r.Err != nil {
			out = append(out, r.Action.Verb+" "+r.Action.Target+r.Action.Group+": "+r.Err.Error())
		}
	}
	return out
}

func TestApplyRecords(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(root *xmltree.Node) {
		add(root, `<record name="api" type="A" ttl="300">192.0.2.10</record>`)
		setText(record(root, "www", "CNAME"), "b.com.")
		record(root, "@", "MX").SetAttr("ttl", "3600")
		drop(root, record(root, "@", "TXT"))
	})
	if len(req.Actions) != 4 {
		t.Fatalf("%+v", req.Actions)
	}
	rs := s.Apply(bg, req)
	if e := errsOf(rs); len(e) > 0 || len(rs) != 5 || rs[4].Action.Verb != "refresh" {
		t.Fatal(e, rs)
	}
	calls := srv.Calls()
	var kinds []string
	for _, c := range calls {
		kinds = append(kinds, strings.Fields(c)[0])
	}
	if strings.Join(kinds, " ") != "DELETE PUT PUT POST POST" || !strings.HasSuffix(calls[4], "/refresh") {
		t.Fatalf("order: deletes, updates, creates, one refresh: %v", calls)
	}
	after, _ := s.Fetch(bg, "a.com")
	if record(after.Root, "api", "A") == nil || record(after.Root, "@", "TXT") != nil ||
		record(after.Root, "www", "CNAME").TextContent() != "b.com." || srv.Zone("a.com").Refreshes != 1 {
		t.Fatal(xmltree.Print(after.Root, 0))
	}
}

// A type cannot change in place: delete, then create; a failed create
// leaves a partial result so the engine keeps the record as new.
func TestApplyTypeChange(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(root *xmltree.Node) {
		r := record(root, "www", "CNAME")
		r.SetAttr("type", "A")
		setText(r, "192.0.2.7")
	})
	if rs := s.Apply(bg, req); len(errsOf(rs)) > 0 {
		t.Fatal(errsOf(rs))
	}
	after, _ := s.Fetch(bg, "a.com")
	if record(after.Root, "www", "CNAME") != nil || record(after.Root, "www", "A").TextContent() != "192.0.2.7" {
		t.Fatal(xmltree.Print(after.Root, 0))
	}
	req = change(t, s, func(root *xmltree.Node) {
		r := record(root, "www", "A")
		r.SetAttr("type", "AAAA")
		setText(r, "2001:db8::7")
	})
	srv.Fail["POST /1.0/domain/zone/a.com/record"] = 500
	rs := s.Apply(bg, req)
	if len(rs) != 2 || rs[0].Err == nil || !strings.HasPrefix(rs[0].Err.Error(), "deleted, create failed: ") || !rs[0].Partial {
		t.Fatalf("%+v", rs)
	}
	if rs[1].Action.Verb != "refresh" || rs[1].Err != nil {
		t.Fatalf("the delete still needs its refresh: %+v", rs[1])
	}
}

func TestCheckRefuses(t *testing.T) {
	cases := []struct {
		name string
		edit func(root *xmltree.Node)
		want string
	}{
		{"bad A", func(r *xmltree.Node) { add(r, `<record name="x" type="A">2001:db8::1</record>`) }, "not an IPv4 address"},
		{"bad AAAA", func(r *xmltree.Node) { add(r, `<record name="x" type="AAAA">1.2.3.4</record>`) }, "not an IPv6 address"},
		{"bad type", func(r *xmltree.Node) { add(r, `<record name="x" type="WR">https://x</record>`) }, `type "WR" is not an OVH record type`},
		{"absolute name", func(r *xmltree.Node) { add(r, `<record name="x.a.com." type="A">1.2.3.4</record>`) }, "relative to the zone"},
		{"cname apex", func(r *xmltree.Node) { add(r, `<record name="@" type="CNAME">b.com.</record>`) }, "apex"},
		{"cname shared", func(r *xmltree.Node) { add(r, `<record name="www" type="TXT">x</record>`) }, "only record of its name"},
		{"cname on redirect", func(r *xmltree.Node) { add(r, `<record name="old" type="CNAME">b.com.</record>`) }, "only record of its name"},
		{"bad MX", func(r *xmltree.Node) { add(r, `<record name="m" type="MX">mx.b.com.</record>`) }, `MX value "mx.b.com." is not "<priority> <host>"`},
		{"bad ttl", func(r *xmltree.Node) { add(r, `<record name="x" type="A" ttl="soon">1.2.3.4</record>`) }, "ttl"},
		{"lossy", func(r *xmltree.Node) { add(r, "<record name=\"x\" type=\"TXT\">a�b</record>") }, "XML cannot hold"},
		{"bad redirect type", func(r *xmltree.Node) { add(r, `<redirect name="r" type="sometimes">https://x</redirect>`) }, "redirect type"},
		{"bad dynhost", func(r *xmltree.Node) { add(r, `<dynhost name="d">nope</dynhost>`) }, "not an IPv4 address"},
		{"dnssec value", func(r *xmltree.Node) { setText(r.Child("dnssec"), "on") }, `want enabled or disabled`},
		{"soa removed", func(r *xmltree.Node) { drop(r, r.Child("soa")) }, "SOA cannot be removed"},
		{"soa number", func(r *xmltree.Node) { r.Child("soa").SetAttr("refresh", "daily") }, "refresh"},
		{"login without suffix", func(r *xmltree.Node) { add(r, `<dynhost-login name="d"/>`) }, "suffix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := site(t)
			s := testSession(t, srv, selection{})
			req := change(t, s, c.edit)
			rs := s.Check(bg, req)
			if e := strings.Join(errsOf(rs), "; "); !strings.Contains(e, c.want) {
				t.Fatalf("want %q in %q", c.want, e)
			}
			rs = s.Apply(bg, req)
			if len(errsOf(rs)) == 0 || len(srv.Calls()) != 0 {
				t.Fatalf("nothing may be sent: %v", srv.Calls())
			}
		})
	}
}

// Check passes edits it has no complaint about, with their description.
func TestCheckOK(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(r *xmltree.Node) { add(r, `<record name="api" type="A">192.0.2.1</record>`) })
	rs := s.Check(bg, req)
	if len(rs) != 1 || rs[0].Err != nil || rs[0].Detail != "create record api A 192.0.2.1" || len(srv.Calls()) != 0 {
		t.Fatalf("%+v", rs)
	}
}

func TestApplyRedirectDynHostLogin(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(root *xmltree.Node) {
		rd := root.Child("redirect")
		setText(rd, "https://a.com/newer")
		rd.SetAttr("type", "visible")
		setText(root.Child("dynhost"), "198.51.100.9")
		add(root, `<dynhost-login suffix="nas" name="nas"/>`)
		root.Child("dynhost-login").SetAttr("name", "home2")
	})
	rs := s.Apply(bg, req)
	if e := errsOf(rs); len(e) != 1 || !strings.Contains(e[0], "needs a password") {
		t.Fatalf("no terminal: %v", e)
	}
	req.Secret = func(string) (string, error) { return "s3cret-pw", nil }
	req = change(t, s, func(root *xmltree.Node) { add(root, `<dynhost-login suffix="nas" name="nas"/>`) })
	req.Secret = func(string) (string, error) { return "s3cret-pw", nil }
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	z := srv.Zone("a.com")
	var rd *ovhtest.Redirect
	for _, r := range z.Redirects {
		rd = r
	}
	if rd.Target != "https://a.com/newer" || rd.Type != "visible" || z.Logins["a.com-nas"] == nil || z.Logins["a.com-nas"].Password != "s3cret-pw" ||
		z.Logins["a.com-home"].SubDomain != "home2" {
		t.Fatalf("%+v %+v", rd, z.Logins)
	}
	for _, d := range z.DynHosts {
		if d.IP != "198.51.100.9" {
			t.Fatal(d)
		}
	}
	// a redirect's name cannot change in place: delete and create
	req = change(t, s, func(root *xmltree.Node) { root.Child("redirect").SetAttr("name", "older") })
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	after, _ := s.Fetch(bg, "a.com")
	if find(after.Root, "redirect", "name", "older") == nil || find(after.Root, "redirect", "name", "old") != nil {
		t.Fatal(xmltree.Print(after.Root, 0))
	}
}

func TestApplySOAAndDNSSEC(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(root *xmltree.Node) {
		root.Child("soa").SetAttr("refresh", "43200")
		setText(root.Child("dnssec"), "disabled")
	})
	for _, a := range req.Actions {
		if a.Warning == "" {
			t.Fatalf("%s %s must warn", a.Verb, a.Group)
		}
	}
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	z := srv.Zone("a.com")
	if z.SOA.Refresh != 43200 || z.DNSSEC != "disableInProgress" {
		t.Fatalf("%+v %s", z.SOA, z.DNSSEC)
	}
	req = change(t, s, func(root *xmltree.Node) { setText(root.Child("dnssec"), "enabled") })
	if e := strings.Join(errsOf(s.Check(bg, req)), ""); !strings.Contains(e, "disableInProgress") {
		t.Fatal(e)
	}
}

func TestDescribe(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, func(root *xmltree.Node) {
		add(root, `<record name="api" type="A" ttl="300">192.0.2.10</record>`)
		setText(record(root, "www", "CNAME"), "b.com.")
		drop(root, record(root, "@", "TXT"))
		ns := record(root, "@", "NS")
		setText(ns, "ns9.example.")
	})
	var got []string
	for _, a := range req.Actions {
		if a.Class != "dns" {
			t.Fatal(a.Class)
		}
		got = append(got, a.Detail+"|"+a.Warning)
	}
	want := []string{
		"create record api A 192.0.2.10 ttl 300|",
		"update record @ NS dns101.ovh.net. → ns9.example.|changes the zone's own NS records: a mistake makes the whole zone unreachable",
		"update record www CNAME a.com. → b.com.|",
		"delete record @ TXT v=spf1 include:mx.ovh.com -all ttl 600|",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestRefreshFailure(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	srv.Fail["POST /1.0/domain/zone/a.com/refresh"] = 500
	req := change(t, s, func(root *xmltree.Node) { add(root, `<record name="api" type="A">192.0.2.10</record>`) })
	rs := s.Apply(bg, req)
	if len(rs) != 2 || rs[0].Err != nil || rs[1].Action.Verb != "refresh" || rs[1].Err == nil {
		t.Fatalf("%+v", rs)
	}
	_ = errors.New
}
