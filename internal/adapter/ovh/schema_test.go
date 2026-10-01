package ovh

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
	n := parse(t, `<zone name="a.com"><record id="9" name="www" type="A">1.2.3.4</record><dnssec>enabled</dnssec>
<redirect id="5" name="@" type="visible">www.a.com</redirect><soa refresh="86400" ttl="3600" serial="1" server="dns101.ovh.net."/>
<record name="api" type="A">5.6.7.8</record><record id="3" name="@" type="MX">10 mx.a.com.</record></zone>`)
	canon.Normalize(n, zoneSchema)
	want := `<zone name="a.com">
  <soa ttl="3600" refresh="86400" server="dns101.ovh.net." serial="1"/>
  <dnssec>enabled</dnssec>
  <record id="3" name="@" type="MX">10 mx.a.com.</record>
  <record name="api" type="A">5.6.7.8</record>
  <record id="9" name="www" type="A">1.2.3.4</record>
  <redirect id="5" name="@" type="visible">www.a.com</redirect>
</zone>`
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestZoneSchemaReadOnly(t *testing.T) {
	base := parse(t, `<zone name="a.com"><soa ttl="3600" serial="1" server="s."/><record id="3" name="@" type="MX">10 mx.</record></zone>`)
	local := parse(t, `<zone name="a.com"><soa ttl="3600" serial="2" server="s."/><record id="3" name="@" type="MX">10 mx.</record></zone>`)
	if err := validate.Resource(local, base, zoneSchema); err == nil {
		t.Fatal("serial is read-only")
	}
	local = parse(t, `<zone name="b.com"><soa ttl="3600" serial="1" server="s."/></zone>`)
	if err := validate.Resource(local, base, zoneSchema); err == nil {
		t.Fatal("zone name is read-only")
	}
}
