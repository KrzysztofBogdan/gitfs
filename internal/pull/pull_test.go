package pull

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/aurl"
	"github.com/KrzysztofBogdan/gitfs/internal/uerr"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// fakePuller is a minimal adapter + Puller used to drive pull.Run tests.
type fakePuller struct {
	files      map[string][]byte
	tombstones []string
	newHead    []byte
	gotHead    []byte
	pullErr    error
}

func (f *fakePuller) Info() adapter.Info { return adapter.Info{Scheme: "faketest"} }

func (f *fakePuller) Authenticate(
	_ context.Context, prior adapter.Credentials, _ adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	return prior, false, nil
}

func (f *fakePuller) Fetch(
	_ context.Context, _ adapter.Credentials, _ adapter.Emitter,
) ([]byte, error) {
	return nil, nil
}

func (f *fakePuller) Pull(
	_ context.Context, _ adapter.Credentials, head []byte, emit adapter.PullEmitter,
) ([]byte, error) {
	f.gotHead = append([]byte(nil), head...)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	for p, c := range f.files {
		if err := emit.File(p, c); err != nil {
			return nil, err
		}
	}
	for _, p := range f.tombstones {
		if err := emit.Tombstone(p); err != nil {
			return nil, err
		}
	}
	return f.newHead, nil
}

func (f *fakePuller) Commit(
	_ context.Context, _ adapter.Credentials, _ []adapter.CommitRequest, _ adapter.CommitEmitter, _ adapter.IO,
) error {
	return nil
}

type fakeFullRefreshPuller struct {
	files   map[string][]byte
	newHead []byte
}

func (f *fakeFullRefreshPuller) Info() adapter.Info { return adapter.Info{Scheme: "faketest"} }

func (f *fakeFullRefreshPuller) Authenticate(
	_ context.Context, prior adapter.Credentials, _ adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	return prior, false, nil
}

func (f *fakeFullRefreshPuller) Fetch(
	_ context.Context, _ adapter.Credentials, emit adapter.Emitter,
) ([]byte, error) {
	for p, c := range f.files {
		if err := emit.File(p, c); err != nil {
			return nil, err
		}
	}
	return f.newHead, nil
}

func (f *fakeFullRefreshPuller) Pull(
	ctx context.Context, creds adapter.Credentials, _ []byte, emit adapter.PullEmitter,
) ([]byte, error) {
	return adapter.FullRefreshPull(ctx, f.Fetch, creds, emit)
}

func (f *fakeFullRefreshPuller) Commit(
	_ context.Context, _ adapter.Credentials, _ []adapter.CommitRequest, _ adapter.CommitEmitter, _ adapter.IO,
) error {
	return nil
}

// mkWorkdir creates an initialized workdir at a fresh temp dir and returns
// its root. Optionally writes an initial HEAD.
func mkWorkdir(t *testing.T, head []byte) string {
	t.Helper()
	root := t.TempDir()
	u, err := aurl.Parse("faketest://me")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := workdir.Init(root, u); err != nil {
		t.Fatalf("init: %v", err)
	}
	if head != nil {
		if err := workdir.WriteHEAD(root, head); err != nil {
			t.Fatalf("WriteHEAD: %v", err)
		}
	}
	return root
}

// writeTree writes a file under root at the given relative path.
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

// writeShadow writes a shadow entry for a path.
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

// run executes pull.Run against a temp workdir, using the given adapter.
func run(t *testing.T, root string, ad adapter.Adapter) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), Args{Dir: root}, Deps{
		Adapter: ad,
		Stdout:  &out,
	})
	return out.String(), err
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// --- tests ---------------------------------------------------------------

