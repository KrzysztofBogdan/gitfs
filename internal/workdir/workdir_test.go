package workdir

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "imap://me@example.com", Scheme: "imap", Account: "me@example.com"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	l := Layout{Root: dir}
	for _, p := range []string{l.Gitfs(), l.Shadow(), l.Log(), l.Trash(), l.Config()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	b, err := os.ReadFile(l.Config())
	if err != nil {
		t.Fatal(err)
	}
	if want := "[remote]\nurl = \"imap://me@example.com\"\n"; string(b) != want {
		t.Errorf("config.toml = %q; want %q", b, want)
	}
	// HEAD should not exist yet.
	if _, err := os.Stat(l.Head()); !os.IsNotExist(err) {
		t.Errorf("HEAD should not exist after Init, got err=%v", err)
	}
}

func TestEmitter(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "jira://acme/PROJ", Scheme: "jira", Account: "acme", Path: "PROJ"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(dir)
	if err := e.File("PROJ-1/desc.md", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// working tree
	b, err := os.ReadFile(filepath.Join(dir, "PROJ-1", "desc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("working tree content = %q", b)
	}
	// shadow
	b, err = os.ReadFile(filepath.Join(dir, ".gitfs", "shadow", "PROJ-1", "desc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("shadow content = %q", b)
	}
	if e.Count() != 1 || len(e.Written()) != 1 {
		t.Errorf("count/written inconsistent: %d / %v", e.Count(), e.Written())
	}
}

func TestEmitterRejectsEscapes(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "x://a"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(dir)
	bad := []string{
		"",
		"/abs/path",
		"..",
		"../oops.md",
		".gitfs/evil",
		"a/../../b",
	}
	for _, p := range bad {
		if err := e.File(p, []byte("x")); err == nil {
			t.Errorf("expected error for path %q", p)
		}
	}
}

func TestEmitterFileAtSetsMtime(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "imap://me@example.com"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(dir)
	want := time.Date(2023, 6, 15, 10, 30, 45, 0, time.UTC)
	if err := e.FileAt("inbox/msg.md", []byte("hi"), want); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "inbox", "msg.md")
	shadow := filepath.Join(dir, ".gitfs", "shadow", "inbox", "msg.md")
	for _, p := range []string{work, shadow} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !st.ModTime().Equal(want) {
			t.Errorf("%s mtime = %v; want %v", p, st.ModTime(), want)
		}
	}
}

func TestEmitterFileAtZeroTimeLeavesMtime(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "imap://me@example.com"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(dir)
	before := time.Now()
	if err := e.FileAt("inbox/msg.md", []byte("hi"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	work := filepath.Join(dir, "inbox", "msg.md")
	st, err := os.Stat(work)
	if err != nil {
		t.Fatal(err)
	}
	m := st.ModTime()
	if m.Before(before.Add(-time.Second)) || m.After(after.Add(time.Second)) {
		t.Errorf("mtime %v not within [%v, %v] — should be write time", m, before, after)
	}
}

func TestFindRootWalksUp(t *testing.T) {
	root := t.TempDir()
	u := adapter.URL{Raw: "x://a"}
	if err := Init(root, u); err != nil {
		t.Fatal(err)
	}
	// Run from a deep subdirectory of root.
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindRoot(deep)
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	// Both should resolve to the same canonical path.
	wantAbs, _ := filepath.EvalSymlinks(root)
	gotAbs, _ := filepath.EvalSymlinks(got)
	if gotAbs != wantAbs {
		t.Errorf("FindRoot = %q, want %q", got, root)
	}
}

func TestFindRootOnRootItself(t *testing.T) {
	root := t.TempDir()
	u := adapter.URL{Raw: "x://a"}
	if err := Init(root, u); err != nil {
		t.Fatal(err)
	}
	got, err := FindRoot(root)
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	wantAbs, _ := filepath.EvalSymlinks(root)
	gotAbs, _ := filepath.EvalSymlinks(got)
	if gotAbs != wantAbs {
		t.Errorf("FindRoot = %q, want %q", got, root)
	}
}

func TestFindRootMissing(t *testing.T) {
	// A fresh temp dir with no .gitfs/ anywhere up the tree (until the
	// filesystem root) should error.
	_, err := FindRoot(t.TempDir())
	if err == nil {
		t.Fatal("expected error when no .gitfs/ found")
	}
}

func TestLockExclusive(t *testing.T) {
	root := t.TempDir()
	u := adapter.URL{Raw: "x://a"}
	if err := Init(root, u); err != nil {
		t.Fatal(err)
	}
	l1, err := AcquireLock(root, 0)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer l1.Release()
	// Second attempt with zero timeout must fail immediately.
	l2, err := AcquireLock(root, 0)
	if err == nil {
		l2.Release()
		t.Fatal("expected second lock to fail")
	}
	// Release first, second should now succeed.
	if err := l1.Release(); err != nil {
		t.Fatal(err)
	}
	l3, err := AcquireLock(root, 0)
	if err != nil {
		t.Fatalf("third lock after release: %v", err)
	}
	l3.Release()
}

func TestEmitterRemoveWritten(t *testing.T) {
	dir := t.TempDir()
	u := adapter.URL{Raw: "x://a"}
	if err := Init(dir, u); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(dir)
	_ = e.File("a/b/c.md", []byte("x"))
	_ = e.File("a/b/d.md", []byte("y"))
	e.RemoveWritten()
	// Files gone
	for _, p := range []string{
		filepath.Join(dir, "a", "b", "c.md"),
		filepath.Join(dir, "a", "b", "d.md"),
		filepath.Join(dir, ".gitfs", "shadow", "a", "b", "c.md"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}
}
