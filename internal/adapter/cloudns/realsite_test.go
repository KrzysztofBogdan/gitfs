package cloudns

// Real-site checks, skipped unless the environment names an API user:
//
//	GFS_CLOUDNS_SITE=cloudns://sub-1234 go test ./internal/adapter/cloudns/ -run TestReal -v
//
// Writes also need a scratch zone, named twice, and a backup directory:
//
//	GFS_CLOUDNS_SCRATCH=z.pl GFS_DNS_WRITE_CONFIRM=z.pl GFS_DNS_BACKUP_DIR=~/gfs-dns-backups

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func realSession(t *testing.T) *session {
	t.Helper()
	site := os.Getenv("GFS_CLOUDNS_SITE")
	if site == "" {
		t.Skip("GFS_CLOUDNS_SITE not set")
	}
	u, err := url.Parse(site)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, os.Getenv, creds.Store{Dirs: creds.DefaultDirs()})
	if err != nil {
		t.Fatal(err)
	}
	return newSession(tg)
}

func TestRealReadRoundTrip(t *testing.T) {
	s := realSession(t)
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range l.Resources {
		r, err := s.Fetch(bg, res.ID)
		if err != nil {
			t.Fatalf("%s: %v", res.ID, err)
		}
		if r.Version != res.Version {
			t.Errorf("%s: Fetch version %s, List %s", res.ID, r.Version, res.Version)
		}
		if res.ID == geodnsID {
			t.Logf(".geodns.xml: %d locations", len(r.Root.ChildrenNamed("location")))
			continue
		}
		if err := validate.Resource(r.Root, r.Root, zoneSchema); err != nil {
			t.Errorf("%s: %v", res.ID, err)
		}
		if acts := changes.ResolveActions(&Adapter{}, r.Root, &envelope.Doc{Content: r.Root.Clone()}, r.Path, r.Path); len(acts) != 0 {
			t.Errorf("%s: %d actions against itself", res.ID, len(acts))
		}
		attrs := map[string]bool{}
		for _, rec := range r.Root.ChildrenNamed("record") {
			for _, a := range rec.Attrs {
				attrs[a.Name] = true
			}
		}
		t.Logf("%s: %d records, record attributes %v", res.ID, len(r.Root.ChildrenNamed("record")), attrs)
	}
}

func backup(t *testing.T, s *session, zone, dir string) {
	t.Helper()
	files := map[string][]byte{}
	for _, a := range []string{"soa-details", "get-dnssec-ds-records", "mail-forwards", "get-zone-info"} {
		data, err := s.c.Raw(bg, a, url.Values{"domain-name": {zone}})
		if err != nil {
			t.Fatal(err)
		}
		files[a+".json"] = data
	}
	for page := 1; ; page++ {
		data, err := s.c.Raw(bg, "records", url.Values{"domain-name": {zone}, "page": {strconv.Itoa(page)}, "rows-per-page": {"100"}})
		if err != nil {
			t.Fatal(err)
		}
		files[fmt.Sprintf("records-%d.json", page)] = data
		m, _ := keyed(data)
		if len(m) < 100 {
			break
		}
	}
	if data, err := s.c.Raw(bg, "records-export", url.Values{"domain-name": {zone}}); err == nil {
		files["export.json"] = data // refused for GeoDNS zones
	}
	root, err := s.readZone(bg, zone)
	if err != nil {
		t.Fatal(err)
	}
	files["zone.xml"] = envelope.Bytes(envelope.New(root), zoneSchema)
	if err := dnsx.WriteBackup(dir, files); err != nil {
		t.Fatal(err)
	}
	t.Logf("backup: %s (%d files)", dir, len(files))
}

func named(root *xmltree.Node, elem, name string) *xmltree.Node {
	for _, c := range root.ChildrenNamed(elem) {
		if attr(c, "name") == name {
			return c
		}
	}
	return nil
}

func deleteNode(ns []*xmltree.Node, n *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, c := range ns {
		if c != n {
			out = append(out, c)
		}
	}
	return out
}

// edit applies f to the zone and returns the results; failures are the
// caller's to judge (some features depend on the plan).
func edit(t *testing.T, s *session, zone string, f func(root *xmltree.Node)) []adapter.Result {
	t.Helper()
	base, err := s.Fetch(bg, zone)
	if err != nil {
		t.Fatal(err)
	}
	local := base.Root.Clone()
	f(local)
	acts := changes.ResolveActions(&Adapter{}, base.Root, &envelope.Doc{Content: local}, base.Path, base.Path)
	return s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: zone, Path: base.Path, Root: local}, Base: base, Actions: acts})
}

func mustOK(t *testing.T, rs []adapter.Result) {
	t.Helper()
	for _, r := range rs {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Detail, r.Err)
		}
	}
}

