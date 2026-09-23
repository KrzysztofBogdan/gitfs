package adapter

import "testing"

func TestAttachmentTargets(t *testing.T) {
	if got := AttachmentTarget("attachment", "att9"); got != "attachment[id=att9]" {
		t.Fatal(got)
	}
	if got := NewAttachmentTarget("attachment", "a b].png"); got != "attachment[file=a b].png]" {
		t.Fatal(got)
	}
	if id, file, ok := ParseAttachmentTarget("attachment[id=att9]"); !ok || id != "att9" || file != "" {
		t.Fatal(id, file, ok)
	}
	if id, file, ok := ParseAttachmentTarget("attachment[file=a b].png]"); !ok || id != "" || file != "a b].png" {
		t.Fatal(id, file, ok)
	}
	if _, _, ok := ParseAttachmentTarget("comment[2]"); ok {
		t.Fatal("comment[2] is not an attachment target")
	}
	if (Action{Verb: "update"}).IsAttachment() || !(Action{Verb: "update", File: "x.files/a"}).IsAttachment() {
		t.Fatal("IsAttachment")
	}
}
