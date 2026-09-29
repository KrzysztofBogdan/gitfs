package confluence

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func opener(files map[string]string) func(string) (io.ReadCloser, error) {
	return func(rel string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(files[rel])), nil }
}

func TestAttachmentsFetchDownloadApply(t *testing.T) {
	srv, sess := engSpace(t)
	att := srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "rollback-flow.png", MediaType: "image/png", Data: []byte("png1")})
	res := resource(t, sess, "98130")
	want := `<attachment id="` + att.ID + `" name="rollback-flow.png" type="image/png" size="4" version="1" created="2026-03-01T10:00:00.000Z" author="Me"/>`
	if got := xmltree.Print(res.Root, 0); !strings.Contains(got, want) {
		t.Fatalf("missing\n%s\nin\n%s", want, got)
	}
	var buf bytes.Buffer
	info, err := sess.Download(bg, "98130", att.ID, &buf)
	if err != nil || buf.String() != "png1" || info.Version != "1" || info.Size != 4 {
		t.Fatalf("%+v %v %q", info, err, buf.String())
	}
	open := opener(map[string]string{"eng/Home/Runbooks.files/rollback-flow.png": "png2", "eng/Home/Runbooks.files/oncall.csv": "a,b"})
	local := &adapter.Resource{ID: res.ID, Path: res.Path, Root: res.Root.Clone()}
	out := sess.Apply(bg, adapter.ApplyRequest{Local: local, Base: res, Lock: res.Version, IDByPath: known, Open: open, Actions: []adapter.Action{
		{Verb: "update", Target: "attachment[id=" + att.ID + "]", File: "eng/Home/Runbooks.files/rollback-flow.png"},
		{Verb: "create", Target: "attachment[file=oncall.csv]", File: "eng/Home/Runbooks.files/oncall.csv"},
	}})
	if len(out) != 2 || out[0].Err != nil || out[0].Version != "2" || out[1].Err != nil || out[1].ID == "" || out[1].Version != "1" {
		t.Fatalf("%+v", out)
	}
	if a, _ := srv.Attachment(att.ID); string(a.Data) != "png2" || a.Version != 2 {
		t.Fatalf("%+v", a)
	}
	if p, _ := srv.Page("98130"); p.Version != 1 {
		t.Fatal("attachment changes must not bump the page version")
	}
	del := sess.Apply(bg, adapter.ApplyRequest{Local: local, Base: res, Lock: res.Version, IDByPath: known,
		Actions: []adapter.Action{{Verb: "delete", Target: "attachment[id=" + att.ID + "]", File: "eng/Home/Runbooks.files/rollback-flow.png"}}})
	if del[0].Err != nil {
		t.Fatalf("%+v", del)
	}
	if _, ok := srv.Attachment(att.ID); ok {
		t.Fatal("not deleted")
	}
	srv.Fail = map[string]int{"POST /wiki/rest/api/content/98130/child/attachment": 500}
	failed := sess.Apply(bg, adapter.ApplyRequest{Local: local, Base: res, Lock: res.Version, IDByPath: known, Open: open,
		Actions: []adapter.Action{{Verb: "create", Target: "attachment[file=oncall.csv]", File: "eng/Home/Runbooks.files/oncall.csv"}}})
	if failed[0].Err == nil || failed[0].Code != "500" {
		t.Fatalf("%+v", failed)
	}
}

func TestCreatePageWithAttachment(t *testing.T) {
	srv, sess := engSpace(t)
	root, _ := xmltree.ParseString(`<page><title>Rollback</title><body type="application/xhtml+xml"><p>x</p></body></page>`)
	out := sess.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{Path: "eng/Home/Runbooks/Rollback.xml", Root: root},
		IDByPath: known, Open: opener(map[string]string{"eng/Home/Runbooks/Rollback.files/f.txt": "f"}),
		Actions: []adapter.Action{{Verb: "create"}, {Verb: "create", Target: "attachment[file=f.txt]", File: "eng/Home/Runbooks/Rollback.files/f.txt"}}})
	if len(out) != 2 || out[0].Err != nil || out[1].Err != nil || out[1].Version != "1" {
		t.Fatalf("%+v", out)
	}
	if as := srv.Attachments(out[0].ID); len(as) != 1 || as[0].Title != "f.txt" || string(as[0].Data) != "f" {
		t.Fatalf("%+v", as)
	}
}

func TestAttachmentCheckAndDescribe(t *testing.T) {
	srv, sess := engSpace(t)
	srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "a.png"})
	res := resource(t, sess, "98130")
	chk := sess.Check(bg, adapter.ApplyRequest{Local: res, Base: res, IDByPath: known,
		Actions: []adapter.Action{{Verb: "create", Target: "attachment[file=a.png]", File: "eng/Home/Runbooks.files/a.png"}}})
	if chk[0].Err == nil || !strings.Contains(chk[0].Err.Error(), "already exists") {
		t.Fatalf("%+v", chk)
	}
	ad := &Adapter{}
	for _, c := range []struct {
		a    adapter.Action
		want string
	}{
		{adapter.Action{Verb: "create", Target: "attachment[file=b.png]", File: "x.files/b.png"}, `attach to "Runbooks"`},
		{adapter.Action{Verb: "update", Target: "attachment[id=att1]", File: "x.files/a.png"}, "upload new version of attachment"},
		{adapter.Action{Verb: "delete", Target: "attachment[id=att1]", File: "x.files/a.png"}, "delete attachment"},
	} {
		a := c.a
		ad.Describe(&a, res)
		if a.Detail != c.want || a.Class != a.Verb {
			t.Errorf("%s: %q class %q", a.Verb, a.Detail, a.Class)
		}
	}
}

func TestSanitizeReservesFilesSuffix(t *testing.T) {
	if got := sanitize("Assets.files"); got != "Assets.files_" {
		t.Fatal(got)
	}
	if got := pagePaths("eng", []pageRef{{ID: "1", Title: "Assets.files"}})["1"]; got != "eng/Assets.files_.xml" {
		t.Fatal(got)
	}
}
