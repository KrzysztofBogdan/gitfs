package jira

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

func doc(content string) string { return `{"type":"doc","version":1,"content":[` + content + `]}` }

func TestADFRoundTrip(t *testing.T) {
	cases := []struct {
		name, in, xml string
		back          string // expected JSON after the round trip, when it differs from in
	}{
		{"marks",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"Bursts above "},{"type":"text","text":"200 rps","marks":[{"type":"strong"}]},{"type":"text","text":" see "},{"type":"text","text":"runbook","marks":[{"type":"link","attrs":{"href":"https://x/r"}},{"type":"strong"}]}]}`),
			`<paragraph>Bursts above <strong>200 rps</strong> see <link href="https://x/r"><strong>runbook</strong></link></paragraph>`, ""},
		{"blocks",
			doc(`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Steps"}]},{"type":"orderedList","attrs":{"order":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]}]}`),
			"<heading level=\"2\">Steps</heading>\n<orderedList order=\"3\">\n  <listItem>\n    <paragraph>one</paragraph>\n  </listItem>\n</orderedList>", ""},
		{"cdata",
			doc(`{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"x ]]> y < z & w\nnext"}]}`),
			"<codeBlock language=\"go\"><![CDATA[x ]]]]><![CDATA[> y < z & w\nnext]]></codeBlock>", ""},
		{"mention space",
			doc(`{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"a1","text":"@Adam"}},{"type":"text","text":" "}]}`),
			"<paragraph>\n  <mention id=\"a1\" text=\"@Adam\"/>\n  <text> </text>\n</paragraph>", ""},
		{"table",
			doc(`{"type":"table","attrs":{"isNumberColumnEnabled":false,"layout":"default"},"content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"colspan":2,"colwidth":[120,80]},"content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}]}]}`),
			"<table isNumberColumnEnabled=\"false\" layout=\"default\">\n  <tableRow>\n    <tableCell colspan=\"2\" colwidth=\"[120,80]\">\n      <paragraph>a</paragraph>\n    </tableCell>\n  </tableRow>\n</table>", ""},
		{"block mark",
			doc(`{"type":"paragraph","marks":[{"type":"alignment","attrs":{"align":"center"}}],"content":[{"type":"text","text":"c"}]}`),
			"<alignment align=\"center\">\n  <paragraph>c</paragraph>\n</alignment>", ""},
		{"media",
			doc(`{"type":"mediaSingle","attrs":{"layout":"center"},"content":[{"type":"media","attrs":{"type":"file","id":"552b","collection":"","height":183,"width":200}}]}`),
			"<mediaSingle layout=\"center\">\n  <media collection=\"\" height=\"183\" id=\"552b\" type=\"file\" width=\"200\"/>\n</mediaSingle>", ""},
		{"unknown node",
			doc(`{"type":"futureThing","attrs":{"x":1}}`),
			`<adf-raw>{"type":"futureThing","attrs":{"x":1}}</adf-raw>`, ""},
		{"attribute type mismatch",
			doc(`{"type":"heading","attrs":{"level":"2"},"content":[{"type":"text","text":"x"}]}`),
			`<adf-raw>{"type":"heading","attrs":{"level":"2"},"content":[{"type":"text","text":"x"}]}</adf-raw>`, ""},
		{"unknown mark",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"a "},{"type":"text","text":"b","marks":[{"type":"sparkle"}]}]}`),
			`<paragraph>a <adf-raw>{"type":"text","text":"b","marks":[{"type":"sparkle"}]}</adf-raw></paragraph>`, ""},
		{"null attribute dropped",
			doc(`{"type":"codeBlock","attrs":{"language":null},"content":[{"type":"text","text":"x"}]}`),
			`<codeBlock>x</codeBlock>`,
			doc(`{"type":"codeBlock","content":[{"type":"text","text":"x"}]}`)},
		{"adjacent text merged",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"text","text":"c","marks":[{"type":"em"}]},{"type":"text","text":"d","marks":[{"type":"em"}]}]}`),
			`<paragraph>ab<em>cd</em></paragraph>`,
			doc(`{"type":"paragraph","content":[{"type":"text","text":"ab"},{"type":"text","text":"cd","marks":[{"type":"em"}]}]}`)},
	}
	for _, c := range cases {
		nodes, err := adfToNodes(json.RawMessage(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		body := &xmltree.Node{Kind: xmltree.Element, Name: "description", Children: nodes}
		if got := xmltree.PrintInner(body); got != c.xml {
			t.Errorf("%s: xml\n%s\nwant\n%s", c.name, got, c.xml)
			continue
		}
		// what a file holds: the body printed at some depth, parsed back
		parsed, err := xmltree.ParseString(xmltree.Print(body, 3))
		if err != nil {
			t.Errorf("%s: reparse: %v", c.name, err)
			continue
		}
		v, err := nodesToADF(parsed.Children)
		if err != nil {
			t.Errorf("%s: back: %v", c.name, err)
			continue
		}
		got, _ := json.Marshal(v)
		want := c.back
		if want == "" {
			want = c.in
		}
		if !jsonEqual(t, got, []byte(want)) {
			t.Errorf("%s: back\n%s\nwant\n%s", c.name, got, want)
		}
	}
}

func TestADFEmpty(t *testing.T) {
	if n, err := adfToNodes(json.RawMessage("null")); n != nil || err != nil {
		t.Fatal(n, err)
	}
	if v, err := nodesToADF(nil); v != nil || err != nil {
		t.Fatal(v, err)
	}
	ws, _ := xmltree.ParseString("<d>\n  \n</d>")
	if v, err := nodesToADF(ws.Children); v != nil || err != nil {
		t.Fatal(v, err)
	}
}

func TestADFFromFileErrors(t *testing.T) {
	cases := map[string]string{
		`<d><blink>x</blink></d>`:                       "unknown ADF element <blink>",
		`<d><heading level="two">x</heading></d>`:       `<heading level="two">: want a number`,
		`<d><table isNumberColumnEnabled="yes"/></d>`:   `<table isNumberColumnEnabled="yes">: want true or false`,
		`<d><strong><adf-raw>{}</adf-raw></strong></d>`: "<adf-raw> cannot be inside a mark",
		`<d><adf-raw>{nope</adf-raw></d>`:               "<adf-raw>:",
		`<d>plain words</d>`:                            "text outside a block: wrap it in <paragraph>",
	}
	for in, want := range cases {
		n, _ := xmltree.ParseString(in)
		if _, err := nodesToADF(n.Children); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", in, err, want)
		}
	}
}

func TestADFUserWrittenInline(t *testing.T) {
	// what a user types: wrappers nested their own way, whitespace between blocks
	n, _ := xmltree.ParseString("<d>\n  <paragraph>Hi <strong>big <em>world</em></strong></paragraph>\n  <rule/>\n</d>")
	v, err := nodesToADF(n.Children)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(v)
	want := doc(`{"type":"paragraph","content":[{"type":"text","text":"Hi "},{"type":"text","text":"big ","marks":[{"type":"strong"}]},{"type":"text","text":"world","marks":[{"type":"strong"},{"type":"em"}]}]},{"type":"rule"}`)
	if !jsonEqual(t, got, []byte(want)) {
		t.Fatalf("%s", got)
	}
}
