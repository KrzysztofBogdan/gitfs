package fake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestFakeCreateUpdateLock(t *testing.T) {
	a := New()
	ctx := context.Background()
	s, _ := a.Open(ctx, nil, nil)
	root, _ := xmltree.ParseString(`<note><title>T</title><comment>hi</comment></note>`)
	res := s.Apply(ctx, adapter.ApplyRequest{
		Local:   &adapter.Resource{Path: "n/a.xml", Root: root},
		Actions: []adapter.Action{{Verb: "create"}},
	})
	if len(res) != 1 || res[0].Err != nil || res[0].ID == "" {
		t.Fatalf("%+v", res)
	}
	got, err := s.Fetch(ctx, res[0].ID)
	if err != nil || got.Path != "n/a.xml" || got.Version != "1" {
		t.Fatalf("%+v %v", got, err)
	}
	if c := got.Root.Child("comment"); c == nil {
		t.Fatal("comment lost")
	} else if _, ok := c.Attr("id"); !ok {
		t.Fatal("comment id not assigned")
	}

	a.Remote.Edit(res[0].ID, func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	stale := s.Apply(ctx, adapter.ApplyRequest{Local: got, Base: got, Lock: "1",
		Actions: []adapter.Action{{Verb: "update", Group: "title"}}})
	if !errors.Is(stale[0].Err, adapter.ErrLock) {
		t.Fatalf("want ErrLock, got %+v", stale)
	}

	a.Remote.FailVerb = map[string]error{"delete": errors.New("403 forbidden")}
	r := s.Apply(ctx, adapter.ApplyRequest{Local: got, Base: got, Lock: "2", Actions: []adapter.Action{{Verb: "delete"}}})
	if r[0].Err == nil || !strings.Contains(r[0].Err.Error(), "403") {
		t.Fatalf("%+v", r)
	}
	l, _ := s.List(ctx, "")
	if !l.Full || len(l.Resources) != 1 {
		t.Fatalf("%+v", l)
	}
}
