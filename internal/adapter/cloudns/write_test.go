package cloudns

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestRegistered(t *testing.T) {
	ad, u, err := adapter.ForURL("cloudns://sub-95884?zones=qa1.pl")
	if err != nil || ad.Name() != "cloudns" || ad.DefaultDir(u) != "cloudns-sub-95884" {
		t.Fatal(ad, err)
	}
	if s, err := ad.(adapter.Normalizer).Normalize(u); err != nil || s != "cloudns://sub-95884?zones=qa1.pl" {
		t.Fatal(s, err)
	}
}

func change(t *testing.T, s *session, zone string, edit func(root *xmltree.Node)) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, zone)
	if err != nil {
		t.Fatal(err)
	}
	local := base.Root.Clone()
	edit(local)
	acts := changes.ResolveActions(&Adapter{}, base.Root, &envelope.Doc{Content: local}, base.Path, base.Path)
	return adapter.ApplyRequest{Local: &adapter.Resource{ID: zone, Path: base.Path, Root: local}, Base: base, Actions: acts, Lock: base.Version}
}

func byID(root *xmltree.Node, name, id string) *xmltree.Node {
	for _, c := range root.ChildrenNamed(name) {
		if v, _ := c.Attr("id"); v == id {
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
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		add(root, `<record name="www" type="A" ttl="300" geo="EUR">192.0.2.10</record>`)
		byID(root, "record", "11").SetAttr("priority", "20")
		drop(root, byID(root, "record", "17"))
	})
	rs := s.Apply(bg, req)
	if e := errsOf(rs); len(e) > 0 {
		t.Fatal(e)
	}
	if w := strings.Join(srv.Writes(), " "); w != "delete-record mod-record add-record" {
		t.Fatal(w)
	}
	z := srv.Zone("qa1.pl")
	if r := z.Records["11"]; r.Extra["priority"] != "20" || r.Record != "mxa.eu.mailgun.org" || r.TTL != "3600" {
		t.Fatalf("mod-record must send the whole record: %+v", r)
	}
	if z.Records["17"] != nil {
		t.Fatal("not deleted")
	}
	var www *struct{ Geo, TTL string }
	for _, r := range z.Records {
		if r.Host == "www" {
			www = &struct{ Geo, TTL string }{r.Geo, r.TTL}
		}
	}
	if www == nil || www.Geo != "5" || www.TTL != "300" {
		t.Fatalf("%+v", www)
	}
	for _, r := range rs {
		if r.Action.Verb == "create" && !strings.HasPrefix(r.Detail, "id=") {
			t.Fatalf("create result: %+v", r)
		}
	}
}

func TestApplyTypeChange(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		r := byID(root, "record", "16")
		r.SetAttr("type", "CNAME")
		setText(r, "api.qa1.pl")
	})
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	if srv.Zone("qa1.pl").Records["16"] != nil || strings.Join(srv.Writes(), " ") != "delete-record add-record" {
		t.Fatal(srv.Writes())
	}
	srv.Fail["add-record"] = "Invalid record"
	req = change(t, s, "qa1.pl", func(root *xmltree.Node) {
		for _, r := range root.ChildrenNamed("record") {
			if v, _ := r.Attr("name"); v == "old" {
				r.SetAttr("type", "TXT")
				setText(r, "gone")
			}
		}
	})
	rs := s.Apply(bg, req)
	if len(rs) != 1 || rs[0].Err == nil || !strings.HasPrefix(rs[0].Err.Error(), "deleted, create failed: ") || !rs[0].Partial {
		t.Fatalf("%+v", rs)
	}
}

