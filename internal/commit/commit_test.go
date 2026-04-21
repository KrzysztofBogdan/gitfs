package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/aurl"
	"github.com/KrzysztofBogdan/gitfs/internal/uerr"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// fakeCommitter is a minimal adapter used to drive commit.Run tests.
// For each request whose path is a key in accept, the fake calls
// emit.Accept with the mapped results; for each path in reject, it
// calls emit.Reject. Paths missing from both are auto-accepted as a
// single same-path same-content result (echo).
type fakeCommitter struct {
	accept      map[string][]adapter.CommitResult
	reject      map[string]string
	commitErr   error
	gotRequests []adapter.CommitRequest
}

func (f *fakeCommitter) Info() adapter.Info { return adapter.Info{Scheme: "faketest"} }

func (f *fakeCommitter) Authenticate(
	_ context.Context, prior adapter.Credentials, _ adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	return prior, false, nil
}

func (f *fakeCommitter) Fetch(
	_ context.Context, _ adapter.Credentials, _ adapter.Emitter,
) ([]byte, error) {
	return nil, nil
}

func (f *fakeCommitter) Pull(
	_ context.Context, _ adapter.Credentials, _ []byte, _ adapter.PullEmitter,
) ([]byte, error) {
	return nil, nil
}

func (f *fakeCommitter) Commit(
	_ context.Context, _ adapter.Credentials,
	reqs []adapter.CommitRequest, emit adapter.CommitEmitter, _ adapter.IO,
) error {
	f.gotRequests = append(f.gotRequests, reqs...)
	if f.commitErr != nil {
		return f.commitErr
	}
	for _, r := range reqs {
		if reason, ok := f.reject[r.Path]; ok {
			if err := emit.Reject(r.Path, reason); err != nil {
				return err
			}
			continue
		}
		results, ok := f.accept[r.Path]
		if !ok {
			// Default: echo input content at same path, or deletion.
			if r.Kind == adapter.CommitDeletion {
				results = nil
			} else {
				results = []adapter.CommitResult{{Path: r.Path, Content: r.Content}}
			}
		}
		if err := emit.Accept(r.Path, results); err != nil {
			return err
		}
	}
	return nil
}

func mkWorkdir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	u, err := aurl.Parse("faketest://me")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := workdir.Init(root, u); err != nil {
		t.Fatalf("init: %v", err)
	}
	return root
}

func writeTree(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeShadow(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	shadow := filepath.Join(
		workdir.Layout{Root: root}.Shadow(),
		filepath.FromSlash(rel),
	)
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func fixedNow() func() time.Time {
	t := time.Date(2026, 4, 19, 12, 34, 56, 0, time.UTC)
	return func() time.Time { return t }
}

func run(t *testing.T, root string, c *fakeCommitter, args Args) (string, error) {
	t.Helper()
	if args.Dir == "" {
		args.Dir = root
	}
	var out bytes.Buffer
	err := Run(context.Background(), args, Deps{
		Adapter: c,
		Stdout:  &out,
		Now:     fixedNow(),
	})
	return out.String(), err
}

// --- tests ---------------------------------------------------------------

func TestCommitSendsDirtyFile(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{
		accept: map[string][]adapter.CommitResult{
			"x.md": {{Path: "x.md", Content: []byte("canonical")}},
		},
	}

	out, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Adapter saw the request with pre-rewrite tree content.
	if len(c.gotRequests) != 1 {
		t.Fatalf("requests = %d, want 1", len(c.gotRequests))
	}
	got := c.gotRequests[0]
	if got.Path != "x.md" || got.Kind != adapter.CommitContent ||
		!bytes.Equal(got.Content, []byte("edited")) {
		t.Errorf("request = %+v", got)
	}

	// Tree and shadow rewritten to canonical.
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("canonical")) {
		t.Errorf("tree = %q", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("canonical")) {
		t.Errorf("shadow = %q", gotShadow)
	}

	// HEAD NEVER touched.
	if _, err := os.Stat(workdir.Layout{Root: root}.Head()); !os.IsNotExist(err) {
		t.Errorf("HEAD must not be written by commit, err=%v", err)
	}

	// Log file written: exactly one per invocation.
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 1 {
		t.Fatalf("log files = %d, want 1", len(logs))
	}

	// Trash snapshot contains the pre-commit tree content.
	trashes, _ := os.ReadDir(workdir.Layout{Root: root}.Trash())
	if len(trashes) != 1 {
		t.Fatalf("trash dirs = %d, want 1", len(trashes))
	}
	snap := filepath.Join(
		workdir.Layout{Root: root}.Trash(), trashes[0].Name(), "x.md",
	)
	if got := readFile(t, snap); !bytes.Equal(got, []byte("edited")) {
		t.Errorf("snapshot = %q", got)
	}

	// Output mentions "sent" for the path.
	if !strings.Contains(out, "sent:") || !strings.Contains(out, "x.md") {
		t.Errorf("summary missing 'sent:' for x.md, got: %q", out)
	}
}

func TestCommitNoPathsSendableIsSilent(t *testing.T) {
	root := mkWorkdir(t)
	// Clean state: no dirty paths anywhere.
	writeTree(t, root, "x.md", []byte("same"))
	writeShadow(t, root, "x.md", []byte("same"))

	c := &fakeCommitter{}
	out, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Empty output, exit 0.
	if out != "" {
		t.Errorf("expected silent no-op, got: %q", out)
	}
	// No log written.
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 0 {
		t.Errorf("log files = %d, want 0", len(logs))
	}
	// No trash dir created either.
	trashes, _ := os.ReadDir(workdir.Layout{Root: root}.Trash())
	if len(trashes) != 0 {
		t.Errorf("trash dirs = %d, want 0", len(trashes))
	}
	// Adapter was never asked.
	if len(c.gotRequests) != 0 {
		t.Errorf("adapter called with %d requests, want 0", len(c.gotRequests))
	}
}

func TestCommitNewFileSent(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "new.md", []byte("hello"))
	// No shadow → "new" commit target.

	c := &fakeCommitter{}
	out, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(c.gotRequests) != 1 || c.gotRequests[0].Path != "new.md" ||
		c.gotRequests[0].Kind != adapter.CommitContent {
		t.Fatalf("requests = %+v", c.gotRequests)
	}
	// Echo acceptance → shadow now matches tree.
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "new.md",
	))
	if !bytes.Equal(gotShadow, []byte("hello")) {
		t.Errorf("shadow = %q", gotShadow)
	}
	if !strings.Contains(out, "sent:") || !strings.Contains(out, "new.md") {
		t.Errorf("summary = %q", out)
	}
}

