package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestFakeAttachments(t *testing.T) {
	a := New()
	a.Remote.Put("1", "n.xml", `<note><title>T</title></note>`)
	a.Remote.PutAttachment("1", "a1", "x.png", []byte("abc"))
	ctx := context.Background()
	s, _ := a.Open(ctx, nil, nil)
	res, err := s.Fetch(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got := xmltree.Print(res.Root, 0); !strings.Contains(got, `<attachment id="a1" name="x.png" size="3" version="1" created="2026-01-01T00:00:00Z"/>`) {
		t.Fatalf("element missing:\n%s", got)
	}
	var buf bytes.Buffer
	info, err := s.Download(ctx, "1", "a1", &buf)
	if err != nil || buf.String() != "abc" || info.Version != "1" || info.Size != 3 || a.Remote.Downloads != 1 {
		t.Fatalf("%+v %v %q", info, err, buf.String())
	}
	files := map[string]string{"n.files/x.png": "abcd", "n.files/new.csv": "a,b"}
	open := func(rel string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(files[rel])), nil }
	out := s.Apply(ctx, adapter.ApplyRequest{Local: res, Base: res, Lock: "1", Open: open, Actions: []adapter.Action{
		{Verb: "update", Target: "attachment[id=a1]", File: "n.files/x.png"},
		{Verb: "create", Target: "attachment[file=new.csv]", File: "n.files/new.csv"},
	}})
	if len(out) != 2 || out[0].Err != nil || out[0].Version != "2" || out[1].Err != nil || out[1].ID == "" || out[1].Version != "1" {
		t.Fatalf("%+v", out)
	}
	if data, v, _ := a.Remote.Attachment("1", "a1"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
	if id, ok := a.Remote.AttachmentNamed("1", "new.csv"); !ok || id != out[1].ID {
		t.Fatal(id, ok)
	}
	after, _ := s.Fetch(ctx, "1")
	if after.Version != "1" {
		t.Fatal("attachment changes must not bump the resource version")
	}
	out = s.Apply(ctx, adapter.ApplyRequest{Local: after, Base: after, Lock: "1",
		Actions: []adapter.Action{{Verb: "delete", Target: "attachment[id=a1]", File: "n.files/x.png"}}})
	if out[0].Err != nil {
		t.Fatalf("%+v", out)
	}
	if _, _, ok := a.Remote.Attachment("1", "a1"); ok {
		t.Fatal("not deleted")
	}
	if _, err := s.Download(ctx, "1", "a1", &buf); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	a.ReadOnlyAttachments()
	if a.Schema().Attachment().Allows("create") || !Schema.Attachment().Allows("create") {
		t.Fatal("ReadOnlyAttachments must change this adapter only")
	}
}
