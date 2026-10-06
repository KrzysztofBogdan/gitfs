package engine

import (
	"path"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

// treeParent is a Confluence-like parent: a/p/c.xml needs a/p.xml.
func treeParent(p string) string {
	d := path.Dir(p)
	if !strings.Contains(d, "/") {
		return ""
	}
	return d + ".xml"
}

// nested makes the fake tree-shaped: a new file depends on, and is created
// under, the file of its folder.
func nested(ad *fake.Adapter) {
	ad.Deps = func(l *adapter.Resource) []string {
		if p := treeParent(l.Path); p != "" {
			return []string{p}
		}
		return nil
	}
	ad.Remote.Parent = treeParent
}

func noteXML(title string) string { return "<note><title>" + title + "</title></note>" }

func idOf(t *testing.T, env *Env, p string) string {
	t.Helper()
	en, ok := env.Index.ByPath(p)
	if !ok {
		t.Fatalf("%s is not tracked", p)
	}
	return en.ID
}

func TestCommitCreatesParentFirst(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Deps = func(l *adapter.Resource) []string {
		if l.Path == "a/1 child.xml" {
			return []string{"a/2 parent.xml"}
		}
		return nil
	}
	ad.Remote.Parent = func(p string) string {
		if p == "a/1 child.xml" {
			return "a/2 parent.xml"
		}
		return ""
	}
	write(t, env, "a/1 child.xml", noteXML("Child"))
	write(t, env, "a/2 parent.xml", noteXML("Parent"))
	write(t, env, "a/0 other.xml", noteXML("Other"))
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	want := "create a/0 other.xml|create a/2 parent.xml|create a/1 child.xml"
	if got := strings.Join(ad.Remote.Calls, "|"); got != want {
		t.Fatalf("order: %s", got)
	}
	if ad.Remote.Parents[idOf(t, env, "a/1 child.xml")] != idOf(t, env, "a/2 parent.xml") {
		t.Fatalf("%v", ad.Remote.Parents)
	}
}

func TestCommitRefusesCycleAndMissingParent(t *testing.T) {
	env, ad, out := cloned(t)
	deps := map[string][]string{"a/x.xml": {"a/y.xml"}, "a/y.xml": {"a/x.xml"}, "a/c.xml": {"a/nope.xml"}}
	ad.Deps = func(l *adapter.Resource) []string { return deps[l.Path] }
	for _, p := range []string{"a/x.xml", "a/y.xml", "a/c.xml", "a/z.xml"} {
		write(t, env, p, noteXML(p))
	}
	r := commit(t, env, CommitOpts{})
	if r.Failed != 3 || strings.Join(ad.Remote.Calls, "|") != "create a/z.xml" {
		t.Fatalf("%+v %v\n%s", r, ad.Remote.Calls, out)
	}
	for _, s := range []string{"parent cycle: a/x.xml -> a/y.xml -> a/x.xml",
		"parent a/nope.xml is not tracked and not part of this commit"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("missing %q:\n%s", s, out)
		}
	}
}
