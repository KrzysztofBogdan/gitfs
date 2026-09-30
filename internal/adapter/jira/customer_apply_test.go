package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// custChange fetches request id, lets edit change a copy, and returns the request.
func custChange(t *testing.T, s *custSession, id string, edit func(*xmltree.Node), acts ...adapter.Action) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	local := *base
	local.Root = base.Root.Clone()
	edit(local.Root)
	return adapter.ApplyRequest{Local: &local, Base: base, Actions: acts}
}

func ok(t *testing.T, out []adapter.Result) {
	t.Helper()
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%+v: %v", r.Action, r.Err)
		}
	}
}

func TestCustomerReplyTransitionParticipants(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Transitions = []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}}
	})
	p.AddUser(jtest.User{Account: "a:bo", Name: "Bo"})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	req := custChange(t, s, "164494", func(r *xmltree.Node) {
		add(r, textEl("comment", "Icon replaced, please re-review."))
		setText(r, "status", "Mark as resolved")
		add(r, textEl2("participant", "Bo", "account", "a:bo"))
	}, adapter.Action{Verb: "update", Group: "status"}, adapter.Action{Verb: "update", Group: "participant"},
		adapter.Action{Verb: "create", Target: "comment[1]"})
	if out := s.Check(bg, req); out[0].Err != nil || out[0].Detail != "transition Mark as resolved" || p.Count("POST", "/") != 0 {
		t.Fatalf("%+v %v", out, p.Requests)
	}
	ok(t, s.Apply(bg, req))
	r := p.Request("164494")
	if r.Status != "Resolved" || len(r.Participants) != 1 || r.Participants[0] != "a:bo" || r.Comments[len(r.Comments)-1].Body != "Icon replaced, please re-review." {
		t.Fatalf("%+v", r)
	}

	req = custChange(t, s, "164494", func(r *xmltree.Node) { add(r, textEl("participant", "bo@x.com")) },
		adapter.Action{Verb: "update", Group: "participant"})
	if out := s.Check(bg, req); out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "customers cannot look people up") {
		t.Fatal(out[0].Err)
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { remove(r, "participant") }, adapter.Action{Verb: "update", Group: "participant"})
	ok(t, s.Apply(bg, req))
	if len(p.Request("164494").Participants) != 0 {
		t.Fatal("participant not removed")
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { setText(r, "status", "Escalate") }, adapter.Action{Verb: "update", Group: "status"})
	if out := s.Check(bg, req); out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `no transition "Escalate" for you on this request; available:`) {
		t.Fatal(out[0].Err)
	}
}

func TestCustomerApprovalsAndRefusals(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Approvals = []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}}
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	answer := adapter.Action{Verb: "update", Target: "approval[id=12]"}
	req := custChange(t, s, "164494", func(r *xmltree.Node) { r.Child("approval").SetAttr("decision", "maybe") }, answer)
	if out := s.Check(bg, req); out[0].Err == nil || !strings.Contains(out[0].Err.Error(), `decision="approve" or decision="decline"`) {
		t.Fatal(out[0].Err)
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { r.Child("approval").SetAttr("decision", "approve") }, answer)
	ok(t, s.Apply(bg, req))
	if p.Request("164494").Approvals[0].Decision != "approved" {
		t.Fatal("approval not answered")
	}
	for _, c := range []struct {
		a    adapter.Action
		want string
	}{
		{adapter.Action{Verb: "update", Group: "summary"}, "<summary> is read-only for customers"},
		{adapter.Action{Verb: "update", Target: "comment[id=1]"}, "customers cannot edit or delete comments"},
		{adapter.Action{Verb: "delete"}, "customers cannot delete requests"},
	} {
		req := custChange(t, s, "164494", func(*xmltree.Node) {}, c.a)
		if out := s.Apply(bg, req); out[0].Err == nil || out[0].Err.Error() != c.want {
			t.Errorf("%+v: %v", c.a, out[0].Err)
		}
	}
}