func TestPullAppliesNewFileAndAdvancesHEAD(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	p := &fakePuller{
		files:   map[string][]byte{"a/b.md": []byte("hello")},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Tree and shadow both written.
	got := readFile(t, filepath.Join(root, "a/b.md"))
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("tree = %q", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "a/b.md",
	))
	if !bytes.Equal(gotShadow, []byte("hello")) {
		t.Errorf("shadow = %q", gotShadow)
	}
	// HEAD advanced.
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1", head)
	}
	// Adapter saw prior HEAD.
	if !bytes.Equal(p.gotHead, []byte("HEAD-0")) {
		t.Errorf("adapter saw head %q, want HEAD-0", p.gotHead)
	}
	// Output mentions the path as updated.
	if !strings.Contains(out, "updated:") || !strings.Contains(out, "a/b.md") {
		t.Errorf("summary missing updated line, got: %q", out)
	}
	// No held-HEAD line.
	if strings.Contains(out, "HEAD held at") {
		t.Errorf("unexpected held-HEAD line: %q", out)
	}
}

func TestPullSkipsDirtyFileAndHoldsHEAD(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	// Clean state: shadow and tree both contain the original.
	writeTree(t, root, "x.md", []byte("orig"))
	writeShadow(t, root, "x.md", []byte("orig"))
	// User edited the tree → dirty.
	writeTree(t, root, "x.md", []byte("local-edit"))

	p := &fakePuller{
		files:   map[string][]byte{"x.md": []byte("remote-new")},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Tree untouched.
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("local-edit")) {
		t.Errorf("tree = %q, want local-edit", got)
	}
	// Shadow untouched.
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("orig")) {
		t.Errorf("shadow = %q, want orig", gotShadow)
	}
	// HEAD held at HEAD-0.
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q, want HEAD-0", head)
	}
	if !strings.Contains(out, "skipped (local changes):") {
		t.Errorf("summary missing skip line, got: %q", out)
	}
	if !strings.Contains(out, "HEAD held at") {
		t.Errorf("summary missing HEAD-held line, got: %q", out)
	}
}

func TestPullOverwritesCleanFile(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "x.md", []byte("orig"))
	writeShadow(t, root, "x.md", []byte("orig"))

	p := &fakePuller{
		files:   map[string][]byte{"x.md": []byte("remote-new")},
		newHead: []byte("HEAD-1"),
	}

	if _, err := run(t, root, p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("remote-new")) {
		t.Errorf("tree = %q, want remote-new", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("remote-new")) {
		t.Errorf("shadow = %q, want remote-new", gotShadow)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1", head)
	}
}

func TestPullLocallyDeletedIsSkipped(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	// Shadow present but tree missing = intentional local delete.
	writeShadow(t, root, "x.md", []byte("orig"))

	p := &fakePuller{
		files:   map[string][]byte{"x.md": []byte("remote-new")},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Tree stays absent.
	if _, err := os.Stat(filepath.Join(root, "x.md")); !os.IsNotExist(err) {
		t.Errorf("tree should stay absent, err=%v", err)
	}
	// Shadow stays.
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("orig")) {
		t.Errorf("shadow = %q", gotShadow)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q, want HEAD-0 (held)", head)
	}
	if !strings.Contains(out, "skipped (local changes):") {
		t.Errorf("summary missing skip line, got: %q", out)
	}
}

func TestPullUntrackedLocalFilePreserved(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	// User has a local file at path x.md with no shadow entry — untracked.
	writeTree(t, root, "x.md", []byte("user-draft"))

	// Adapter offers a "new" file at the same path.
	p := &fakePuller{
		files:   map[string][]byte{"x.md": []byte("remote-new")},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Local file preserved.
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("user-draft")) {
		t.Errorf("tree = %q, want user-draft", got)
	}
	// Shadow still absent.
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	)); !os.IsNotExist(err) {
		t.Errorf("shadow should stay absent, err=%v", err)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q, want HEAD-0 (held)", head)
	}
	if !strings.Contains(out, "skipped (local changes):") {
		t.Errorf("summary missing skip line, got: %q", out)
	}
}

