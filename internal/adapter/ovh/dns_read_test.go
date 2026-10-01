package ovh

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh/ovhtest"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func str(s string) *string { return &s }

// site is a fake OVH with zone a.com: records, a redirect and a DynHost
// (each backed by records of its own), a DynHost login, DNSSEC on.
func site(t *testing.T) *ovhtest.Server {
	t.Helper()
	srv := ovhtest.New()
	t.Cleanup(srv.Close)
	srv.AddConsumer("ck", ovhtest.DNSRules...)
	z := srv.AddZone("a.com")
	z.DNSSEC = "enabled"
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "MX", Target: "10 mx1.mail.ovh.net."})
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "TXT", TTL: 600, Target: "v=spf1 include:mx.ovh.com -all"})
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "CNAME", SubDomain: "www", Target: "a.com."})
	srv.AddRedirect("a.com", ovhtest.Redirect{SubDomain: "old", Target: "https://a.com/new", Type: "visiblePermanent", Title: str("Moved")})
	srv.AddDynHost("a.com", ovhtest.DynHost{SubDomain: "home", IP: "198.51.100.5"})
	srv.AddLogin("a.com", ovhtest.Login{Login: "a.com-home", SubDomain: "home"})
	srv.AddZone("b.pl")
	return srv
}

func testSession(t *testing.T, srv *ovhtest.Server, sel selection) *session {
	t.Helper()
	return newSession(target{endpoint: "eu", base: srv.URL + "/1.0", creds: Creds{AppKey: srv.AppKey, AppSecret: srv.AppSecret, ConsumerKey: "ck"}, sel: sel})
}

func TestReadZone(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	names, err := s.zoneNames(bg)
	if err != nil || len(names) != 2 || names[0] != "a.com" {
		t.Fatal(names, err)
	}
	n, err := s.readZone(bg, "a.com")
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(n, zoneSchema)
	z := srv.Zone("a.com")
	ids := map[string]int64{}
	for id, r := range z.Records {
		ids[r.FieldType+" "+r.SubDomain] = id
	}
	got := xmltree.Print(n, 0)
	want := `<zone name="a.com">
  <soa ttl="3600" refresh="86400" expire="3600000" nx-domain-ttl="60" email="tech.ovh.net." server="dns101.ovh.net." serial="2025101600"/>
  <dnssec>enabled</dnssec>
  <record id="` + itoa(ids["MX "]) + `" name="@" type="MX">10 mx1.mail.ovh.net.</record>
  <record id="5100000001" name="@" type="NS">dns101.ovh.net.</record>
  <record id="5100000002" name="@" type="NS">ns101.ovh.net.</record>
  <record id="` + itoa(ids["TXT "]) + `" name="@" type="TXT" ttl="600">v=spf1 include:mx.ovh.com -all</record>
  <record id="` + itoa(ids["CNAME www"]) + `" name="www" type="CNAME">a.com.</record>
  <redirect id="` + itoa(ids["A old"]) + `" name="old" type="visiblePermanent" title="Moved">https://a.com/new</redirect>
  <dynhost id="` + itoa(ids["A home"]) + `" name="home" ttl="60">198.51.100.5</dynhost>
  <dynhost-login login="a.com-home" name="home"/>
</zone>`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	n, _ = s.readZone(bg, "b.pl")
	if n.Child("dnssec") == nil {
		t.Fatal("b.pl supports DNSSEC: disabled must show")
	}
}

// The version moves with any record or DNSSEC change, and only then.
func TestZoneVersion(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	v1, err := s.zoneVersion(bg, "a.com")
	if err != nil {
		t.Fatal(err)
	}
	if v2, _ := s.zoneVersion(bg, "a.com"); v2 != v1 {
		t.Fatal("version must be stable")
	}
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "A", SubDomain: "api", Target: "192.0.2.1"})
	v3, _ := s.zoneVersion(bg, "a.com")
	srv.Zone("a.com").DNSSEC = "disabled"
	v4, _ := s.zoneVersion(bg, "a.com")
	if v3 == v1 || v4 == v3 {
		t.Fatal(v1, v3, v4)
	}
}

func TestZoneNamesSelection(t *testing.T) {
	srv := site(t)
	names, _ := testSession(t, srv, selection{exclude: []string{"a.com"}}).zoneNames(bg)
	if len(names) != 1 || names[0] != "b.pl" {
		t.Fatal(names)
	}
}