func TestCommitLocallyDeletedSent(t *testing.T) {
	root := mkWorkdir(t)
	// Shadow present, tree missing → locally deleted.
	writeShadow(t, root, "gone.md", []byte("original"))

	c := &fakeCommitter{}
	out, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(c.gotRequests) != 1 || c.gotRequests[0].Path != "gone.md" ||
		c.gotRequests[0].Kind != adapter.CommitDeletion {
		t.Fatalf("requests = %+v", c.gotRequests)
	}
	// Shadow removed after acceptance.
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "gone.md",
	)); !os.IsNotExist(err) {
		t.Errorf("shadow should be removed, err=%v", err)
	}
	// Trash snapshot contains the shadow content (tree was missing).
	trashes, _ := os.ReadDir(workdir.Layout{Root: root}.Trash())
	if len(trashes) != 1 {
		t.Fatalf("trash dirs = %d", len(trashes))
	}
	snap := filepath.Join(
		workdir.Layout{Root: root}.Trash(), trashes[0].Name(), "gone.md",
	)
	if got := readFile(t, snap); !bytes.Equal(got, []byte("original")) {
		t.Errorf("snapshot = %q", got)
	}
	if !strings.Contains(out, "sent:") || !strings.Contains(out, "gone.md") {
		t.Errorf("summary = %q", out)
	}
}