func TestCheckRefuses(t *testing.T) {
	cases := []struct {
		name, zone string
		edit       func(root *xmltree.Node)
		want       string
	}{
		{"ttl not allowed", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="A" ttl="120">192.0.2.1</record>`) }, "ttl 120 is not one ClouDNS allows"},
		{"ttl missing", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="A">192.0.2.1</record>`) }, "ttl"},
		{"bad type", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="BOGUS" ttl="60">x</record>`) }, `type "BOGUS" is not a ClouDNS record type`},
		{"bad A", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="A" ttl="60">nope</record>`) }, "not an IPv4 address"},
		{"MX priority", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="m" type="MX" ttl="60">mx.b.com</record>`) }, "MX needs priority"},
		{"SRV port", "qa1.pl", func(r *xmltree.Node) {
			add(r, `<record name="_x._tcp" type="SRV" ttl="60" priority="0" weight="1">t.b.com</record>`)
		}, "SRV needs priority, weight and port"},
		{"unknown geo", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="A" ttl="60" geo="MARS">192.0.2.1</record>`) }, `geo "MARS" is not in .geodns.xml`},
		{"geo on TXT", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="x" type="TXT" ttl="60" geo="EUR">t</record>`) }, "geo only on A, AAAA, CNAME, NAPTR, SRV, ALIAS"},
		{"geo in plain zone", "b.com", func(r *xmltree.Node) { add(r, `<record name="x" type="A" ttl="60" geo="EUR">192.0.2.1</record>`) }, "not a GeoDNS zone"},
		{"cname shared", "qa1.pl", func(r *xmltree.Node) { add(r, `<record name="api" type="CNAME" ttl="60">b.com</record>`) }, "only record of its name"},
		{"failover unknown record", "qa1.pl", func(r *xmltree.Node) { add(r, `<failover record="999" check-type="1"/>`) }, "record 999 is not a record of this zone"},
		{"failover without check type", "qa1.pl", func(r *xmltree.Node) { add(r, `<failover record="13"/>`) }, "check-type"},
		{"mail forward", "qa1.pl", func(r *xmltree.Node) { add(r, `<mail-forward box="x" destination="nobody"/>`) }, "destination"},
		{"ds edited", "qa1.pl", func(r *xmltree.Node) { setText(r.Child("dnssec").Child("ds"), "FFFF") }, "DS records are read-only"},
		{"active", "qa1.pl", func(r *xmltree.Node) { setText(r.Child("active"), "maybe") }, "want true or false"},
		{"soa removed", "qa1.pl", func(r *xmltree.Node) { drop(r, r.Child("soa")) }, "SOA cannot be removed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := site(t)
			s := testSession(t, srv, selection{})
			req := change(t, s, c.zone, c.edit)
			if e := strings.Join(errsOf(s.Check(bg, req)), "; "); !strings.Contains(e, c.want) {
				t.Fatalf("want %q in %q", c.want, e)
			}
			if rs := s.Apply(bg, req); len(errsOf(rs)) == 0 || len(srv.Writes()) != 0 {
				t.Fatalf("nothing may be sent: %v", srv.Writes())
			}
		})
	}
}

// Two CNAMEs of one name are fine when their GeoDNS locations differ.
func TestGeoCNAMEs(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(r *xmltree.Node) {
		add(r, `<record name="cdn" type="CNAME" ttl="60" geo="EUR">eu.cdn.net</record>`)
		add(r, `<record name="cdn" type="CNAME" ttl="60" geo="NAM">us.cdn.net</record>`)
	})
	if e := errsOf(s.Check(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
}

func TestApplyFailover(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		f := root.Child("failover")
		f.Children = f.Children[:1] // drop the second backup
		add(root, `<failover record="13" check-type="1" host="api.qa1.pl"><backup>198.51.100.9</backup></failover>`)
	})
	if len(req.Actions) != 1 || req.Actions[0].Group != "failover" {
		t.Fatalf("%+v", req.Actions)
	}
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	z := srv.Zone("qa1.pl")
	if f := z.Failover["12"]; f["backup_ip_1"] != "198.51.100.7" || f["backup_ip_2"] != "" || f["check_type"] != "17" {
		t.Fatalf("modify: %+v", f)
	}
	if f := z.Failover["13"]; f["backup_ip_1"] != "198.51.100.9" || f["check_type"] != "1" {
		t.Fatalf("activate: %+v", f)
	}
	req = change(t, s, "qa1.pl", func(root *xmltree.Node) {
		for _, f := range root.ChildrenNamed("failover") {
			drop(root, f)
		}
	})
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 || len(z.Failover) != 0 {
		t.Fatal(e, z.Failover)
	}
	if !strings.Contains(req.Actions[0].Detail, "deactivate failover on record 12") {
		t.Fatal(req.Actions[0].Detail)
	}
}

// A deleted record's failover goes with it: no deactivate is sent.
func TestDeleteRecordWithFailover(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		drop(root, byID(root, "record", "12"))
		drop(root, root.Child("failover"))
	})
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 || strings.Join(srv.Writes(), " ") != "delete-record" {
		t.Fatal(e, srv.Writes())
	}
}

func TestApplyZoneSettings(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		root.Child("soa").SetAttr("refresh", "3600")
		root.Child("dnssec").SetAttr("status", "disabled")
		root.Child("dnssec").Children = nil
		setText(root.Child("active"), "false")
		mf := root.Child("mail-forward")
		mf.SetAttr("destination", "other@example.com")
		add(root, `<mail-forward box="sales" destination="s@example.com"/>`)
	})
	for _, a := range req.Actions {
		if a.Class != "dns" {
			t.Fatal(a)
		}
		if (a.Group == "dnssec" || a.Group == "soa" || a.Group == "active") && a.Warning == "" {
			t.Fatalf("%s must warn", a.Group)
		}
	}
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	z := srv.Zone("qa1.pl")
	if z.SOA.Refresh != "3600" || z.SOA.Retry != "1800" || z.DNSSEC || z.Active || len(z.MailForwards) != 2 {
		t.Fatalf("%+v dnssec=%v active=%v %d", z.SOA, z.DNSSEC, z.Active, len(z.MailForwards))
	}
}

func TestDescribe(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		add(root, `<record name="www" type="A" ttl="300" geo="EUR">192.0.2.10</record>`)
		byID(root, "record", "11").SetAttr("priority", "20")
		drop(root, byID(root, "record", "17"))
	})
	var got []string
	for _, a := range req.Actions {
		got = append(got, a.Detail)
	}
	want := "create record www A 192.0.2.10 ttl 300 geo EUR|update record @ MX mxa.eu.mailgun.org ttl 3600 priority 10 → record @ MX mxa.eu.mailgun.org ttl 3600 priority 20|delete record @ TXT v=spf1 include:mailgun.org ~all ttl 3600"
	if strings.Join(got, "|") != want {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// .geodns.xml is read-only.
func TestGeoFileReadOnly(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	g, _ := s.Fetch(bg, geodnsID)
	local := g.Root.Clone()
	local.Children = local.Children[:1]
	rs := s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: geodnsID, Path: geodnsRes, Root: local}, Base: g,
		Actions: []adapter.Action{{Verb: "update", Group: "location"}}})
	if len(rs) != 1 || rs[0].Err == nil || !strings.Contains(rs[0].Err.Error(), "read-only") {
		t.Fatalf("%+v", rs)
	}
}

// Removing status="0" makes a record active again, and removing geo moves
// it to the default location: mod-record must say so, not omit the field.
func TestApplyClearStatusAndGeo(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		byID(root, "record", "16").DelAttr("status")
		byID(root, "record", "12").DelAttr("geo")
	})
	if e := errsOf(s.Apply(bg, req)); len(e) > 0 {
		t.Fatal(e)
	}
	z := srv.Zone("qa1.pl")
	if z.Records["16"].Status != 1 || z.Records["12"].Geo != "1" {
		t.Fatalf("status %d geo %q", z.Records["16"].Status, z.Records["12"].Geo)
	}
}

// Changing one failover leaves the others alone, whatever order the API
// answered their settings in.
func TestFailoverDiffIgnoresAttrOrder(t *testing.T) {
	for range 10 {
		srv := site(t)
		srv.SetFailover("qa1.pl", "13", map[string]string{"check_type": "1", "host": "a.qa1.pl", "path": "/", "port": "80",
			"monitoring_region": "eu", "check_period": "60", "backup_ip_1": "198.51.100.9"})
		s := testSession(t, srv, selection{})
		req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
			canon.Normalize(root, zoneSchema) // as the file has it; the base is the API's order
			for _, f := range root.ChildrenNamed("failover") {
				if attr(f, "record") == "12" {
					f.SetAttr("check-period", "120")
				}
			}
		})
		if len(req.Actions) != 1 || req.Actions[0].Detail != "modify failover on record 12" {
			t.Fatalf("%+v", req.Actions)
		}
		if e := errsOf(s.Apply(bg, req)); len(e) > 0 || strings.Join(srv.Writes(), " ") != "failover-modify" {
			t.Fatal(e, srv.Writes())
		}
	}
}

// Only a zone ClouDNS says is missing is "not found"; a refusal for another
// reason (an outage, a limit) is an error, so pull does not drop the file.
func TestZoneInfoErrorIsNotNotFound(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	if _, err := s.Fetch(bg, "nope.pl"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("unknown zone: %v", err)
	}
	srv.Fail["get-zone-info"] = "Temporary server error, please try again later."
	if _, err := s.Fetch(bg, "qa1.pl"); err == nil || errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("an outage must not read as a deleted zone: %v", err)
	}
}

// A type change gives the record a new id, and ClouDNS drops its failover
// with the old one: the action says so.
func TestTypeChangeWarnsAboutFailover(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	req := change(t, s, "qa1.pl", func(root *xmltree.Node) {
		r := byID(root, "record", "12")
		r.SetAttr("type", "CNAME")
		r.DelAttr("geo")
		setText(r, "api2.qa1.pl")
	})
	var w string
	for _, a := range req.Actions {
		if a.Target == "record[id=12]" {
			w = a.Warning
		}
	}
	if !strings.Contains(w, "failover") {
		t.Fatalf("warning %q", w)
	}
}
