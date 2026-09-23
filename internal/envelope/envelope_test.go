package envelope

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const bare = `<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <page id="1">
      <title>T</title>
    </page>
  </content>
</gfs>
`

func TestRoundTripBare(t *testing.T) {
	d, err := Parse([]byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if d.Wrapped || d.Action != "" || d.Content.Name != "page" {
		t.Fatalf("%+v", d)
	}
	if got := string(Bytes(d, nil)); got != bare {
		t.Fatalf("got\n%s", got)
	}
}

func TestLenientBareRoot(t *testing.T) {
	d, err := Parse([]byte("<page>\n<title>T</title></page>"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Wrapped {
		t.Fatal("want Wrapped")
	}
	d.Content.SetAttr("id", "1")
	if got := string(Bytes(d, nil)); got != bare {
		t.Fatalf("got\n%s", got)
	}
}

func TestActionParamsErrorsConflict(t *testing.T) {
	in := `<gfs to="x" action="send" cc="y"><errors><error code="550" action="send" at="t1"><msg>550 &lt;bob&gt; rejected</msg></error></errors><content><mail/></content></gfs>`
	d, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "send" || d.Params["to"] != "x" || d.Params["cc"] != "y" || len(d.Errors) != 1 || d.Errors[0].Msg != "550 <bob> rejected" {
		t.Fatalf("%+v", d)
	}
	d.Conflict = &Conflict{RemoteVersion: "8", By: "bob", At: "t2", Elements: 1, Hunks: 2}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<gfs action="send" cc="y" to="x">
  <errors>
    <error action="send" code="550" at="t1">
      <msg><![CDATA[550 <bob> rejected]]></msg>
    </error>
  </errors>
  <conflict remote-version="8" by="bob" at="t2" elements="1" hunks="2"/>
  <content>
    <mail/>
  </content>
</gfs>
`
	if got := string(Bytes(d, nil)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	b := d.Bare()
	if b.Action != "" || len(b.Params) != 0 || b.Errors != nil || b.Conflict != nil || d.Action != "send" {
		t.Fatal("Bare must copy and strip")
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{
		`<gfs/>`,
		`<gfs><content/></gfs>`,
		`<gfs><content><a/><b/></content></gfs>`,
		`<gfs><content><a/></content><extra/></gfs>`,
		`not xml`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q): want error", in)
		}
	}
}

func TestMarkers(t *testing.T) {
	conflicted := "<gfs>\n  <conflict remote-version=\"8\"/>\n  <content>\n<<<<<<< local\na\n||||||| base\n=======\nb\n>>>>>>> remote v8\n"
	if !HasMarkers([]byte(conflicted)) || !HasConflictElement([]byte(conflicted)) {
		t.Fatal("want markers and conflict element")
	}
	if HasMarkers([]byte(bare)) || HasConflictElement([]byte(bare)) {
		t.Fatal("bare has none")
	}
	if HasConflictElement([]byte("<gfs>\n  <content>\n    <x><conflict/></x>")) {
		t.Fatal("conflict inside content is not the envelope element")
	}
}

func TestHeaderFooter(t *testing.T) {
	d := New(&xmltree.Node{Kind: xmltree.Element, Name: "page"})
	full := string(Bytes(d, nil))
	if !strings.HasPrefix(full, Header(d)) || !strings.HasSuffix(full, Footer) {
		t.Fatalf("header/footer do not frame %q", full)
	}
}
