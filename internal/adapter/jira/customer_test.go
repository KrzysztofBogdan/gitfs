package jira

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// portal is a fake customer portal with two desks: ECOHELP (34) holding an
// open and a resolved request of ours, and DS (24) with one shared request.
func portal(t *testing.T) *jtest.Portal {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	p := jtest.NewPortal()
	t.Cleanup(p.Close)
	p.Clock = func() time.Time { return now }
	p.AddDesk(jtest.Desk{ID: "34", Key: "ECOHELP", Name: "Developer and Marketplace Support"}, jtest.RequestType{ID: "4180", Name: "Marketplace listing"})
	p.AddDesk(jtest.Desk{ID: "24", Key: "DS", Name: "Design System Support"}, jtest.RequestType{ID: "90", Name: "Question"})
	p.AddUser(jtest.User{Account: "a:sher", Name: "Sherica"})
	p.AddRequest(jtest.Request{ID: "164494", Key: "ECOHELP-164494", Desk: "34", Type: "4180", Summary: "App listing rejected",
		Description: "Our listing was *rejected*.", Reporter: "me", Mine: true, Fields: map[string]any{"customfield_19404": 1216382},
		Created: now.Add(-72 * time.Hour)})
	p.AddRequest(jtest.Request{ID: "159249", Key: "ECOHELP-159249", Desk: "34", Type: "4180", Summary: "Old question", Reporter: "me",
		Mine: true, Status: "Resolved", Category: "DONE", Created: now.Add(-240 * time.Hour)})
	p.AddRequest(jtest.Request{ID: "900", Key: "DS-900", Desk: "24", Type: "90", Summary: "Shared with my org", Reporter: "a:sher",
		Participants: []string{"me"}, Created: now.Add(-24 * time.Hour)})
	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "Please fix the icon.", Public: true})
	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "agents only", Public: false})
	return p
}

func openCust(t *testing.T, p *jtest.Portal, sel custSelection) *custSession {
	t.Helper()
	if sel.ownership == "" {
		sel.ownership = "all"
	}
	if sel.status == "" {
		sel.status = "all"
	}
	s, err := openCustomer(bg, target{base: p.URL, email: "me@x.com", token: "t", csel: sel})
	if err != nil {
		t.Fatal(err)
	}
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func TestCustomerListing(t *testing.T) {
	p := portal(t)
	s := openCust(t, p, custSelection{})
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := ".people.xml|ds/DS-900 Shared with my org.xml|ecohelp/ECOHELP-159249 Old question.xml|ecohelp/ECOHELP-164494 App listing rejected.xml"
	if got := strings.Join(paths(l), "|"); got != want || !l.Full {
		t.Fatalf("%s", got)
	}
	got := xmltree.Print(find(t, l, "ecohelp/ECOHELP-164494 App listing rejected.xml").Root, 0)
	for _, part := range []string{
		`<request id="164494" key="ECOHELP-164494" desk="34" type="4180"`,
		`<requestType>Marketplace listing</requestType>`,
		`<status category="indeterminate">Waiting for support</status>`,
		`<field id="customfield_19404" name="customfield_19404">1216382</field>`,
		`<description type="text/x-jira-wiki">Our listing was *rejected*.</description>`,
		`author="Sherica"`, `Please fix the icon.`,
	} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %s in\n%s", part, got)
		}
	}
	if strings.Contains(got, "agents only") {
		t.Error("a customer never sees internal comments")
	}
	if p.Count("GET", "/rest/api/") != 0 {
		t.Fatal("customer mode must not call the Jira platform API")
	}

	// a later pull: the resolved request comes as a stub, open ones in full
	p.Requests = nil
	l, err = s.List(bg, l.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	old := find(t, l, "ecohelp/ECOHELP-159249 Old question.xml")
	if old.Root != nil || p.Count("GET", "/rest/servicedeskapi/request/159249/comment") != 0 {
		t.Fatal("a closed request is a stub")
	}
	if find(t, l, "ecohelp/ECOHELP-164494 App listing rejected.xml").Root == nil {
		t.Fatal("an open request is listed in full")
	}
	stamp := old.Version
	p.SetStatus("159249", "Reopened", "INDETERMINATE")
	l, _ = s.List(bg, l.Cursor)
	if r := find(t, l, "ecohelp/ECOHELP-159249 Old question.xml"); r.Version == stamp || r.Root == nil {
		t.Fatal("a status change moves the stamp and refetches")
	}
}

func TestCustomerFilters(t *testing.T) {
	p := portal(t)
	l, _ := openCust(t, p, custSelection{ownership: "owned", status: "open"}).List(bg, "")
	if got := strings.Join(paths(l), "|"); got != ".people.xml|ecohelp/ECOHELP-164494 App listing rejected.xml" {
		t.Fatal(got)
	}
	l, _ = openCust(t, p, custSelection{desks: []string{"24"}}).List(bg, "")
	if got := strings.Join(paths(l), "|"); got != ".people.xml|ds/DS-900 Shared with my org.xml" {
		t.Fatal(got)
	}
	if _, err := openCustomer(bg, target{base: p.URL, csel: custSelection{desks: []string{"77"}}}); err == nil ||
		err.Error() != "service desk 77 not found or not visible" {
		t.Fatal(err)
	}
}

func TestCustomerFetchAndDownload(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Attachments = append(r.Attachments, &jtest.Attachment{ID: "555", Filename: "icon.png", Mime: "image/png", Data: []byte("png"), Author: "me"})
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	r, err := s.Fetch(bg, "164494")
	if err != nil || r.Root.Child("attachment") == nil {
		t.Fatalf("%v", err)
	}
	if id, _ := r.Root.Child("attachment").Attr("id"); id != "555" {
		t.Fatal(id)
	}
	var b strings.Builder
	if info, err := s.Download(bg, "164494", "555", &b); err != nil || b.String() != "png" || info.Version != "-" {
		t.Fatal(info, err)
	}
	if _, err := s.Fetch(bg, "900"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("a request on a desk outside the tree: %v", err)
	}
}
