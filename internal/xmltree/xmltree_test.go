package xmltree

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *Node {
	t.Helper()
	n, err := ParseString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

func TestPrintCases(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty element", `<a  x="1" ></a>`, `<a x="1"/>`},
		{"text only", `<a>hi there</a>`, `<a>hi there</a>`},
		{"text keeps newlines", "<a>line1\n\n  line2</a>", "<a>line1\n\n  line2</a>"},
		{"element only reindented", "<a><b>1</b>\n      <c/></a>", "<a>\n  <b>1</b>\n  <c/>\n</a>"},
		{"nested element only", "<a><b><c>x</c></b></a>", "<a>\n  <b>\n    <c>x</c>\n  </b>\n</a>"},
		{"mixed verbatim", "<p>Fixed in <code>4f2a</code>.\n  ok</p>", "<p>Fixed in <code>4f2a</code>.\n  ok</p>"},
		{"mixed inline children stay inline", "<p>a <b><i>x</i> <i>y</i></b></p>", "<p>a <b><i>x</i> <i>y</i></b></p>"},
		{"cdata for lt", "<a>x &lt; y</a>", "<a><![CDATA[x < y]]></a>"},
		{"cdata for amp", "<a><![CDATA[Q&A]]></a>", "<a><![CDATA[Q&A]]></a>"},
		{"cdata terminator split", "<a>&lt;]]&gt;</a>", "<a><![CDATA[<]]]]><![CDATA[>]]></a>"},
		{"gt stays raw", "<a>x &gt; y</a>", "<a>x > y</a>"},
		{"attr escaping", `<a m="&lt;id@x&gt;" q='say "hi"' n="a&#10;b"/>`, `<a m="&lt;id@x&gt;" q="say &quot;hi&quot;" n="a&#10;b"/>`},
		{"prefixes kept", `<body xmlns:ac="u"><ac:macro ac:name="info"/></body>`, "<body xmlns:ac=\"u\">\n  <ac:macro ac:name=\"info\"/>\n</body>"},
		{"undeclared prefix ok", `<ac:link><ri:page ri:content-title="A"/></ac:link>`, "<ac:link>\n  <ri:page ri:content-title=\"A\"/>\n</ac:link>"},
		{"comment on own line", "<a><!-- c --><b/></a>", "<a>\n  <!-- c -->\n  <b/>\n</a>"},
		{"html entity accepted", "<p>a&nbsp;b</p>", "<p>a b</p>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Print(mustParse(t, c.in), 0)
			if got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestPrintIdempotent(t *testing.T) {
	in := `<page id="1"><title>T</title><body type="application/xhtml+xml" xmlns:ac="u">
<p>Retry budget is <ac:inline-comment-marker ac:ref="c9a1">3 per request</ac:inline-comment-marker>.</p>
<table><tbody><tr><th>S</th><th>O</th></tr></tbody></table>
<ac:plain-text-body><![CDATA[./deploy.sh --tag v2 && echo ok]]></ac:plain-text-body></body></page>`
	once := Print(mustParse(t, in), 0)
	twice := Print(mustParse(t, once), 0)
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

func TestPrintDepthAndRaw(t *testing.T) {
	n := mustParse(t, "<a><b/></a>")
	n.Children = append(n.Children, &Node{Kind: Raw, Text: "<<<<<<< local\n=======\n>>>>>>> remote v2"})
	got := Print(n, 1)
	want := "  <a>\n    <b/>\n<<<<<<< local\n=======\n>>>>>>> remote v2\n  </a>"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestPrintInner(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<body><p>a</p><p>b</p></body>", "<p>a</p>\n<p>b</p>"},
		{"<body>plain &amp; text</body>", "<![CDATA[plain & text]]>"},
		{"<body><ul><li>x</li></ul></body>", "<ul>\n  <li>x</li>\n</ul>"},
	}
	for _, c := range cases {
		if got := PrintInner(mustParse(t, c.in)); got != c.want {
			t.Errorf("PrintInner(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "<a>", "<a></b>", "<a/><b/>", "text", "<!DOCTYPE x><a/>"} {
		if _, err := ParseString(in); err == nil {
			t.Errorf("ParseString(%q): want error", in)
		}
	}
}

func TestParseSkipsDeclAndTopLevelNoise(t *testing.T) {
	n := mustParse(t, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- c -->\n<gfs><content/></gfs>\n")
	if n.Name != "gfs" || n.Child("content") == nil {
		t.Fatalf("got %s", Print(n, 0))
	}
}

func TestHelpers(t *testing.T) {
	n := mustParse(t, `<a x="1"><b>t</b><c/><b>u</b></a>`)
	if v, ok := n.Attr("x"); !ok || v != "1" {
		t.Fatal("Attr")
	}
	n.SetAttr("y", "2")
	n.SetAttr("x", "3")
	n.DelAttr("nope")
	if got := Print(n, 0); !strings.HasPrefix(got, `<a x="3" y="2">`) {
		t.Fatalf("SetAttr order: %s", got)
	}
	if len(n.ChildrenNamed("b")) != 2 || n.Child("b").TextContent() != "t" || len(n.Elements()) != 3 {
		t.Fatal("child helpers")
	}
	c := n.Clone()
	c.Child("b").Children[0].Text = "changed"
	if n.Child("b").TextContent() != "t" {
		t.Fatal("Clone is shallow")
	}
	if !Equal(n, n.Clone()) || Equal(n, c) {
		t.Fatal("Equal")
	}
}
