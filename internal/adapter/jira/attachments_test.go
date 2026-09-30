package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestAttachments(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	sidecar := "gen/GEN-1 Migrate auth.files/"
	create := adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "notes.txt"), File: sidecar + "notes.txt"}
	req := adapter.ApplyRequest{Open: func(rel string) (io.ReadCloser, error) {
		if rel != sidecar+"notes.txt" {
			t.Fatalf("opened %s", rel)
		}
		return io.NopCloser(strings.NewReader("hello")), nil
	}}
	attID, err := s.attachment(bg, id, req, create)
	if err != nil || attID == "" {
		t.Fatal(attID, err)
	}
	r, _ := s.Fetch(bg, id)
	a := r.Root.Child("attachment")
	if name, _ := a.Attr("name"); name != "notes.txt" {
		t.Fatalf("%s", xmltree.Print(r.Root, 0))
	}
	var b strings.Builder
	if _, err := s.Download(bg, id, attID, &b); err != nil || b.String() != "hello" {
		t.Fatal(err, b.String())
	}
	del := adapter.Action{Verb: "delete", Target: adapter.AttachmentTarget("attachment", attID), File: sidecar + "notes.txt"}
	if _, err := s.attachment(bg, id, adapter.ApplyRequest{}, del); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Fetch(bg, id); r.Root.Child("attachment") != nil {
		t.Fatal("still attached")
	}
	upd := adapter.Action{Verb: "update", Target: adapter.AttachmentTarget("attachment", attID), File: sidecar + "notes.txt"}
	if _, err := s.attachment(bg, id, adapter.ApplyRequest{}, upd); err == nil || !strings.Contains(err.Error(), "delete the file and add it again") {
		t.Fatal(err)
	}
	if !issueSchema.Attachment().Allows("create") || issueSchema.Attachment().Allows("update") {
		t.Fatal("the schema must offer create and delete only")
	}
}
