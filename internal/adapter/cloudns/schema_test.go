package cloudns

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func parse(t *testing.T, s string) *xmltree.Node {
	t.Helper()
	n, err := xmltree.ParseString(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestZoneSchemaCanon(t *testing.T) {
	n := parse(t, `<zone name="qa1.pl" kind="geodns" type="master"><active>true</active><mail-forward id="7" box="info" destination="a@b.c"/>
<failover record="12" check-type="1"/><record id="12" name="api" type="A" geo="EUR" ttl="60">1.2.3.4</record>
<dnssec status="enabled"><ds key-tag="1" algorithm="13" digest-type="2">AB</ds></dnssec><soa serial="1" ttl="3600" primary="gns1.cloudns.net"/>
<record id="11" name="@" type="MX" priority="10" ttl="3600">mx.qa1.pl</record></zone>`)
	canon.Normalize(n, zoneSchema)
	want := `<zone name="qa1.pl" type="master" kind="geodns">
  <soa primary="gns1.cloudns.net" ttl="3600" serial="1"/>
  <dnssec status="enabled">
    <ds key-tag="1" algorithm="13" digest-type="2">AB</ds>
  </dnssec>
  <active>true</active>
  <record id="11" name="@" type="MX" ttl="3600" priority="10">mx.qa1.pl</record>
  <record id="12" name="api" type="A" ttl="60" geo="EUR">1.2.3.4</record>
  <failover record="12" check-type="1"/>
  <mail-forward id="7" box="info" destination="a@b.c"/>
</zone>`
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestZoneSchemaReadOnly(t *testing.T) {
	base := parse(t, `<zone name="qa1.pl" type="master" kind="geodns"><soa ttl="3600" serial="1"/><active>true</active></zone>`)
	for _, local := range []string{
		`<zone name="qa1.pl" type="master" kind="domain"><soa ttl="3600" serial="1"/><active>true</active></zone>`,
		`<zone name="qa1.pl" type="master" kind="geodns"><soa ttl="3600" serial="2"/><active>true</active></zone>`,
	} {
		if err := validate.Resource(parse(t, local), base, zoneSchema); err == nil {
			t.Errorf("%s: read-only change accepted", local)
		}
	}
	ok := `<zone name="qa1.pl" type="master" kind="geodns"><soa ttl="300" serial="1"/><active>false</active></zone>`
	if err := validate.Resource(parse(t, ok), base, zoneSchema); err != nil {
		t.Fatal(err)
	}
}
