package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

type progressLog []adapter.Progress

func (l *progressLog) add(p adapter.Progress) { *l = append(*l, p) }

// phases compresses the log to one letter per event: l(ist) f(etch) d(one).
func (l progressLog) phases() string {
	var b strings.Builder
	for _, p := range l {
		b.WriteByte(p.Phase[0])
	}
	return b.String()
}

func TestCloneReportsProgress(t *testing.T) {
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.Put("2", "a/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.Stubs = true
	sess, _ := ad.Open(ctx, nil, nil)
	var log progressLog
	var out bytes.Buffer
	if _, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, log.add); err != nil {
		t.Fatal(err)
	}
	if log.phases() != "lffd" || log[2].Done != 1 || log[2].Total != 2 || log[1].Item == "" {
		t.Fatalf("%+v", log)
	}
}

func TestPullReportsProgress(t *testing.T) {
	env, _, _ := cloned(t)
	var log progressLog
	env.Progress = log.add
	pull(t, env, PullOpts{})
	if log.phases() != "lffd" || log[1].Total != 2 {
		t.Fatalf("%+v", log)
	}
}

func TestPullFullRefetchesUnchangedStubs(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Stubs = true
	ad.Remote.EditSilently("1", setTitle("Quiet"))
	pull(t, env, PullOpts{})
	if b, _ := env.Tree.ReadFile("a/one.xml"); strings.Contains(string(b), "Quiet") {
		t.Fatal("a stub with an unchanged version must not be fetched by a plain pull")
	}
	pull(t, env, PullOpts{Full: true})
	if b, _ := env.Tree.ReadFile("a/one.xml"); !strings.Contains(string(b), "Quiet") {
		t.Fatalf("pull --full must fetch every stub:\n%s", b)
	}
}

func TestCloneSkipsResourceGoneAfterListing(t *testing.T) {
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.Put("2", "a/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.Stubs = true
	ad.Remote.Missing = map[string]bool{"2": true}
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Tree.Exists("a/one.xml") || env.Tree.Exists("a/two.xml") {
		t.Fatal("the vanished resource must be skipped, the rest cloned")
	}
}

func TestPullTreatsResourceGoneAfterListingAsDeleted(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Stubs = true
	ad.Remote.Edit("2", setTitle("Two v2"))
	ad.Remote.Missing = map[string]bool{"2": true}
	r := pull(t, env, PullOpts{})
	if r.Deleted != 1 || env.Tree.Exists("a/b/two.xml") {
		t.Fatalf("%+v", r)
	}
}