func TestRealWrites(t *testing.T) {
	zone, dir, skip := dnsx.ScratchZone("cloudns", "GFS_CLOUDNS_SCRATCH", os.Getenv, time.Now())
	if skip != "" {
		t.Skip(skip)
	}
	s := realSession(t)
	if _, err := s.readZone(bg, zone); err != nil {
		t.Fatalf("%s is not a zone of this API user: %v", zone, err)
	}
	backup(t, s, zone, dir)
	name := fmt.Sprintf("gfs-check-%d", time.Now().Unix())
	t.Cleanup(func() { // delete whatever this run created and left behind (names start with name)
		rs := edit(t, s, zone, func(root *xmltree.Node) {
			for _, c := range root.ChildrenNamed("record") {
				if strings.HasPrefix(attr(c, "name"), name) {
					root.Children = deleteNode(root.Children, c)
				}
			}
			for _, c := range root.ChildrenNamed("mail-forward") {
				if strings.HasPrefix(attr(c, "box"), name) {
					root.Children = deleteNode(root.Children, c)
				}
			}
		})
		for _, r := range rs {
			if r.Err != nil {
				t.Errorf("cleanup %s: %v (delete by hand)", r.Action.Detail, r.Err)
			}
		}
	})
	add := func(xml string) func(*xmltree.Node) {
		return func(root *xmltree.Node) {
			n, err := xmltree.ParseString(xml)
			if err != nil {
				t.Fatal(err)
			}
			root.Children = append(root.Children, n)
		}
	}
	z0, _ := s.readZone(bg, zone)
	geo := attr(z0, "kind") == "geodns"
	geoAttr := ""
	if geo {
		geoAttr = ` geo="NAM"`
	}
	serial := attr(z0.Child("soa"), "serial")
	mustOK(t, edit(t, s, zone, add(`<record name="`+name+`" type="A" ttl="60"`+geoAttr+`>192.0.2.10</record>`)))
	z1, _ := s.readZone(bg, zone)
	rec := named(z1, "record", name)
	if rec == nil || textOf(rec) != "192.0.2.10" || (geo && attr(rec, "geo") != "NAM") {
		t.Fatalf("created record: %v", rec)
	}
	if attr(z1.Child("soa"), "serial") == serial {
		t.Error("adding a record must move the zone serial (pull relies on it)")
	}
	mustOK(t, edit(t, s, zone, func(root *xmltree.Node) {
		r := named(root, "record", name)
		r.SetAttr("ttl", "300")
		r.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: "192.0.2.11"}}
	}))
	// failover on the new record: its fields are not documented, so a refusal is logged, not failed
	rs := edit(t, s, zone, func(root *xmltree.Node) {
		add(`<failover record="` + attr(named(root, "record", name), "id") + `" check-type="1" host="192.0.2.11"><backup>192.0.2.12</backup></failover>`)(root)
	})
	if rs[0].Err != nil {
		t.Logf("failover activate refused (field names to confirm): %v", rs[0].Err)
	} else {
		z2, _ := s.readZone(bg, zone)
		for _, f := range z2.ChildrenNamed("failover") {
			if attr(f, "record") == attr(named(z2, "record", name), "id") {
				t.Logf("failover as read back: %s", xmltree.Print(f, 0))
			}
		}
	}
	mustOK(t, edit(t, s, zone, func(root *xmltree.Node) {
		r := named(root, "record", name)
		r.SetAttr("type", "TXT")
		r.DelAttr("geo")
		r.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: "gfs check"}}
		for _, f := range root.ChildrenNamed("failover") {
			if attr(f, "record") == attr(r, "id") {
				root.Children = deleteNode(root.Children, f)
			}
		}
	}))
	mustOK(t, edit(t, s, zone, func(root *xmltree.Node) { root.Children = deleteNode(root.Children, named(root, "record", name)) }))
	var stats struct {
		Count int  `json:"count"`
		Limit flex `json:"limit"`
	}
	if err := s.c.Do(bg, "get-mail-forwards-stats", url.Values{"domain-name": {zone}}, &stats); err == nil && stats.Limit != "0" {
		mustOK(t, edit(t, s, zone, add(`<mail-forward box="`+name+`" destination="nobody@example.com"/>`)))
		mustOK(t, edit(t, s, zone, func(root *xmltree.Node) {
			for _, c := range root.ChildrenNamed("mail-forward") {
				if attr(c, "box") == name {
					root.Children = deleteNode(root.Children, c)
				}
			}
		}))
	} else {
		t.Logf("mail forwards not available on this plan (limit %q)", stats.Limit)
	}
	z3, _ := s.readZone(bg, zone)
	if named(z3, "record", name) != nil {
		t.Error("record left behind")
	}
}