func TestCustomerRaiseWithAttachment(t *testing.T) {
	p := portal(t)
	p.SetFields("34", "4180", jtest.RequestField{ID: "summary", Name: "Summary", Required: true},
		jtest.RequestField{ID: "description", Name: "Description"}, jtest.RequestField{ID: "customfield_19404", Name: "Partner ID", Required: true})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	x := `<request><summary>New listing</summary><requestType>Marketplace listing</requestType>
  <description type="text/x-jira-wiki">Please review.</description><comment>More soon.</comment></request>`
	req := adapter.ApplyRequest{Local: &adapter.Resource{Path: "ecohelp/New listing.xml", Root: parse(t, x)}, Actions: []adapter.Action{{Verb: "create"}}}
	if out := s.Check(bg, req); out[0].Err == nil || out[0].Err.Error() != `a new "Marketplace listing" request needs Partner ID` {
		t.Fatal(out[0].Err)
	}
	req.Local.Root = parse(t, strings.Replace(x, "<comment>", `<field id="customfield_19404">1216382</field><comment>`, 1))
	req.Actions = append(req.Actions, adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "shot.png"), File: "ecohelp/New listing.files/shot.png"})
	req.Open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("png")), nil }
	out := s.Apply(bg, req)
	ok(t, out)
	r := p.Request(out[0].ID)
	if r == nil || r.Summary != "New listing" || r.Fields["customfield_19404"] != "1216382" || len(r.Comments) != 1 || len(r.Attachments) != 1 || out[1].ID == "" {
		t.Fatalf("%+v %+v", out, r)
	}
	bad := adapter.ApplyRequest{Local: &adapter.Resource{Path: "ecohelp/X.xml", Root: parse(t, `<request><summary>x</summary><requestType>Nope</requestType></request>`)},
		Actions: []adapter.Action{{Verb: "create"}}}
	if out := s.Check(bg, bad); out[0].Err == nil || out[0].Err.Error() != `<requestType> "Nope" is not offered by ECOHELP; request types: Marketplace listing` {
		t.Fatal(out[0].Err)
	}
}

func TestCustomerAvailable(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Transitions = []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}}
		r.Approvals = []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}, {ID: "13", Name: "Finance", Decision: "approved"}}
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	local, _ := s.Fetch(bg, "164494")
	setText(local.Root, "status", "mark as resolved")
	adv, err := s.Available(bg, "164494", local)
	if err != nil || adv.State != "status: Waiting for support" || len(adv.Items) != 2 || adv.Items[1].Name != "Legal" ||
		adv.Note != `local <status> mark as resolved -> "Mark as resolved"` {
		t.Fatalf("%+v %v", adv, err)
	}
}

func TestCustomerDescribe(t *testing.T) {
	local := &adapter.Resource{Path: "ecohelp/New.xml", Root: parse(t, `<request><requestType>Bug</requestType><status>Mark as resolved</status></request>`)}
	for _, c := range []struct {
		a             adapter.Action
		class, detail string
	}{
		{adapter.Action{Verb: "create"}, "create", `raise "Bug" on ECOHELP`},
		{adapter.Action{Verb: "create", Target: "comment[1]"}, "reply", "add reply (emails the service desk)"},
		{adapter.Action{Verb: "update", Target: "approval[id=12]"}, "approve", "answer approval 12"},
		{adapter.Action{Verb: "update", Group: "status"}, "transition", "transition: Mark as resolved"},
		{adapter.Action{Verb: "update", Group: "participant"}, "delete", "update participants (may remove people)"},
		{adapter.Action{Verb: "update", Group: "summary"}, "update", "update summary (not allowed for customers)"},
	} {
		a := c.a
		(&CustomerAdapter{}).Describe(&a, local)
		if a.Class != c.class || a.Detail != c.detail {
			t.Errorf("%+v: %s %q", c.a, a.Class, a.Detail)
		}
	}
}

// Participant changes can remove people, so they ask like deletes.
func TestCustomerParticipantChangeAsks(t *testing.T) {
	a := adapter.Action{Verb: "update", Group: "participant"}
	(&CustomerAdapter{}).Describe(&a, &adapter.Resource{Path: "e/x.xml", Root: parse(t, `<request/>`)})
	if a.Class != "delete" {
		t.Fatal(a.Class)
	}
}