func TestPullTombstoneRemovesCleanPath(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "gone.md", []byte("orig"))
	writeShadow(t, root, "gone.md", []byte("orig"))

	p := &fakePuller{
		tombstones: []string{"gone.md"},
		newHead:    []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.md")); !os.IsNotExist(err) {
		t.Errorf("tree should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "gone.md",
	)); !os.IsNotExist(err) {
		t.Errorf("shadow should be removed, err=%v", err)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1", head)
	}
	if !strings.Contains(out, "removed:") {
		t.Errorf("summary missing removed line, got: %q", out)
	}
}

func TestPullTombstoneSkippedWhenDirty(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "x.md", []byte("local-edit"))
	writeShadow(t, root, "x.md", []byte("orig"))

	p := &fakePuller{
		tombstones: []string{"x.md"},
		newHead:    []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "x.md")); !bytes.Equal(got, []byte("local-edit")) {
		t.Errorf("tree should stay, got %q", got)
	}
	gotShadow := readFile(t, filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	))
	if !bytes.Equal(gotShadow, []byte("orig")) {
		t.Errorf("shadow should stay, got %q", gotShadow)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q, want HEAD-0", head)
	}
	if !strings.Contains(out, "skipped (local changes):") {
		t.Errorf("summary missing skip line, got: %q", out)
	}
}

func TestPullTombstoneForLocallyDeletedRemovesShadow(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	// Tree missing, shadow present → locally deleted.
	writeShadow(t, root, "x.md", []byte("orig"))

	p := &fakePuller{
		tombstones: []string{"x.md"},
		newHead:    []byte("HEAD-1"),
	}

	if _, err := run(t, root, p); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "x.md",
	)); !os.IsNotExist(err) {
		t.Errorf("shadow should be removed, err=%v", err)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1", head)
	}
}

func TestPullFullRefreshRemovesMissingTrackedPath(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "inbox/old.md", []byte("old"))
	writeShadow(t, root, "inbox/old.md", []byte("old"))
	writeTree(t, root, "sent/keep.md", []byte("keep"))
	writeShadow(t, root, "sent/keep.md", []byte("keep"))

	p := &fakeFullRefreshPuller{
		files: map[string][]byte{
			"sent/keep.md": []byte("keep"),
			"sent/new.md":  []byte("new"),
		},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "inbox/old.md")); !os.IsNotExist(err) {
		t.Fatalf("old tree path should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workdir.Layout{Root: root}.Shadow(), "inbox/old.md",
	)); !os.IsNotExist(err) {
		t.Fatalf("old shadow path should be removed, err=%v", err)
	}
	if got := readFile(t, filepath.Join(root, "sent/new.md")); !bytes.Equal(got, []byte("new")) {
		t.Fatalf("new tree path = %q, want new", got)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Fatalf("HEAD = %q, want HEAD-1", head)
	}
	if !strings.Contains(out, "removed:  inbox/old.md") {
		t.Fatalf("summary missing removed line, got: %q", out)
	}
	if !strings.Contains(out, "updated:  sent/new.md") {
		t.Fatalf("summary missing updated line, got: %q", out)
	}
}

// Idempotent re-pull: adapter re-emits a file whose tree+shadow already
// match the proposed content. The path must not appear in the summary
// (it's an applied no-op, per pull design §3), but HEAD still advances.
func TestPullUnchangedFileIsSilentNoOp(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "x.md", []byte("same"))
	writeShadow(t, root, "x.md", []byte("same"))

	p := &fakePuller{
		files:   map[string][]byte{"x.md": []byte("same")},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "updated:") {
		t.Errorf("unchanged file should not produce 'updated:', got: %q", out)
	}
	if !strings.Contains(out, "Already up to date.") {
		t.Errorf("expected 'Already up to date.', got: %q", out)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1 (no-op still advances HEAD)", head)
	}
}

