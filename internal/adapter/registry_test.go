package adapter

import (
	"context"
	"net/url"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

type stub struct{}

func (stub) Name() string                                                       { return "stub" }
func (stub) Schemes() []string                                                  { return []string{"stub", "stub+x"} }
func (stub) Schema() *schema.Schema                                             { return nil }
func (stub) PathModel() PathModel                                               { return Tree }
func (stub) DefaultDir(*url.URL) string                                         { return "stub" }
func (stub) Verbs() []Verb                                                      { return nil }
func (stub) Describe(*Action, *Resource)                                        {}
func (stub) Open(context.Context, *url.URL, map[string]string) (Session, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	Register(stub{})
	a, u, err := ForURL("stub+x://host/SPACE")
	if err != nil || a.Name() != "stub" || u.Host != "host" {
		t.Fatalf("%v %v %v", a, u, err)
	}
	if _, _, err := ForURL("nope://x"); err == nil {
		t.Fatal("unknown scheme must fail")
	}
}

func TestIsMove(t *testing.T) {
	cases := []struct {
		m        PathModel
		from, to string
		want     bool
	}{
		{Tree, "a/b.xml", "a/c.xml", true},
		{Tree, "a/b.xml", "a/b.xml", false},
		{Flat, "a/b.xml", "c/d.xml", false},
		{DirTree, "inbox/x.xml", "inbox/y.xml", false},
		{DirTree, "inbox/x.xml", "archive/x.xml", true},
	}
	for _, c := range cases {
		if got := IsMove(c.m, c.from, c.to); got != c.want {
			t.Errorf("IsMove(%v,%s,%s)=%v", c.m, c.from, c.to, got)
		}
	}
}
