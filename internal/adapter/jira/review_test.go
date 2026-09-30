package jira

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// A new file whose comments include a public one must ask, like any reply.
func TestCreateWithPublicCommentAsks(t *testing.T) {
	a := adapter.Action{Verb: "create"}
	root := parse(t, `<issue><type>Support</type><comment public="true"><paragraph>hi</paragraph></comment></issue>`)
	(&Adapter{}).Describe(&a, &adapter.Resource{Path: "sup/New.xml", Root: root})
	if a.Class != "reply" || !strings.Contains(a.Detail, "public reply") {
		t.Fatalf("%s %q", a.Class, a.Detail)
	}
}

// Characters XML 1.0 forbids (ANSI escapes in pasted logs) must never reach a file.
func TestIllegalXMLCharactersStayParseable(t *testing.T) {
	var is apiIssue
	json.Unmarshal([]byte(`{"id":"1","key":"GEN-1","fields":{"summary":"bad \u001b[31mred","issuetype":{"name":"Task"},"status":{"name":"To Do"},
	 "description":{"type":"doc","version":1,"content":[{"type":"codeBlock","content":[{"type":"text","text":"log \u001b[31mFAIL\u001b[0m"}]}]}}}`), &is)
	root, err := decoder{reg: newRegistry()}.issue(is, map[string]fieldMeta{"description": {ID: "description"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := envelope.Bytes(envelope.New(root), issueSchema)
	doc, err := envelope.Parse(data)
	if err != nil {
		t.Fatalf("file does not parse: %v\n%s", err, data)
	}
	back, err := nodesToADF(doc.Content.Child("description").Children)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(back)
	if !strings.Contains(string(b), `log \u001b[31mFAIL\u001b[0m`) {
		t.Fatalf("the ADF text must round-trip: %s", b)
	}
}

// When the transition cannot run, fields that were to travel with it must fail too.
func TestTransitionFieldsFailWithTransition(t *testing.T) {
	for i := 0; i < 20; i++ {
		srv, now := site(t)
		supportFlow(srv)
		srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
			Statuses: []jtest.Status{{ID: "11", Name: "In Progress", Category: "indeterminate"}, {ID: "12", Name: "Closed", Category: "done"}},
			Transitions: []jtest.Transition{{ID: "31", Name: "Resolve", To: "Closed",
				Screen: []jtest.ScreenField{{ID: "resolution", Required: true}, {ID: "priority", Required: true}}}}})
		s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
		req, out := change(t, s, srv.ID("SUP-1"), func(r *xmltree.Node) {
			setText(r, "status", "Closed")
			setText(r, "resolution", "Done")
		}, "status", "resolution")
		apply(t, s, req, out)
		if out[0].Err == nil || out[1].Err == nil || !strings.Contains(out[1].Err.Error(), "not sent") {
			t.Fatalf("run %d: %v / %v", i, out[0].Err, out[1].Err)
		}
	}
}
