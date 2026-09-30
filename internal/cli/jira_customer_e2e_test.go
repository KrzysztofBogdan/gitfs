package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

func TestJiraCustomerEndToEnd(t *testing.T) {
	p := jtest.NewPortal()
	t.Cleanup(p.Close)
	p.AddDesk(jtest.Desk{ID: "34", Key: "ECOHELP", Name: "Developer Support"}, jtest.RequestType{ID: "4180", Name: "Marketplace listing"})
	p.SetFields("34", "4180", jtest.RequestField{ID: "summary", Name: "Summary", Required: true}, jtest.RequestField{ID: "description", Name: "Description"})
	p.AddUser(jtest.User{Account: "a:sher", Name: "Sherica"})
	p.AddRequest(jtest.Request{ID: "164494", Key: "ECOHELP-164494", Desk: "34", Type: "4180", Summary: "Listing rejected", Reporter: "me", Mine: true,
		Transitions: []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}},
		Approvals:   []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}}})
	t.Setenv("GFS_JIRA_TOKEN", "t")
	t.Setenv("GFS_JIRA_EMAIL", "me@x.com")
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/34?base="+p.URL)
	t.Chdir(dir + "/ecosystem")
	file := "ecohelp/ECOHELP-164494 Listing rejected.xml"
	if !exists(file) || !strings.Contains(remoteURL(t, "."), "jira+customer://ecosystem.atlassian.net?base=") {
		t.Fatal("clone layout")
	}

	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "Please fix the icon.", Public: true})
	mustRun(t, 0, "pull")
	if !strings.Contains(read(t, file), "Please fix the icon.") {
		t.Fatal("an agent's comment on an open request must arrive")
	}

	replaceIn(t, file, "</request>", "  <comment>Fixed, please re-review.</comment>\n    </request>")
	mustContain(t, mustRun(t, 1, "commit"), "--allow reply")
	mustRun(t, 0, "commit", "--allow", "reply")

	replaceIn(t, file, `status="pending"/>`, `status="pending" decision="approve"/>`)
	mustContain(t, mustRun(t, 0, "actions", file), "Mark as resolved", "Legal")
	mustRun(t, 0, "commit", "--allow", "approve")
	if p.Request("164494").Approvals[0].Decision != "approved" {
		t.Fatal("approval")
	}

	replaceIn(t, file, "<status category=\"indeterminate\">Waiting for support</status>", "<status category=\"indeterminate\">Mark as resolved</status>")
	mustRun(t, 0, "commit")
	if !strings.Contains(read(t, file), `<status category="done">Resolved</status>`) {
		t.Fatalf("status after the transition:\n%s", read(t, file))
	}

	os.WriteFile("ecohelp/Second listing.xml", []byte(`<request><summary>Second listing</summary><requestType>Marketplace listing</requestType></request>`), 0o644)
	mustContain(t, mustRun(t, 0, "status"), `raise "Marketplace listing" on ECOHELP`)
	mustRun(t, 0, "commit")
	var found bool
	entries, _ := os.ReadDir("ecohelp")
	for _, e := range entries {
		found = found || strings.HasSuffix(e.Name(), " Second listing.xml") && strings.HasPrefix(e.Name(), "ECOHELP-")
	}
	if !found {
		t.Fatalf("new request not renamed: %v", entries)
	}
}
