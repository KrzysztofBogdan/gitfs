package jira

import (
	"strings"
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

func TestIssueSchemaCanon(t *testing.T) {
	root := parse(t, `<issue key="A-1" id="1">
  <comment id="9" created="2026-02-01">b</comment>
  <field id="customfield_2">2</field>
  <summary>S</summary>
  <comment internal="true">new</comment>
  <field id="customfield_1">1</field>
  <labels><label>z</label><label>a</label></labels>
  <status>To Do</status>
</issue>`)
	canon.Normalize(root, issueSchema)
	want := `<issue id="1" key="A-1">
  <summary>S</summary>
  <status>To Do</status>
  <labels>
    <label>a</label>
    <label>z</label>
  </labels>
  <field id="customfield_1">1</field>
  <field id="customfield_2">2</field>
  <comment id="9" created="2026-02-01">b</comment>
  <comment internal="true">new</comment>
</issue>`
	if got := xmltree.Print(root, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestIssueSchemaValidate(t *testing.T) {
	base := parse(t, `<issue id="1" key="A-1"><comment id="9" author="Bo" created="c">x</comment><link id="5" type="blocks">A-2</link></issue>`)
	cases := map[string]string{
		`<issue id="1" key="A-1"><comment id="9" author="Bo" created="c">edited</comment></issue>`:             "",
		`<issue id="1" key="A-1"><comment internal="true">new</comment><link type="blocks">A-3</link></issue>`: "",
		`<issue id="1" key="A-2"></issue>`:                                                                             `read-only attribute "key" changed`,
		`<issue id="1" key="A-1"><comment author="Me">new</comment></issue>`:                                           `read-only attribute "author" changed`,
		`<issue id="1" key="A-1"><worklog><started>s</started><spent>2h</spent><comment>x</comment></worklog></issue>`: `<comment> type "" not allowed`,
		`<issue id="1" key="A-1"><worklog><started>s</started><spent>2h</spent><comment type="application/vnd.atlassian.adf+xml"><paragraph>x</paragraph></comment></worklog></issue>`: "",
		`<issue id="1" key="A-1"><attachment name="a.png"/></issue>`: "new attachments are created by adding a file",
	}
	for in, want := range cases {
		err := validate.Resource(parse(t, in), base, issueSchema)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", in, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err %v, want %q", in, err, want)
		}
	}
}
