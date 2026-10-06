package bitbucket

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func fixture(t *testing.T, name string) *xmltree.Node {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	n, err := xmltree.ParseString(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMergedSchema(t *testing.T) {
	s := mergedSchema
	if s.Root != "pullrequest" || !slices.Equal(s.Roots, []string{"pipeline"}) {
		t.Fatal(s.Root, s.Roots)
	}
	var names []string
	for _, e := range s.Elems {
		names = append(names, e.Name)
	}
	want := "title state result trigger target pr duration step steplog draft source destination close-source-branch author reviewer participant my-review merge-commit description build comment"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("order:\n%s\nwant\n%s", got, want)
	}
	if a := s.Attachment(); a == nil || a.Name != "steplog" || len(a.Ops) != 0 {
		t.Fatalf("%+v", a)
	}
	for _, f := range []string{"schema-pr.xml", "schema-run.xml"} {
		n := fixture(t, f)
		if err := validate.Resource(n, n.Clone(), s); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	pr := fixture(t, "schema-pr.xml")
	changed := pr.Clone()
	changed.SetAttr("number", "7")
	if err := validate.Resource(changed, pr, s); err == nil || !strings.Contains(err.Error(), `"number"`) {
		t.Fatal(err)
	}
}

func TestStepOrder(t *testing.T) {
	n := fixture(t, "schema-run.xml")
	canon.Normalize(n, mergedSchema)
	steps := n.ChildrenNamed("step")
	if id, _ := steps[0].Attr("id"); id != "s9" {
		t.Fatalf("step 9 before step 10:\n%s", xmltree.Print(n, 0))
	}
}

func TestRelaxNG(t *testing.T) {
	g := RelaxNG()
	if _, err := xmltree.ParseString(g); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`<element name="pullrequest">`, `<element name="pipeline">`, `<element name="steplog">`, `<element name="comment">`} {
		if !strings.Contains(g, w) {
			t.Fatalf("lacks %s", w)
		}
	}
}