// Mixed batch: one path unchanged, one path genuinely updated. Only the
// changed path appears in the summary.
func TestPullMixedChangedAndUnchanged(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	writeTree(t, root, "same.md", []byte("same"))
	writeShadow(t, root, "same.md", []byte("same"))
	writeTree(t, root, "changed.md", []byte("old"))
	writeShadow(t, root, "changed.md", []byte("old"))

	p := &fakePuller{
		files: map[string][]byte{
			"same.md":    []byte("same"),
			"changed.md": []byte("new"),
		},
		newHead: []byte("HEAD-1"),
	}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "same.md") {
		t.Errorf("unchanged path must not appear in summary: %q", out)
	}
	if !strings.Contains(out, "updated:") || !strings.Contains(out, "changed.md") {
		t.Errorf("expected 'updated:' line for changed.md, got: %q", out)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-1")) {
		t.Errorf("HEAD = %q, want HEAD-1", head)
	}
}

func TestPullNoChangesAlreadyUpToDate(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	p := &fakePuller{newHead: []byte("HEAD-0")}

	out, err := run(t, root, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "Already up to date.") {
		t.Errorf("missing 'Already up to date.', got: %q", out)
	}
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q", head)
	}
}

func TestPullNotInWorkdir(t *testing.T) {
	// Point Dir at a directory with no .gitfs/.
	root := t.TempDir()
	err := Run(context.Background(), Args{Dir: root}, Deps{
		Adapter: &fakePuller{},
		Stdout:  io.Discard,
	})
	if !uerr.Is(err) {
		t.Errorf("expected *uerr.UserError, got %T: %v", err, err)
	}
}

func TestPullAdapterErrorNoWrites(t *testing.T) {
	root := mkWorkdir(t, []byte("HEAD-0"))
	p := &fakePuller{pullErr: errBoom}
	_, err := run(t, root, p)
	if err == nil {
		t.Fatal("expected error")
	}
	// HEAD untouched.
	head := readFile(t, workdir.Layout{Root: root}.Head())
	if !bytes.Equal(head, []byte("HEAD-0")) {
		t.Errorf("HEAD = %q, want HEAD-0", head)
	}
}

// sentinel error used by TestPullAdapterErrorNoWrites.
var errBoom = &boomErr{}

type boomErr struct{}

func (*boomErr) Error() string { return "boom" }

// TestPullLooksUpAdapterByScheme verifies that when Deps.Adapter is nil,
// Run reads .gitfs/config.toml, parses the URL, and instantiates the
// registered adapter.
func TestPullLooksUpAdapterByScheme(t *testing.T) {
	// Register a fake puller under a unique scheme.
	p := &fakePuller{
		files:   map[string][]byte{"a.md": []byte("hello")},
		newHead: []byte("HEAD-1"),
	}
	adapter.Register("fakepull1", func(adapter.URL) (adapter.Adapter, error) {
		return p, nil
	})

	root := t.TempDir()
	u, err := aurl.Parse("fakepull1://me")
	if err != nil {
		t.Fatal(err)
	}
	if err := workdir.Init(root, u); err != nil {
		t.Fatal(err)
	}
	if err := workdir.WriteHEAD(root, []byte("HEAD-0")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = Run(context.Background(), Args{Dir: root}, Deps{Stdout: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "a.md")); string(got) != "hello" {
		t.Errorf("tree = %q", got)
	}
}

func TestPullWalksUpFromSubdir(t *testing.T) {
	p := &fakePuller{newHead: []byte("HEAD-0")}
	adapter.Register("fakepull2", func(adapter.URL) (adapter.Adapter, error) {
		return p, nil
	})

	root := t.TempDir()
	u, _ := aurl.Parse("fakepull2://me")
	if err := workdir.Init(root, u); err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := Run(context.Background(), Args{Dir: deep}, Deps{Stdout: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Already up to date.") {
		t.Errorf("output = %q", out.String())
	}
}