func TestCommitCleanPathSkippedWhenExplicit(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "same.md", []byte("same"))
	writeShadow(t, root, "same.md", []byte("same"))

	c := &fakeCommitter{}
	out, err := run(t, root, c, Args{Paths: []string{"same.md"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Adapter is NOT called for clean paths.
	if len(c.gotRequests) != 0 {
		t.Errorf("adapter called on clean path: %+v", c.gotRequests)
	}
	// Summary notes "no change".
	if !strings.Contains(out, "no change:") || !strings.Contains(out, "same.md") {
		t.Errorf("summary missing 'no change:' for same.md, got: %q", out)
	}
	// No log (no successes).
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 0 {
		t.Errorf("log files = %d, want 0", len(logs))
	}
}

func TestCommitNonexistentPathExitsTwo(t *testing.T) {
	root := mkWorkdir(t)
	c := &fakeCommitter{}

	out, err := run(t, root, c, Args{Paths: []string{"ghost.md"}})
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	if !uerr.Is(err) {
		t.Errorf("expected *uerr.UserError, got %T: %v", err, err)
	}
	if !strings.Contains(out, "invalid:") || !strings.Contains(out, "ghost.md") {
		t.Errorf("summary missing 'invalid:' for ghost.md, got: %q", out)
	}
}

func TestCommitRejectionKeepsDirtyAndExitsOne(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{
		reject: map[string]string{"x.md": "boom"},
	}
	out, err := run(t, root, c, Args{})
	if err == nil {
		t.Fatal("expected error")
	}
	if uerr.Is(err) {
		t.Errorf("rejection must not be a user error (exit 1 not 2): %v", err)
	}
	// Tree + shadow untouched.
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("edited")) {
		t.Errorf("tree = %q", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("orig")) {
		t.Errorf("shadow = %q", gotShadow)
	}
	// No log (no successes).
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 0 {
		t.Errorf("log files = %d, want 0", len(logs))
	}
	// Summary carries the rejection reason.
	if !strings.Contains(out, "rejected:") ||
		!strings.Contains(out, "x.md") ||
		!strings.Contains(out, "boom") {
		t.Errorf("summary = %q", out)
	}
}

func TestCommitRejectionWinsOverInvalid(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{reject: map[string]string{"x.md": "nope"}}
	_, err := run(t, root, c, Args{Paths: []string{"x.md", "ghost.md"}})
	if err == nil {
		t.Fatal("expected error")
	}
	// 1 beats 2.
	if uerr.Is(err) {
		t.Errorf("rejection+invalid must exit 1 not 2, got user error: %v", err)
	}
}

func TestCommitPartialSuccess(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "a.md", []byte("a-new"))
	writeShadow(t, root, "a.md", []byte("a-old"))
	writeTree(t, root, "b.md", []byte("b-new"))
	writeShadow(t, root, "b.md", []byte("b-old"))

	c := &fakeCommitter{reject: map[string]string{"b.md": "busy"}}
	out, err := run(t, root, c, Args{})
	if err == nil {
		t.Fatal("expected error (one rejection)")
	}
	// a.md applied.
	if got := readFile(t, filepath.Join(root, "a.md")); !bytes.Equal(got, []byte("a-new")) {
		t.Errorf("a.md tree = %q, want a-new", got)
	}
	gotShadowA := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "a.md",
	))
	if !bytes.Equal(gotShadowA, []byte("a-new")) {
		t.Errorf("a.md shadow = %q, want a-new", gotShadowA)
	}
	// b.md untouched.
	gotShadowB := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "b.md",
	))
	if !bytes.Equal(gotShadowB, []byte("b-old")) {
		t.Errorf("b.md shadow = %q, want b-old", gotShadowB)
	}
	// Log file exists (at least one success).
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 1 {
		t.Fatalf("log files = %d, want 1", len(logs))
	}
	// Log only lists a.md.
	body, _ := os.ReadFile(filepath.Join(
		workdir.Layout{Root: root}.Log(), logs[0].Name(),
	))
	var log logEntry
	if err := json.Unmarshal(body, &log); err != nil {
		t.Fatalf("log parse: %v", err)
	}
	paths := make([]string, 0, len(log.Entries))
	for _, e := range log.Entries {
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	if len(paths) != 1 || paths[0] != "a.md" {
		t.Errorf("log paths = %v, want [a.md]", paths)
	}
	// Output shows both lines.
	if !strings.Contains(out, "sent:") || !strings.Contains(out, "a.md") {
		t.Errorf("summary missing sent a.md: %q", out)
	}
	if !strings.Contains(out, "rejected:") || !strings.Contains(out, "b.md") {
		t.Errorf("summary missing rejection b.md: %q", out)
	}
}

func TestCommitCanonicalRenameRemovesOriginal(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "drafts/reply.eml", []byte("draft content"))
	// No shadow → new commit-target.

	c := &fakeCommitter{
		accept: map[string][]adapter.CommitResult{
			"drafts/reply.eml": {
				{Path: "sent/2026/reply.eml", Content: []byte("final content")},
			},
		},
	}
	_, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Original path gone from tree + shadow.
	if _, err := os.Stat(filepath.Join(root, "drafts/reply.eml")); !os.IsNotExist(err) {
		t.Errorf("original tree path should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "drafts/reply.eml",
	)); !os.IsNotExist(err) {
		t.Errorf("original shadow path should be removed, err=%v", err)
	}
	// New path written to tree + shadow.
	if got := readFile(t, filepath.Join(root, "sent/2026/reply.eml")); !bytes.Equal(got, []byte("final content")) {
		t.Errorf("new tree = %q", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "sent/2026/reply.eml",
	))
	if !bytes.Equal(gotShadow, []byte("final content")) {
		t.Errorf("new shadow = %q", gotShadow)
	}
}

