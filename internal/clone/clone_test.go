package clone

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/uerr"
)

// --- fakes ---------------------------------------------------------------

type fakeAdapter struct {
	info     adapter.Info
	authBlob adapter.Credentials
	authErr  error
	fetchErr error
	files    map[string][]byte
	head     []byte
	gotCreds adapter.Credentials
}

func (f *fakeAdapter) Info() adapter.Info { return f.info }

func (f *fakeAdapter) Authenticate(
	_ context.Context, prior adapter.Credentials, _ adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	if prior != nil {
		return prior, false, nil
	}
	return f.authBlob, true, f.authErr
}

func (f *fakeAdapter) Fetch(
	_ context.Context, creds adapter.Credentials, emit adapter.Emitter,
) ([]byte, error) {
	f.gotCreds = creds
	if f.fetchErr != nil {
		// Emit partial content to exercise rollback.
		for p, c := range f.files {
			_ = emit.File(p, c)
			break
		}
		return nil, f.fetchErr
	}
	for p, c := range f.files {
		if err := emit.File(p, c); err != nil {
			return nil, err
		}
	}
	return f.head, nil
}

func (f *fakeAdapter) Pull(
	_ context.Context, _ adapter.Credentials, _ []byte, _ adapter.PullEmitter,
) ([]byte, error) {
	return nil, errors.New("fakeAdapter.Pull: not used by clone tests")
}

func (f *fakeAdapter) Commit(
	_ context.Context, _ adapter.Credentials, _ []adapter.CommitRequest, _ adapter.CommitEmitter, _ adapter.IO,
) error {
	return errors.New("fakeAdapter.Commit: not used by clone tests")
}

type nullIO struct{}

func (nullIO) Stdout() io.Writer                 { return io.Discard }
func (nullIO) Stderr() io.Writer                 { return io.Discard }
func (nullIO) IsTTY() bool                       { return false }
func (nullIO) ReadLine(string) (string, error)   { return "", errors.New("no tty") }
func (nullIO) ReadSecret(string) (string, error) { return "", errors.New("no tty") }

type memStore struct {
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }
func (m *memStore) Get(k string) ([]byte, bool, error) {
	b, ok := m.data[k]
	return b, ok, nil
}
func (m *memStore) Put(k string, v []byte) error { m.data[k] = v; return nil }
func (m *memStore) Delete(k string) error        { delete(m.data, k); return nil }

// --- tests ---------------------------------------------------------------

func registerFake(t *testing.T, ad *fakeAdapter) (url string, cleanup func()) {
	t.Helper()
	adapter.Register("faketest", func(adapter.URL) (adapter.Adapter, error) {
		return ad, nil
	})
	return "faketest://me", func() {
		// Registry has no unregister; overwriting with noop factory
		// is fine for tests — each case creates its own adapter value.
	}
}

func TestRunHappyPath(t *testing.T) {
	ad := &fakeAdapter{
		info:     adapter.Info{Scheme: "faketest", AuthStrategy: adapter.AuthTokenPaste},
		authBlob: []byte(`{"type":"x"}`),
		files: map[string][]byte{
			"a/b.md": []byte("hello"),
			"c.md":   []byte("world"),
		},
		head: []byte("HEAD-TOKEN"),
	}
	u, cleanup := registerFake(t, ad)
	defer cleanup()

	dir := filepath.Join(t.TempDir(), "clone-dir")
	var out bytes.Buffer
	err := Run(context.Background(), Args{URL: u, Dir: dir}, Deps{
		Store:  newMemStore(),
		IO:     nullIO{},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	for _, p := range []string{"a/b.md", "c.md",
		".gitfs/shadow/a/b.md", ".gitfs/shadow/c.md", ".gitfs/HEAD"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	head, _ := os.ReadFile(filepath.Join(dir, ".gitfs", "HEAD"))
	if string(head) != "HEAD-TOKEN" {
		t.Errorf("HEAD = %q", head)
	}
	if !strings.Contains(out.String(), "(2 files)") {
		t.Errorf("summary missing file count, got: %q", out.String())
	}
}

func TestRunRollbackCreatedDir(t *testing.T) {
	ad := &fakeAdapter{
		info:     adapter.Info{Scheme: "faketest", AuthStrategy: adapter.AuthTokenPaste},
		authBlob: []byte(`{}`),
		files:    map[string][]byte{"a.md": []byte("x")},
		fetchErr: errors.New("boom"),
	}
	u, cleanup := registerFake(t, ad)
	defer cleanup()

	dir := filepath.Join(t.TempDir(), "new-dir")
	err := Run(context.Background(), Args{URL: u, Dir: dir}, Deps{
		Store: newMemStore(), IO: nullIO{}, Stdout: io.Discard,
	})
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be removed on rollback, got err=%v", err)
	}
}

func TestRunRollbackReusedEmptyDir(t *testing.T) {
	ad := &fakeAdapter{
		info:     adapter.Info{Scheme: "faketest", AuthStrategy: adapter.AuthTokenPaste},
		authBlob: []byte(`{}`),
		files:    map[string][]byte{"a/deep/file.md": []byte("x")},
		fetchErr: errors.New("boom"),
	}
	u, cleanup := registerFake(t, ad)
	defer cleanup()

	dir := t.TempDir() // pre-existing empty
	err := Run(context.Background(), Args{URL: u, Dir: dir}, Deps{
		Store: newMemStore(), IO: nullIO{}, Stdout: io.Discard,
	})
	if err == nil {
		t.Fatal("expected fetch error")
	}
	// Dir retained
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir should be retained: %v", err)
	}
	// .gitfs gone
	if _, err := os.Stat(filepath.Join(dir, ".gitfs")); !os.IsNotExist(err) {
		t.Errorf(".gitfs should be removed, got err=%v", err)
	}
	// Emitted file gone
	if _, err := os.Stat(filepath.Join(dir, "a", "deep", "file.md")); !os.IsNotExist(err) {
		t.Errorf("emitted file should be removed, got err=%v", err)
	}
}

func TestRunUnknownScheme(t *testing.T) {
	err := Run(context.Background(),
		Args{URL: "nosuch-scheme-xyz://foo", Dir: t.TempDir()},
		Deps{Store: newMemStore(), IO: nullIO{}, Stdout: io.Discard},
	)
	if err == nil {
		t.Fatal("expected UserError")
	}
	if !uerr.Is(err) {
		t.Errorf("expected *uerr.UserError, got %T: %v", err, err)
	}
}

func TestRunNonEmptyDir(t *testing.T) {
	ad := &fakeAdapter{
		info:     adapter.Info{Scheme: "faketest", AuthStrategy: adapter.AuthTokenPaste},
		authBlob: []byte(`{}`),
	}
	u, cleanup := registerFake(t, ad)
	defer cleanup()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "preexisting"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Args{URL: u, Dir: dir}, Deps{
		Store: newMemStore(), IO: nullIO{}, Stdout: io.Discard,
	})
	if !uerr.Is(err) {
		t.Fatalf("expected UserError, got %T: %v", err, err)
	}
	if ad.gotCreds != nil {
		t.Error("adapter Fetch called before dir check")
	}
}

func TestRunBadURL(t *testing.T) {
	err := Run(context.Background(),
		Args{URL: "not a url", Dir: t.TempDir()},
		Deps{Store: newMemStore(), IO: nullIO{}, Stdout: io.Discard},
	)
	if !uerr.Is(err) {
		t.Errorf("expected UserError, got %T: %v", err, err)
	}
}
