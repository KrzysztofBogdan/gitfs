package ovh

// Real-site checks, skipped unless the environment names an account:
//
//	GFS_OVH_SITE=ovh://eu go test ./internal/adapter/ovh/ -run TestReal -v
//
// Writes also need a scratch zone, named twice, and a backup directory:
//
//	GFS_OVH_SCRATCH=z.com GFS_DNS_WRITE_CONFIRM=z.com GFS_DNS_BACKUP_DIR=~/gfs-dns-backups

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	site := os.Getenv("GFS_OVH_SITE")
	if site == "" {
		t.Skip("GFS_OVH_SITE not set")
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

// Every zone reads back unchanged: no actions against itself, valid, and
// the same version twice.
func TestRealReadRoundTrip(t *testing.T) {
	s := realSession(t)
	zones, err := s.zoneNames(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range zones {
		r, err := s.Fetch(bg, z)
		if err != nil {
			t.Fatalf("%s: %v", z, err)
		}
		if err := validate.Resource(r.Root, r.Root, zoneSchema); err != nil {
			t.Errorf("%s: %v", z, err)
		}
		if acts := changes.ResolveActions(&Adapter{}, r.Root, &envelope.Doc{Content: r.Root.Clone()}, r.Path, r.Path); len(acts) != 0 {
			t.Errorf("%s: %d actions against itself", z, len(acts))
		}
		if again, _ := s.Fetch(bg, z); again == nil || again.Version != r.Version {
			t.Errorf("%s: version not stable", z)
		}
		t.Logf("%s: %d records, %d redirects, %d dynhosts", z, len(r.Root.ChildrenNamed("record")),
			len(r.Root.ChildrenNamed("redirect")), len(r.Root.ChildrenNamed("dynhost")))
	}
}

// backup saves the zone as OVH has it before any write.
func backup(t *testing.T, s *session, zone, dir string) {
	t.Helper()
	var export string
	if err := s.c.Do(bg, http.MethodGet, zonePath(zone, "export"), nil, &export); err != nil {
		t.Fatal(err)
	}
	d, err := s.fetchZone(bg, zone)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(map[string]any{"soa": d.soa, "dnssec": d.dnssec, "records": d.records,
		"redirects": d.redirects, "dynhosts": d.dynhosts, "logins": d.logins}, "", "  ")
	root := d.toXML(zone)
	if err := dnsx.WriteBackup(dir, map[string][]byte{"export.zone": []byte(export), "zone.json": data,
		"zone.xml": envelope.Bytes(envelope.New(root), zoneSchema)}); err != nil {
		t.Fatal(err)
	}
	t.Logf("backup: %s", dir)
}

// edit applies one change to the scratch zone and returns its results.
func edit(t *testing.T, s *session, zone string, f func(root *xmltree.Node)) []adapter.Result {
	t.Helper()
	base, err := s.Fetch(bg, zone)
	if err != nil {
		t.Fatal(err)
	}
	local := base.Root.Clone()
	f(local)
	acts := changes.ResolveActions(&Adapter{}, base.Root, &envelope.Doc{Content: local}, base.Path, base.Path)
	rs := s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: zone, Path: base.Path, Root: local}, Base: base, Actions: acts})
	for _, r := range rs {
		if r.Err != nil {
			t.Fatalf("%s %s: %v", r.Action.Verb, r.Action.Detail, r.Err)
		}
	}
	return rs
}

func named(root *xmltree.Node, elem, name string) *xmltree.Node {
	for _, c := range root.ChildrenNamed(elem) {
		if attr(c, "name") == name {
			return c
		}
	}
	return nil
}

func TestRealWrites(t *testing.T) {
	zone, dir, skip := dnsx.ScratchZone("ovh", "GFS_OVH_SCRATCH", os.Getenv, time.Now())
	if skip != "" {
		t.Skip(skip)
	}
	s := realSession(t)
	zones, err := s.zoneNames(bg)
	if err != nil || !strings.Contains(" "+strings.Join(zones, " ")+" ", " "+zone+" ") {
		t.Fatalf("%s is not a zone of this account (%v)", zone, err)
	}
	backup(t, s, zone, dir)
	name := fmt.Sprintf("gfs-check-%d", time.Now().Unix())
	t.Cleanup(func() { // delete whatever this run created and left behind (names start with name)
		r, err := s.Fetch(bg, zone)
		if err != nil {
			t.Errorf("cleanup: %v (delete %s* by hand)", err, name)
			return
		}
		local := r.Root.Clone()
		left := 0
		for _, el := range []string{"record", "redirect", "dynhost"} {
			for _, c := range local.ChildrenNamed(el) {
				if strings.HasPrefix(attr(c, "name"), name) {
					local.Children = deleteNode(local.Children, c)
					left++
				}
			}
		}
		if left == 0 {
			return
		}
		acts := changes.ResolveActions(&Adapter{}, r.Root, &envelope.Doc{Content: local}, r.Path, r.Path)
		for _, res := range s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: zone, Path: r.Path, Root: local}, Base: r, Actions: acts}) {
			if res.Err != nil {
				t.Errorf("cleanup %s: %v (delete by hand)", res.Action.Detail, res.Err)
			}
		}
	})
	serial := func() string {
		r, _ := s.Fetch(bg, zone)
		return attr(r.Root.Child("soa"), "serial")
	}
	before := serial()
	add := func(xml string) func(*xmltree.Node) {
		return func(root *xmltree.Node) {
			n, err := xmltree.ParseString(xml)
			if err != nil {
				t.Fatal(err)
			}
			root.Children = append(root.Children, n)
		}
	}
	edit(t, s, zone, add(`<record name="`+name+`" type="A" ttl="60">192.0.2.10</record>`))
	r, _ := s.Fetch(bg, zone)
	if rec := named(r.Root, "record", name); rec == nil || textOf(rec) != "192.0.2.10" {
		t.Fatal("created record not found")
	}
	if serial() == before {
		t.Error("the refresh must move the SOA serial")
	}
	edit(t, s, zone, func(root *xmltree.Node) { setText(named(root, "record", name), "192.0.2.11") })
	edit(t, s, zone, func(root *xmltree.Node) {
		rec := named(root, "record", name)
		rec.SetAttr("type", "TXT")
		setText(rec, "gfs check")
	})
	r, _ = s.Fetch(bg, zone)
	if rec := named(r.Root, "record", name); rec == nil || attr(rec, "type") != "TXT" {
		t.Fatal("type change")
	}
	edit(t, s, zone, func(root *xmltree.Node) { root.Children = deleteNode(root.Children, named(root, "record", name)) })
	edit(t, s, zone, add(`<redirect name="`+name+`-r" type="visiblePermanent">https://example.com/</redirect>`))
	edit(t, s, zone, func(root *xmltree.Node) {
		root.Children = deleteNode(root.Children, named(root, "redirect", name+"-r"))
	})
	edit(t, s, zone, add(`<dynhost name="`+name+`-d">192.0.2.12</dynhost>`))
	edit(t, s, zone, func(root *xmltree.Node) { root.Children = deleteNode(root.Children, named(root, "dynhost", name+"-d")) })
	r, _ = s.Fetch(bg, zone)
	for _, el := range []string{"record", "redirect", "dynhost"} {
		for _, c := range r.Root.ChildrenNamed(el) {
			if strings.HasPrefix(attr(c, "name"), name) {
				t.Errorf("%s %s left behind", el, attr(c, "name"))
			}
		}
	}
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