func TestCommitDeletionResultRemovesPath(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{
		// Adapter accepts the dirty send as a server-side deletion.
		accept: map[string][]adapter.CommitResult{
			"x.md": {{Path: "x.md", Delete: true}},
		},
	}
	_, err := run(t, root, c, Args{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.md")); !os.IsNotExist(err) {
		t.Errorf("tree should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	)); !os.IsNotExist(err) {
		t.Errorf("shadow should be removed, err=%v", err)
	}
}

func TestCommitMessagePropagated(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{}
	_, err := run(t, root, c, Args{Message: "hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.gotRequests) != 1 || c.gotRequests[0].Message != "hello" {
		t.Errorf("request message = %+v", c.gotRequests)
	}
}

func TestCommitOutsideWorkdir(t *testing.T) {
	root := t.TempDir() // no .gitfs
	err := Run(context.Background(), Args{Dir: root}, Deps{
		Adapter: &fakeCommitter{},
		Stdout:  io.Discard,
		Now:     fixedNow(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !uerr.Is(err) {
		t.Errorf("expected *uerr.UserError, got %T: %v", err, err)
	}
}

// memStore is an in-memory creds.Store used to check UpdateCredentials
// persistence behaviour.
type memStore struct {
	entries map[string][]byte
}

func newMemStore() *memStore { return &memStore{entries: map[string][]byte{}} }

func (m *memStore) Get(key string) ([]byte, bool, error) {
	b, ok := m.entries[key]
	return b, ok, nil
}

func (m *memStore) Put(key string, blob []byte) error {
	m.entries[key] = append([]byte(nil), blob...)
	return nil
}

func (m *memStore) Delete(key string) error {
	delete(m.entries, key)
	return nil
}

// updaterCommitter accepts every request and calls emit.UpdateCredentials
// with the canned updated blob.
type updaterCommitter struct {
	fakeCommitter
	updated []byte
}

func (u *updaterCommitter) Info() adapter.Info {
	return adapter.Info{Scheme: "faketest", AuthStrategy: adapter.AuthTokenPaste}
}

func (u *updaterCommitter) Commit(
	ctx context.Context, creds adapter.Credentials,
	reqs []adapter.CommitRequest, emit adapter.CommitEmitter, io adapter.IO,
) error {
	if err := u.fakeCommitter.Commit(ctx, creds, reqs, emit, io); err != nil {
		return err
	}
	if u.updated != nil {
		return emit.UpdateCredentials(u.updated)
	}
	return nil
}

func TestCommitPersistsUpdatedCredentialsFromKeyring(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	store := newMemStore()
	// faketest's AuthTokenPaste strategy triggers the keyring lookup;
	// prior blob "v1" becomes the credential. The fake then asks the
	// core to persist "v2".
	store.Put("faketest:me", []byte(`{"v":1}`))

	u := &updaterCommitter{updated: []byte(`{"v":2}`)}
	// Inject a URL by swapping through Deps.Adapter + a real config.
	// mkWorkdir already wrote a faketest://me config.
	var out bytes.Buffer
	err := Run(context.Background(), Args{Dir: root}, Deps{
		Adapter: u,
		Store:   store,
		IO:      &nullIO{},
		Stdout:  &out,
		Now:     fixedNow(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(store.entries["faketest:me"]) != `{"v":2}` {
		t.Errorf("keyring not updated; got %q", store.entries["faketest:me"])
	}
}

// nullIO is a dev-null IO used by tests that do not exercise prompts.
type nullIO struct{}

func (nullIO) Stdout() io.Writer                 { return io.Discard }
func (nullIO) Stderr() io.Writer                 { return io.Discard }
func (nullIO) IsTTY() bool                       { return false }
func (nullIO) ReadLine(string) (string, error)   { return "", errors.New("no tty") }
func (nullIO) ReadSecret(string) (string, error) { return "", errors.New("no tty") }

func TestCommitDoesNotPersistUpdatedCredentialsFromEnv(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	store := newMemStore()
	// No prior entry — resolveCredentials falls through to Authenticate.
	// The updater adapter's fakeCommitter.Authenticate returns Persist=false,
	// so the blob is env-like; UpdateCredentials must not be persisted.

	u := &updaterCommitter{updated: []byte(`{"v":2}`)}
	var out bytes.Buffer
	err := Run(context.Background(), Args{Dir: root}, Deps{
		Adapter: u,
		Store:   store,
		IO:      &nullIO{},
		Stdout:  &out,
		Now:     fixedNow(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := store.entries["faketest:me"]; ok {
		t.Errorf("env-derived creds should not be persisted on UpdateCredentials; got %q",
			store.entries["faketest:me"])
	}
}

func TestCommitAdapterErrorAbortsWithoutWrites(t *testing.T) {
	root := mkWorkdir(t)
	writeTree(t, root, "x.md", []byte("edited"))
	writeShadow(t, root, "x.md", []byte("orig"))

	c := &fakeCommitter{commitErr: errors.New("network down")}
	_, err := run(t, root, c, Args{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Tree + shadow untouched.
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("edited")) {
		t.Errorf("tree = %q", got)
	}
	// No log, no trash.
	logs, _ := os.ReadDir(workdir.Layout{Root: root}.Log())
	if len(logs) != 0 {
		t.Errorf("log files = %d, want 0", len(logs))
	}
}
