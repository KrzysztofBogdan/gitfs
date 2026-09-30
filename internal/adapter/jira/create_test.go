package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

func newFile(t *testing.T, path, x string) adapter.ApplyRequest {
	t.Helper()
	return adapter.ApplyRequest{Local: &adapter.Resource{Path: path, Root: parse(t, x)},
		Actions: []adapter.Action{{Verb: "create"}}}
}

func TestPlanCreateRefusals(t *testing.T) {
	srv, s := editSite(t)
	srv.SetFields("GEN", "2",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})
	cases := map[string]string{
		`<issue><summary>x</summary></issue>`:                                                     "<type> is required; GEN has Sub-task, Task",
		`<issue><type>Epic</type><summary>x</summary></issue>`:                                    `GEN has no issue type "Epic"; types: Sub-task, Task`,
		`<issue><type>Sub-task</type><summary>x</summary></issue>`:                                "a new Sub-task in GEN needs <parent>",
		`<issue><type>Sub-task</type><summary>x</summary><priority>High</priority></issue>`:       "<priority> is not on the create screen of Sub-task in GEN",
		`<issue><type>Task</type></issue>`:                                                        "a new Task in GEN needs <summary>",
		`<issue><type>Task</type><summary>x</summary><field id="customfield_9">1</field></issue>`: `<field id="customfield_9"> is not on the create screen of Task in GEN`,
	}
	for x, want := range cases {
		_, err := s.planCreate(bg, newFile(t, "gen/New.xml", x))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", x, err, want)
		}
	}
	if _, err := s.planCreate(bg, newFile(t, "ops/New.xml", `<issue><type>Task</type><summary>x</summary></issue>`)); err == nil ||
		err.Error() != `no project "ops" in this tree; gfs does not create projects (projects: gen)` {
		t.Fatal(err)
	}
}

func TestCreateEverything(t *testing.T) {
	srv, s := editSite(t)
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	req := newFile(t, "gen/Rate limiter drops burst traffic.xml", `<issue>
  <summary>Rate limiter drops burst traffic</summary>
  <type>Task</type>
  <status>In Progress</status>
  <priority>High</priority>
  <assignee>bea@x.com</assignee>
  <labels><label>backend</label></labels>
  <field id="customfield_10050"><option>Core</option></field>
  <description type="application/vnd.atlassian.adf+xml"><paragraph>Bursts above 200 rps are dropped.</paragraph></description>
  <link type="blocks">GEN-1</link>
  <comment><paragraph>Found in load test.</paragraph></comment>
  <worklog><started>2026-09-29T09:00:00.000+0200</started><spent>1h</spent></worklog>
</issue>`)
	req.Actions = append(req.Actions, adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "hey.txt"),
		File: "gen/Rate limiter drops burst traffic.files/hey.txt"})
	req.Open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hey -z 10s")), nil }
	out := s.create(bg, req)
	if len(out) != 2 || out[0].Err != nil || out[0].Detail != "GEN-3" || out[1].Err != nil || out[1].ID == "" || out[1].Version != "-" {
		t.Fatalf("%+v", out)
	}
	is := srv.Issue(out[0].ID)
	if is.Status != "In Progress" || is.Assignee != "u:b" || is.Fields["customfield_10050"].(map[string]any)["value"] != "Core" ||
		len(is.Comments) != 1 || len(is.Worklogs) != 1 || len(is.Attachments) != 1 {
		t.Fatalf("%+v", is)
	}
	r, _ := s.Fetch(bg, out[0].ID)
	if r.Path != "gen/GEN-3 Rate limiter drops burst traffic.xml" || r.Root.Child("link") == nil {
		t.Fatalf("%s %v", r.Path, r.Root.Child("link"))
	}
}

func TestCreateKeepsGoingAfterStatusFails(t *testing.T) {
	srv, s := editSite(t)
	out := s.create(bg, newFile(t, "gen/X.xml", `<issue><type>Task</type><summary>X</summary><status>Shipped</status>
  <comment><paragraph>still added</paragraph></comment></issue>`))
	if len(out) != 2 || out[0].Err != nil || out[1].Action.Group != "status" ||
		out[1].Err.Error() != `"Shipped" is not reachable from the current status in one step; reachable: "Done", "In Progress", "To Do"` {
		t.Fatalf("%+v", out)
	}
	if is := srv.Issue(out[0].ID); is == nil || len(is.Comments) != 1 {
		t.Fatal("the issue and its comment exist")
	}
}

func TestDeleteIssue(t *testing.T) {
	srv, s := editSite(t)
	if err := s.deleteIssue(bg, srv.ID("GEN-1")); err == nil || err.Error() != "delete or re-parent its sub-tasks first (GEN-2)" {
		t.Fatal(err)
	}
	if err := s.deleteIssue(bg, srv.ID("GEN-2")); err != nil || srv.Issue(srv.ID("GEN-2")) != nil {
		t.Fatal(err)
	}
	if err := s.deleteIssue(bg, srv.ID("GEN-1")); err != nil {
		t.Fatal(err)
	}
}
