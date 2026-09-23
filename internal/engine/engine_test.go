package engine

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

var ctx = context.Background()

// cloned returns an Env cloned from a fake remote holding two notes.
func cloned(t *testing.T) (*Env, *fake.Adapter, *bytes.Buffer) {
	t.Helper()
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title><comment id="c1" created="2026-01-01">hi</comment></note>`)
	ad.Remote.Put("2", "a/b/two.xml", `<note><title>Two</title><body type="text/plain">line1
line2
line3
line4
line5</body></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out)
	if err != nil {
		t.Fatal(err)
	}
	env.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	out.Reset()
	return env, ad, &out
}

func status(t *testing.T, env *Env) []changes.FileChange {
	t.Helper()
	cs, err := changes.Compute(env.Tree, env.Index, env.Adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func read(t *testing.T, env *Env, p string) string {
	t.Helper()
	b, err := env.Tree.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, env *Env, p, s string) {
	t.Helper()
	if err := env.Tree.WriteFile(p, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

// edit replaces old with new in a working file.
func edit(t *testing.T, env *Env, p, old, new string) {
	t.Helper()
	s := read(t, env, p)
	if !strings.Contains(s, old) {
		t.Fatalf("%s does not contain %q:\n%s", p, old, s)
	}
	write(t, env, p, strings.Replace(s, old, new, 1))
}

func TestClone(t *testing.T) {
	env, _, _ := cloned(t)
	one := read(t, env, "a/one.xml")
	want := `<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <note id="1" version="1">
      <title>One</title>
      <comment id="c1" created="2026-01-01">hi</comment>
    </note>
  </content>
</gfs>
`
	if one != want {
		t.Fatalf("got\n%s", one)
	}
	if e, ok := env.Index.ByID("2"); !ok || e.Path != "a/b/two.xml" || e.Version != "1" {
		t.Fatalf("index: %+v", e)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("clean clone must have no changes: %+v", cs)
	}
}
