package cloudns

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/cloudns/cloudnstest"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// site is a fake ClouDNS with GeoDNS zone qa1.pl (records of several kinds,
// one with failover, a mail forward, DNSSEC on) and plain zone b.com.
func site(t *testing.T) *cloudnstest.Server {
	t.Helper()
	srv := cloudnstest.New()
	t.Cleanup(srv.Close)
	z := srv.AddZone("qa1.pl", "geodns")
	z.DNSSEC = true
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "11", Type: "MX", Record: "mxa.eu.mailgun.org", Extra: map[string]string{"priority": "10"}})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "12", Type: "A", Host: "api", Record: "203.0.113.20", TTL: "60", Geo: "6"})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "13", Type: "A", Host: "api", Record: "192.0.2.1", TTL: "60"})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "14", Type: "SRV", Host: "_sip._tcp", Record: "sip.qa1.pl",
		Extra: map[string]string{"priority": "0", "weight": "5", "port": "5060"}})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "15", Type: "CAA", Record: "letsencrypt.org", Extra: map[string]string{"caa_flag": "0", "caa_type": "issue"}})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "16", Type: "A", Host: "old", Record: "192.0.2.9", Status: -1})
	srv.AddRecord("qa1.pl", cloudnstest.Record{ID: "17", Type: "TXT", Record: "v=spf1 include:mailgun.org ~all"})
	srv.SetFailover("qa1.pl", "12", map[string]string{"check_type": "17", "host": "api.qa1.pl", "path": "/health",
		"monitoring_region": "eu", "check_period": "60", "main_ip": "203.0.113.20", "backup_ip_1": "198.51.100.7", "backup_ip_2": "198.51.100.8"})
	srv.AddMailForward("qa1.pl", cloudnstest.MailForward{Box: "info", Destination: "me@example.com"})
	srv.AddZone("b.com", "domain")
	return srv
}

func testSession(t *testing.T, srv *cloudnstest.Server, sel selection) *session {
	t.Helper()
	s := newSession(target{host: "sub-95884", base: srv.URL, auth: Auth{SubAuthID: srv.SubAuthID, Password: srv.Password}, sel: sel})
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func TestReadZone(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	zs, err := s.zones(bg)
	if err != nil || len(zs) != 2 || zs[1].Name != "qa1.pl" || zs[1].Kind != "geodns" {
		t.Fatalf("%+v %v", zs, err)
	}
	n, err := s.readZone(bg, "qa1.pl")
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(n, zoneSchema)
	mf := ""
	for id := range srv.Zone("qa1.pl").MailForwards {
		mf = id
	}
	want := `<zone name="qa1.pl" type="master" kind="geodns" active="true">
  <soa primary="gns1.cloudns.net" admin="support@cloudns.net" refresh="7200" retry="1800" expire="1209600" ttl="3600" serial="2026092810"/>
  <dnssec status="enabled">
    <ds key-tag="12626" algorithm="13" digest-type="2">B156B918CC62</ds>
  </dnssec>
  <record id="15" name="@" type="CAA" ttl="3600" caa-flag="0" caa-type="issue">letsencrypt.org</record>
  <record id="11" name="@" type="MX" ttl="3600" priority="10">mxa.eu.mailgun.org</record>
  <record id="499153689" name="@" type="NS" ttl="3600">gns1.cloudns.net</record>
  <record id="499153690" name="@" type="NS" ttl="3600">gns2.cloudns.net</record>
  <record id="17" name="@" type="TXT" ttl="3600">v=spf1 include:mailgun.org ~all</record>
  <record id="14" name="_sip._tcp" type="SRV" ttl="3600" priority="0" weight="5" port="5060">sip.qa1.pl</record>
  <record id="12" name="api" type="A" ttl="60" geo="NAM">203.0.113.20</record>
  <record id="13" name="api" type="A" ttl="60">192.0.2.1</record>
  <record id="16" name="old" type="A" ttl="3600" status="0">192.0.2.9</record>
  <failover record="12" check-period="60" check-type="17" host="api.qa1.pl" main-ip="203.0.113.20" monitoring-region="eu" path="/health">
    <backup>198.51.100.7</backup>
    <backup>198.51.100.8</backup>
  </failover>
  <mail-forward id="` + mf + `" box="info" destination="me@example.com"/>
</zone>`
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	n, _ = s.readZone(bg, "b.com")
	if got := xmltree.Print(n, 0); !strings.Contains(got, `kind="domain"`) || !strings.Contains(got, `<dnssec status="disabled"/>`) || strings.Contains(got, "geo=") {
		t.Fatal(got)
	}
}

// Records come in pages of 100.
func TestReadManyRecords(t *testing.T) {
	srv := site(t)
	for i := range 230 {
		srv.AddRecord("b.com", cloudnstest.Record{Type: "A", Host: fmt.Sprintf("h%03d", i), Record: "192.0.2.1"})
	}
	n, err := testSession(t, srv, selection{}).readZone(bg, "b.com")
	if err != nil || len(n.ChildrenNamed("record")) != 232 {
		t.Fatal(len(n.ChildrenNamed("record")), err)
	}
}

// List: zones as stubs versioned by serial and mail forwards, plus
// .geodns.xml; Fetch returns the same version.
func TestListFetch(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range l.Resources {
		paths = append(paths, r.Path)
	}
	if !l.Full || strings.Join(paths, " ") != ".geodns.xml zone/b.com.xml zone/qa1.pl.xml" {
		t.Fatal(paths)
	}
	qa := l.Resources[2]
	r, err := s.Fetch(bg, "qa1.pl")
	if err != nil || r.Version != qa.Version || qa.Root != nil || r.Path != "zone/qa1.pl.xml" {
		t.Fatalf("%+v %v", r, err)
	}
	geo, err := s.Fetch(bg, geodnsID)
	if err != nil || !strings.Contains(xmltree.Print(geo.Root, 0), `<location code="US" name="United States" parent="NAM"/>`) {
		t.Fatal(err, geo)
	}
	srv.AddMailForward("qa1.pl", cloudnstest.MailForward{Box: "x", Destination: "y@z.com"})
	l2, _ := s.List(bg, "")
	if l2.Resources[2].Version == qa.Version || l2.Resources[1].Version != l.Resources[1].Version {
		t.Fatal("a mail forward must move only its zone's version")
	}
	if _, err := s.Fetch(bg, "nope.pl"); err == nil {
		t.Fatal("unknown zone")
	}
}

// Without any GeoDNS zone there is no .geodns.xml.
func TestNoGeoFile(t *testing.T) {
	srv := site(t)
	l, _ := testSession(t, srv, selection{zones: []string{"b.com"}}).List(bg, "")
	if len(l.Resources) != 1 || l.Resources[0].Path != "zone/b.com.xml" {
		t.Fatal(l.Resources)
	}
}
