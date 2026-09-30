package jira

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const issueJSON = `{"id":"103922","key":"SUP-4057","fields":{
 "summary":"Promo code request","issuetype":{"id":"10006","name":"Support"},
 "status":{"name":"Waiting for support","statusCategory":{"key":"indeterminate"}},
 "resolution":null,"priority":{"name":"Lowest"},
 "assignee":{"accountId":"712020:a","displayName":"Adam","accountType":"atlassian","active":true},
 "reporter":{"accountId":"qm:h","displayName":"hadas@example.com","emailAddress":"hadas@example.com","accountType":"customer","active":true},
 "creator":{"accountId":"qm:h","displayName":"hadas@example.com","accountType":"customer","active":true},
 "project":{"id":"10001","key":"SUP"},"parent":{"key":"SUP-4000"},"labels":["renewal"],
 "components":[{"name":"billing"}],"fixVersions":[],"versions":[{"name":"2026.09"}],"duedate":"2026-09-30",
 "timetracking":{"originalEstimate":"2d","remainingEstimate":"4h","timeSpent":"1d 2h"},
 "created":"2026-09-29T15:22:28.934+0200","updated":"2026-09-29T15:23:31.800+0200","resolutiondate":null,
 "customfield_10040":{"id":"10022","value":"1-10"},"customfield_10019":"0|i06csf:","environment":null,
 "description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]},
 "issuelinks":[{"id":"10231","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"SUP-3990"}},
   {"id":"10232","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"SUP-3001"}}],
 "attachment":[{"id":"10500","filename":"log.txt","size":1204,"mimeType":"text/plain","created":"2026-09-29T15:30:00.000+0200","author":{"accountId":"712020:a","displayName":"Adam"}}]
}}`

func TestDecodeIssue(t *testing.T) {
	var is apiIssue
	if err := json.Unmarshal([]byte(issueJSON), &is); err != nil {
		t.Fatal(err)
	}
	set := map[string]fieldMeta{
		"summary": {ID: "summary"}, "priority": {ID: "priority"}, "description": {ID: "description"},
		"customfield_10040": cf("customfield_10040", "Number of users", sys+"select"),
	}
	pub, internal := true, false
	comments := []apiComment{
		{ID: "2", Author: apiUser{AccountID: "712020:a", DisplayName: "Adam"}, Created: "2026-09-29T16:00:00.000+0200",
			Updated: "2026-09-29T16:00:00.000+0200", JSDPublic: &internal, Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"note"}]}`))},
		{ID: "1", Author: apiUser{AccountID: "qm:h", DisplayName: "hadas@example.com"}, Created: "2026-09-29T15:50:00.000+0200",
			Updated: "2026-09-29T15:50:00.000+0200", JSDPublic: &pub, Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"thanks"}]}`))},
	}
	worklogs := []apiWorklog{{ID: "4412", Author: apiUser{AccountID: "712020:a", DisplayName: "Adam"}, Started: "2026-09-29T09:00:00.000+0200",
		TimeSpent: "2h", Created: "2026-09-29T17:00:00.000+0200", Updated: "2026-09-29T17:00:00.000+0200"}}
	reg := newRegistry()
	root, err := decoder{reg: reg, jsm: true}.issue(is, set, comments, worklogs)
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(root, issueSchema)
	want := `<issue id="103922" key="SUP-4057" created="2026-09-29T15:22:28.934+0200" updated="2026-09-29T15:23:31.800+0200">
  <summary>Promo code request</summary>
  <type>Support</type>
  <status>Waiting for support</status>
  <priority>Lowest</priority>
  <assignee account="712020:a">Adam</assignee>
  <reporter account="qm:h">hadas@example.com</reporter>
  <creator account="qm:h">hadas@example.com</creator>
  <parent>SUP-4000</parent>
  <labels>
    <label>renewal</label>
  </labels>
  <components>
    <component>billing</component>
  </components>
  <affectsVersions>
    <version>2026.09</version>
  </affectsVersions>
  <due>2026-09-30</due>
  <timetracking spent="1d 2h">
    <original>2d</original>
    <remaining>4h</remaining>
  </timetracking>
  <field id="customfield_10040" name="Number of users">
    <option id="10022">1-10</option>
  </field>
  <description type="application/vnd.atlassian.adf+xml">
    <paragraph>Hello</paragraph>
  </description>
  <link id="10231" type="blocks">SUP-3990</link>
  <link id="10232" type="is blocked by">SUP-3001</link>
  <attachment id="10500" name="log.txt" size="1204" mime="text/plain" created="2026-09-29T15:30:00.000+0200" author="Adam"/>
  <comment id="1" account="qm:h" author="hadas@example.com" created="2026-09-29T15:50:00.000+0200" updated="2026-09-29T15:50:00.000+0200" public="true">
    <paragraph>thanks</paragraph>
  </comment>
  <comment id="2" account="712020:a" author="Adam" created="2026-09-29T16:00:00.000+0200" updated="2026-09-29T16:00:00.000+0200" internal="true">
    <paragraph>note</paragraph>
  </comment>
  <worklog id="4412" account="712020:a" author="Adam" created="2026-09-29T17:00:00.000+0200" updated="2026-09-29T17:00:00.000+0200">
    <started>2026-09-29T09:00:00.000+0200</started>
    <spent>2h</spent>
  </worklog>
</issue>`
	if got := xmltree.Print(root, 0); got != want {
		t.Fatalf("got\n%s", got)
	}
	if reg.m["qm:h"].Email != "hadas@example.com" || len(reg.m) != 2 {
		t.Fatalf("people %+v", reg.m)
	}
	// outside a service project comments carry no visibility
	root, _ = decoder{reg: reg}.issue(is, set, comments[:1], nil)
	if c := root.Child("comment"); c == nil || len(c.Attrs) != 5 {
		t.Fatalf("non-JSM comment attrs: %+v", c)
	}
}

func TestInlinePage(t *testing.T) {
	cs, total, err := page[apiComment](json.RawMessage(`{"comments":[{"id":"1"}],"total":7,"maxResults":1}`), "comments")
	if err != nil || len(cs) != 1 || total != 7 {
		t.Fatal(cs, total, err)
	}
	if cs, total, err := page[apiComment](json.RawMessage(`null`), "comments"); cs != nil || total != 0 || err != nil {
		t.Fatal("null page")
	}
}

func TestIssuePaths(t *testing.T) {
	for _, c := range []struct{ key, summary, want string }{
		{"GEN-1", "Migrate auth", "gen/GEN-1 Migrate auth.xml"},
		{"GEN-2", "a/b\\c", "gen/GEN-2 a-b-c.xml"},
		{"GEN-3", "  ", "gen/GEN-3.xml"},
		{"GEN-4", "notes.files", "gen/GEN-4 notes.files_.xml"},
		{"GEN-5", "tab\there", "gen/GEN-5 tab here.xml"},
	} {
		if got := issuePath("gen", c.key, c.summary); got != c.want {
			t.Errorf("%s: %q, want %q", c.key, got, c.want)
		}
	}
	long := issuePath("sup", "SUP-9", strings.Repeat("Zażółć gęślą jaźń ", 20))
	if name := strings.TrimPrefix(long, "sup/"); len(name) > maxName+len(".xml") || !utf8.ValidString(name) || !strings.HasPrefix(name, "SUP-9 Zażółć") {
		t.Fatalf("%d bytes: %q", len(name), name)
	}
}
