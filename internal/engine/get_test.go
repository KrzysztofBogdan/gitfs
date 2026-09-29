package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

// attached clones a fake remote whose notes carry attachments, two with the same name.
func attached(t *testing.T) (*Env, *fake.Adapter, *bytes.Buffer) {
	t.Helper()
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	ad.Remote.PutAttachment("1", "11", "x.png", []byte("second"))
	ad.Remote.Put("2", "b/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.PutAttachment("2", "20", "report.pdf", []byte("pdf"))
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	out.Reset()
	return env, ad, &out
}

func fetchAll(t *testing.T, env *Env, out *bytes.Buffer) {
	t.Helper()
	if r, err := Get(ctx, env, []string{""}); err != nil || r.ExitCode() != 0 {
		t.Fatalf("%+v %v\n%s", r, err, out)
	}
	out.Reset()
}

func TestGet(t *testing.T) {
	env, _, out := attached(t)
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("a clone with unfetched attachments is clean: %+v", cs)
	}
	if env.Tree.Exists("a/one.files") {
		t.Fatal("clone must not download attachment bytes")
	}
	r, err := Get(ctx, env, []string{"a/one.xml"})
	if err != nil || r.Fetched != 2 || r.ExitCode() != 0 {
		t.Fatalf("%+v %v\n%s", r, err, out)
	}
	if read(t, env, "a/one.files/x.png") != "abc" || read(t, env, "a/one.files/x (2).png") != "second" {
		t.Fatal("bytes")
	}
	for _, want := range []string{"  +  a/one.files/x.png   3 B   v1", "2 fetched, 0 up to date, 0 refused, 0 failed (9 B)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	atts, _ := env.Tree.LoadAttachments()
	if l, ok := atts.Get("1", "11"); !ok || l.Path != "a/one.files/x (2).png" || l.Version != "1" {
		t.Fatalf("tracking line not saved: %+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("fetched attachments are clean: %+v", cs)
	}

	out.Reset()
	if r, _ = Get(ctx, env, []string{"a/one.files/x.png"}); r.UpToDate != 1 || !strings.Contains(out.String(), "  =  a/one.files/x.png   up to date") {
		t.Fatal(out)
	}

	write(t, env, "a/one.files/x.png", "abd")
	out.Reset()
	r, _ = Get(ctx, env, []string{"a"})
	if r.Refused != 1 || r.UpToDate != 1 || r.ExitCode() != 1 || !strings.Contains(out.String(), "a/one.files/x.png   changed locally") {
		t.Fatalf("%+v\n%s", r, out)
	}

	write(t, env, "b/two.files/report.pdf", "mine")
	out.Reset()
	r, _ = Get(ctx, env, []string{"b/two.xml", "nope.xml"})
	if r.Refused != 2 || !strings.Contains(out.String(), "not fetched by gfs") || !strings.Contains(out.String(), "nope.xml   not a resource") {
		t.Fatalf("%+v\n%s", r, out)
	}
}

func TestGetReportsStaleElement(t *testing.T) {
	env, ad, out := attached(t)
	ad.Remote.EditAttachment("2", "20", []byte("pdf2"))
	if _, err := Get(ctx, env, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "b/two.files/report.pdf   4 B   v2   (remote has v2; run gfs pull)") {
		t.Fatal(out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("a newer download is not a local change: %+v", cs)
	}
}
