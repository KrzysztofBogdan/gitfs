package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/cloudns/cloudnstest"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh/ovhtest"
)

func TestOVHCloneEditCommit(t *testing.T) {
	srv := ovhtest.New()
	t.Cleanup(srv.Close)
	srv.AddConsumer("ck", ovhtest.DNSRules...)
	srv.AddZone("a.com")
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "CNAME", SubDomain: "www", Target: "a.com."})
	t.Setenv("GFS_OVH_APP_KEY", srv.AppKey)
	t.Setenv("GFS_OVH_APP_SECRET", srv.AppSecret)
	t.Setenv("GFS_OVH_CONSUMER_KEY", "ck")
	dir := t.TempDir()
	t.Chdir(dir)
	mustContain(t, mustRun(t, 0, "clone", "ovh://eu?base="+url.QueryEscape(srv.URL+"/1.0")), "Cloned 1 resources")
	t.Chdir(filepath.Join(dir, "ovh-eu"))
	file := "domain/zone/a.com.xml"
	replaceIn(t, file, "</zone>", `<record name="api" type="A" ttl="300">192.0.2.10</record></zone>`)
	replaceIn(t, file, ">a.com.</record>", ">b.com.</record>")
	mustContain(t, mustRun(t, 0, "status"), "M  domain/zone/a.com.xml", "create record api A 192.0.2.10 ttl 300", "update record www CNAME a.com. → b.com.")
	mustContain(t, mustRun(t, 1, "commit", "--dry-run"), "would run  create record api A 192.0.2.10 ttl 300  [ask]") // no terminal: asks deny
	out := mustRun(t, 1, "commit")
	mustContain(t, out, "needs confirmation: rerun with --allow dns or --force")
	if len(srv.Calls()) != 0 {
		t.Fatalf("a denied commit sends nothing: %v", srv.Calls())
	}
	mustContain(t, mustRun(t, 0, "commit", "--force"), "create  domain/zone/a.com.xml   ok  record[1] create record api A 192.0.2.10 ttl 300",
		"refresh domain/zone/a.com.xml   ok", "3 actions, 0 failed, 0 denied")
	b, _ := os.ReadFile(file)
	if !regexp.MustCompile(`<record id="\d+" name="api" type="A" ttl="300">192.0.2.10</record>`).Match(b) || srv.Zone("a.com").Refreshes != 1 {
		t.Fatalf("new record gets its id:\n%s", b)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
	srv.AddRecord("a.com", ovhtest.Record{FieldType: "TXT", SubDomain: "x", Target: "hello"})
	mustContain(t, mustRun(t, 0, "pull"), "~  domain/zone/a.com.xml")
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), ">hello</record>") {
		t.Fatalf("%s", b)
	}
}

func TestClouDNSCloneEditCommit(t *testing.T) {
	srv := cloudnstest.New()
	t.Cleanup(srv.Close)
	srv.AddZone("qa1.pl", "geodns")
	srv.AddRecord("qa1.pl", cloudnstest.Record{Type: "A", Host: "api", Record: "192.0.2.1", TTL: "60"})
	t.Setenv("GFS_CLOUDNS_PASSWORD", srv.Password)
	dir := t.TempDir()
	t.Chdir(dir)
	mustContain(t, mustRun(t, 0, "clone", "cloudns://sub-95884?base="+url.QueryEscape(srv.URL)), "Cloned 2 resources")
	t.Chdir(filepath.Join(dir, "cloudns-sub-95884"))
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
	file := "zone/qa1.pl.xml"
	replaceIn(t, file, "</zone>", `<record name="api" type="A" ttl="60" geo="EUR">203.0.113.20</record></zone>`)
	mustContain(t, mustRun(t, 0, "status"), "create record api A 203.0.113.20 ttl 60 geo EUR")
	mustContain(t, mustRun(t, 0, "commit", "--allow", "dns"), "create  zone/qa1.pl.xml   ok  record[1] id=")
	if b, _ := os.ReadFile(file); !regexp.MustCompile(`<record id="\d+" name="api" type="A" ttl="60" geo="EUR">203.0.113.20</record>`).Match(b) {
		t.Fatalf("%s", b)
	}
	replaceIn(t, file, `ttl="60" geo="EUR">203.0.113.20`, `ttl="120" geo="EUR">203.0.113.20`)
	out := mustRun(t, 1, "commit", "--force")
	mustContain(t, out, "ttl 120 is not one ClouDNS allows")
	replaceIn(t, ".geodns.xml", `code="EUR"`, `code="XXX"`)
	mustContain(t, mustRun(t, 1, "commit", "--force", ".geodns.xml"), "invalid .geodns.xml   FAIL") // refused before the adapter sees it
}
