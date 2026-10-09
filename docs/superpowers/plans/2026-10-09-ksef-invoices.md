# KSeF Invoices Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A read-only `ksef://` adapter that mirrors one company's KSeF 2.0 invoices: original FA(3) XML byte for byte in `<role>/<yyyy>/<mm>/`, gfs metadata in `_gfs/<role>/<yyyy>/<mm>/`, pulled through encrypted exports.

**Architecture:** Two engine additions first: `adapter.Placer` (attachments placed outside `X.files/`, wrapped by `attach.Placement` and used at every sidecar call site) and `Resource.Files` (attachment bytes a listing delivers, written and tracked by `Env.Store`). Then `internal/adapter/ksef`: URL and credentials, an HTTP client with KSeF-token login, the export pipeline (windows, AES, parts, ZIP, hash checks), metadata→XML mapping, the session (cursor, read-only `Check`, `Download`), `gfs auth login`, and a fake server `kseftest` used by unit and end-to-end tests.

**Tech Stack:** Go 1.25 standard library (`crypto/rsa`, `crypto/aes`, `crypto/cipher`, `crypto/x509`, `archive/zip`, `net/http/httptest`), cobra, the repo's `httpx`, `creds`, `schema`, `xmltree`, `attach`, `changes`, `engine`.

**Spec:** `docs/superpowers/specs/2026-10-09-ksef-invoices-design.md`. Section numbers below refer to it.

## Global Constraints

- Files: originals at `<role>/<yyyy>/<mm>/<ksef>.xml`, metadata at `_gfs/<role>/<yyyy>/<mm>/<ksef>.xml`; roles `issued`=`Subject1`, `received`=`Subject2`, `third-party`=`Subject3`.
- Original bytes are never parsed or rewritten; SHA-256 (base64) must equal `invoiceHash` before anything is written.
- Default `since` is `2026-02-01`; export windows are at most 90 days (KSeF's limit is 100; 90 leaves margin for its UTC/Polish-time rule).
- Every listing reads through `POST /invoices/exports`; `GET /invoices/ksef/<n>` only in `Download`.
- Secrets (KSeF token, access/refresh tokens, AES keys) never in files, logs, errors.
- Adapters without a `Placer` and without `Resource.Files` behave exactly as before: every existing test passes unchanged.
- After every task: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.

## Review Focus

1. A file a user drops into a role folder (`received/2026/10/scan.pdf`, an extra `.xml`) — expected: `status` shows it as `!` "not part of the KSeF mirror", nothing crashes, nothing is sent. Test: Task 1 (`TestPlacedStray`), Task 10 e2e.
2. The same invoice delivered twice (window edges, a truncated package continued from `lastPermanentStorageDate`) — expected: one file, no error. Test: Task 5 (`TestExportDedupAcrossTruncation`).
3. A 429 on export start or an export finishing with status ≥ 400 mid-clone — expected: wait/retry or a clear error, cursor unchanged, nothing half-written for that listing. Test: Task 5 (`TestExport429`, `TestExportFailed`), Task 7 (`TestListErrorKeepsNothing`).
4. An access token expiring during a long clone — expected: refreshed transparently. Test: Task 4 (`TestRefresh`).
5. A pull long after the last one (cursor older than 90 days) — expected: split into windows, no 400 from KSeF. Test: Task 5 (`TestWindowsFromOldCursor`).

---

## Task overview

| # | task | spec |
|---|------|------|
| 1 | `adapter.Placer`, `attach.Placement`, every sidecar call site | §2.1 |
| 2 | `Resource.Files`: bytes from a listing written and tracked | §2.2 |
| 3 | ksef URL, NIP, `since`, roles, credentials | §3 |
| 4 | ksef client, errors, keys, KSeF-token login, refresh; `kseftest` auth | §7, §9, §10.1 |
| 5 | exports: windows, parts, AES, ZIP, hashes, truncation; `kseftest` exports | §6.1–6.2, §9 |
| 6 | metadata → `<invoice>`, schema, RELAX NG | §4, §5 |
| 7 | session: List/cursor, Fetch, Check, Download, Identity; adapter + Placer | §4, §6.3–6.4, §8 |
| 8 | `gfs auth login ksef://…` | §7.1 |
| 9 | CLI registration, `gfs schema ksef`, `gfs help ksef`, start.md | §11 |
| 10 | end-to-end tests | §10.2 |
| 11 | real-site checks on TEST (XAdES helper) | §10.3 |
| 12 | spec touch-ups, final verification | — |

---

### Task 1: `adapter.Placer` and `attach.Placement`

**Files:**
- Modify: `internal/adapter/adapter.go` (add `Placer`)
- Create: `internal/attach/placement.go`
- Modify: `internal/attach/attach.go` (`Derive` takes a `Placement`)
- Modify: `internal/changes/changes.go`, `internal/changes/attachments.go`
- Modify: `internal/engine/attach.go`, `internal/engine/get.go`, `internal/engine/pull.go`, `internal/engine/resolve.go`
- Modify: `internal/adapter/fake/fake.go` (a `Placed` variant)
- Test: `internal/engine/placed_test.go`, `internal/attach/placement_test.go`

**Interfaces:**
- Produces:
  ```go
  // adapter
  type Placer interface {
  	ResourceDir() string
  	AttachmentPath(resPath, name string) string
  	ResourceOf(attPath string) (resPath string, ok bool)
  }
  // attach
  type Placement struct{ p adapter.Placer }
  func PlacementOf(ad any) Placement
  func (pl Placement) IsResource(p string) bool
  func (pl Placement) Dir(resPath string) string
  func (pl Placement) Path(resPath, name string) string
  func (pl Placement) ResourceOf(p string) (string, bool)
  func (pl Placement) Owns(resPath, p string) bool
  func (pl Placement) List(t *workdir.Tree, resPath string) (files, dirs []string, err error)
  func (pl Placement) Placed() bool
  func Derive(pl Placement, root *xmltree.Node, e *schema.Elem, resPath string) map[string]string
  // fake
  func NewPlaced() *Placed // fake adapter whose attachments live outside X.files/
  ```

- [ ] **Step 1: Write the failing tests**

`internal/attach/placement_test.go`:
```go
package attach

import "testing"

type kPlacer struct{}

func (kPlacer) ResourceDir() string { return "_m" }
func (kPlacer) AttachmentPath(res, name string) string {
	return path.Dir(strings.TrimPrefix(res, "_m/")) + "/" + name
}
func (kPlacer) ResourceOf(p string) (string, bool) {
	if strings.HasPrefix(p, "_m/") || !strings.HasSuffix(p, ".bin") {
		return "", false
	}
	return "_m/" + strings.TrimSuffix(p, ".bin") + ".xml", true
}

func TestPlacementDefault(t *testing.T) {
	pl := PlacementOf(nil)
	if pl.Placed() || !pl.IsResource("a/b.xml") || pl.Dir("a/b.xml") != "a/b.files" || pl.Path("a/b.xml", "x.png") != "a/b.files/x.png" {
		t.Fatal(pl)
	}
	if r, ok := pl.ResourceOf("a/b.files/x.png"); !ok || r != "a/b.xml" {
		t.Fatal(r, ok)
	}
}

func TestPlacementPlaced(t *testing.T) {
	pl := Placement{p: kPlacer{}}
	if !pl.Placed() || pl.IsResource("r/a.bin") || !pl.IsResource("_m/r/a.xml") {
		t.Fatal("resource dir")
	}
	if pl.Path("_m/r/a.xml", "a.bin") != "r/a.bin" || pl.Dir("_m/r/a.xml") != "r" {
		t.Fatal(pl.Path("_m/r/a.xml", "a.bin"))
	}
	if !pl.Owns("_m/r/a.xml", "r/a.bin") || pl.Owns("_m/r/a.xml", "r/b.bin") {
		t.Fatal("owns")
	}
}
```
(imports `path`, `strings`, `testing`.)

`internal/adapter/fake/fake.go` gains a placed variant used by engine tests: resources under `_m/`, attachment `name` of resource `_m/<dir>/<stem>.xml` at `<dir>/<stem>.<name>`.

`internal/engine/placed_test.go`:
```go
package engine

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

func placed(t *testing.T) (*Env, *fake.Placed, *bytes.Buffer) {
	t.Helper()
	ad := fake.NewPlaced()
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	ad.Remote.Put("2", "_m/a/two.xml", `<note><title>Two</title></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, ad, &out
}

func TestPlacedGetAndStatus(t *testing.T) {
	env, _, out := placed(t)
	fetchAll(t, env, out)
	if read(t, env, "a/one.x.png") != "abc" {
		t.Fatal("attachment must land beside, not in a .files folder")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("clean tree: %+v", cs)
	}
	write(t, env, "a/one.x.png", "changed")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Path != "_m/a/one.xml" || cs[0].Attachments[0].Status != 'M' {
		t.Fatalf("%+v", cs)
	}
}

func TestPlacedStray(t *testing.T) {
	env, _, _ := placed(t)
	write(t, env, "a/scan.pdf", "x")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Status != '!' || cs[0].Path != "a/scan.pdf" || !strings.Contains(cs[0].Err.Error(), "not part of the mirror") {
		t.Fatalf("%+v", cs)
	}
}

func TestPlacedNewFileOfResource(t *testing.T) {
	env, _, _ := placed(t)
	write(t, env, "a/two.y.txt", "new")
	cs := status(t, env)
	if len(cs) != 1 || cs[0].Path != "_m/a/two.xml" || cs[0].Attachments[0].Status != 'A' {
		t.Fatalf("a file the placer maps to a resource is that resource's new attachment: %+v", cs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/attach/ ./internal/engine/ -run 'Placement|Placed'`
Expected: FAIL to compile (`PlacementOf`, `fake.NewPlaced` undefined).

- [ ] **Step 3: Implement**

`internal/adapter/adapter.go`, after `Cacher`:
```go
// Placer is implemented by adapters whose attachment files live outside the
// X.files/ sidecar (ksef spec §2.1). Resource files are then only the files
// under ResourceDir; every other file is an attachment of the resource
// ResourceOf names, or a stray.
type Placer interface {
	ResourceDir() string
	AttachmentPath(resPath, name string) string
	ResourceOf(attPath string) (resPath string, ok bool)
}
```

`internal/attach/placement.go`:
```go
package attach

import (
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Placement says where a resource's attachment files live: in its X.files/
// sidecar (attachments spec 3.2), or where the adapter's Placer puts them.
type Placement struct{ p adapter.Placer }

// PlacementOf is ad's placement; anything that is not a Placer gets X.files/.
func PlacementOf(ad any) Placement {
	p, _ := ad.(adapter.Placer)
	return Placement{p: p}
}

func (pl Placement) Placed() bool { return pl.p != nil }

// IsResource reports whether a scanned file is a resource file.
func (pl Placement) IsResource(p string) bool {
	return pl.p == nil || strings.HasPrefix(p, pl.p.ResourceDir()+"/")
}

// Dir is the folder holding resPath's attachment files.
func (pl Placement) Dir(resPath string) string {
	if pl.p == nil {
		return SidecarDir(resPath)
	}
	return path.Dir(pl.p.AttachmentPath(resPath, "x"))
}

// Path is where the attachment file called name of resPath lives.
func (pl Placement) Path(resPath, name string) string {
	if pl.p == nil {
		return SidecarDir(resPath) + "/" + name
	}
	return pl.p.AttachmentPath(resPath, name)
}

func (pl Placement) ResourceOf(p string) (string, bool) {
	if pl.p == nil {
		return ResourceOf(p)
	}
	return pl.p.ResourceOf(p)
}

// Owns reports whether the file p in Dir(resPath) belongs to resPath.
func (pl Placement) Owns(resPath, p string) bool {
	if pl.p == nil {
		return path.Dir(p) == SidecarDir(resPath)
	}
	r, ok := pl.p.ResourceOf(p)
	return ok && r == resPath
}

// List is ListSidecar of resPath's attachment folder, keeping only its files;
// a placed folder is shared, so it reports no subdirectories.
func (pl Placement) List(t *workdir.Tree, resPath string) (files, dirs []string, err error) {
	files, dirs, err = t.ListSidecar(pl.Dir(resPath))
	if pl.p == nil || err != nil {
		return files, dirs, err
	}
	var own []string
	for _, f := range files {
		if pl.Owns(resPath, f) {
			own = append(own, f)
		}
	}
	return own, nil, nil
}
```
Check `attach` does not create an import cycle: `adapter` imports only `schema`, `xmltree`; `attach` already imports `workdir`.

`internal/attach/attach.go`: `Derive(pl Placement, root, e, resPath)`; replace `dir := SidecarDir(resPath)` / `out[it.id] = dir + "/" + name` with `out[it.id] = pl.Path(resPath, name)`.

Call sites (each gets `pl := attach.PlacementOf(<adapter>)`; engine adds `func (e *Env) placement() attach.Placement { return attach.PlacementOf(e.Adapter) }`):

| file | change |
|---|---|
| `changes/attachments.go` | `attCtx` gains `pl attach.Placement` and `files map[string]bool` (attachment files accounted for). `claim`: `c.claimed[c.pl.Dir(resPath)] = true` only when `!c.pl.Placed()`. `changes`: `dir := c.pl.Dir(resPath)`; `files, dirs, err := c.pl.List(c.t, resPath)`; mark every file of `files` and every tracked `cur` in `c.files`; both `attach.Derive` calls pass `c.pl`. `current`: `alt := dir + "/" + path.Base(l.Path)` unchanged (dir now from placement). `orphans`: `attach.ResourceOf` → `c.pl.ResourceOf`; skip the `Sidecars()` folder check when `c.pl.Placed()`. |
| `changes/changes.go` | `ac := &attCtx{..., pl: attach.PlacementOf(ad), files: map[string]bool{}}`. In the scan loop, when `!ac.pl.IsResource(p)`: append `p` to `others` and `continue`. After `orphans`, for each `p` in `others` not in `ac.files`: `out = append(out, FileChange{Status: '!', Path: p, Err: errors.New("not part of the mirror; gfs ignores it (move it elsewhere)")})`. |
| `engine/attach.go` | `placeAttachments`: `derived := attach.Derive(e.placement(), root, el, resPath)`; `dir := e.placement().Dir(resPath)`. `sidecarFiles`: `files, _, _ := e.placement().List(e.Tree, fc.Path)`. |
| `engine/get.go` | `derived := attach.Derive(e.placement(), root, el, wp)`; `sd := e.placement().Dir(wp)`; `whole` only matches `t == sd` when `!e.placement().Placed()` (a placed folder is shared by many resources). `workingPaths`: skip `p` when `!e.placement().IsResource(p)`. |
| `engine/pull.go` | `selected`: `res, _ := e.placement().ResourceOf(l.Path)`. |
| `engine/resolve.go` | `if _, inSidecar := e.placement().ResourceOf(p); inSidecar` (and skip non-resource files when placed). |

`internal/adapter/fake/fake.go`:
```go
// Placed is the fake adapter with attachments outside X.files/: resources
// live under _m/, attachment name of _m/<dir>/<stem>.xml is <dir>/<stem>.<name>.
type Placed struct{ *Adapter }

func NewPlaced() *Placed { return &Placed{New()} }

func (*Placed) ResourceDir() string { return "_m" }

func (*Placed) AttachmentPath(resPath, name string) string {
	rel := strings.TrimPrefix(resPath, "_m/")
	return strings.TrimSuffix(rel, ".xml") + "." + name
}

func (*Placed) ResourceOf(p string) (string, bool) {
	if strings.HasPrefix(p, "_m/") {
		return "", false
	}
	dir, file := path.Split(p)
	stem, _, ok := strings.Cut(file, ".")
	if !ok {
		return "", false
	}
	return "_m/" + dir + stem + ".xml", true
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/attach/ ./internal/changes/ ./internal/engine/ ./internal/cli/`
Expected: PASS, including every pre-existing attachment test.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter internal/attach internal/changes internal/engine
git commit -m "engine: attachments placed outside X.files by the adapter (Placer)"
```

### Task 2: `Resource.Files`

**Files:**
- Modify: `internal/adapter/adapter.go` (`Resource.Files`)
- Modify: `internal/engine/engine.go` (`Store` writes files; `saveIndex` flushes attachments)
- Modify: `internal/adapter/fake/fake.go` (`Remote.WithFiles`)
- Test: `internal/engine/placed_test.go`

**Interfaces:**
- Consumes: `attach.Placement`, `attach.Derive(pl, …)` (Task 1)
- Produces: `adapter.Resource.Files map[string][]byte`; `fake.Remote.WithFiles bool`

- [ ] **Step 1: Write the failing test**

```go
func TestListingDeliversFiles(t *testing.T) {
	ad := fake.NewPlaced()
	ad.Remote.WithFiles = true
	ad.Remote.Put("1", "_m/a/one.xml", `<note><title>One</title></note>`)
	ad.Remote.PutAttachment("1", "10", "x.png", []byte("abc"))
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, env, "a/one.x.png") != "abc" || ad.Remote.Downloads != 0 {
		t.Fatal("bytes from the listing are written without a download")
	}
	if l, ok := env.Atts.Get("1", "10"); !ok || l.Path != "a/one.x.png" || l.Version != "1" {
		t.Fatalf("tracked as fetched: %+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
	ad.Remote.Put("2", "_m/a/two.xml", `<note><title>Two</title></note>`)
	ad.Remote.PutAttachment("2", "20", "y.png", []byte("def"))
	pull(t, env, PullOpts{})
	if read(t, env, "a/two.y.png") != "def" {
		t.Fatal("pull writes listed bytes of a new resource")
	}
	fresh, _ := env.Tree.LoadAttachments()
	if _, ok := fresh.Get("2", "20"); !ok {
		t.Fatal(".gfs/attachments saved at the end of the batch")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/engine/ -run TestListingDeliversFiles` — Expected: FAIL (`WithFiles` undefined).

- [ ] **Step 3: Implement**

`adapter.Resource`:
```go
	// Files are attachment bytes by attachment id that the listing already
	// holds (ksef spec §2.2): Store writes and tracks them as if fetched.
	Files map[string][]byte
```

`fake.Remote`: `WithFiles bool // listed resources carry their attachment bytes`; in `resource()`: when `r.WithFiles`, `res.Files[a.id] = a.data`.

`engine/engine.go`, `Store` after `StoreBase` succeeds:
```go
	return e.storeFiles(res)
}

// storeFiles writes the attachment bytes a listing delivered and tracks them.
func (e *Env) storeFiles(res *adapter.Resource) error {
	el := e.attachmentElem()
	if len(res.Files) == 0 || el == nil {
		return nil
	}
	derived := attach.Derive(e.placement(), res.Root, el, res.Path)
	els := attach.Elements(res.Root, el)
	for _, id := range sortedIDs(derived) {
		data, ok := res.Files[id]
		if !ok {
			continue
		}
		p := derived[id]
		if _, tracked := e.Atts.Get(res.ID, id); !tracked && e.Tree.Exists(p) {
			fmt.Fprintf(e.Out, "  !  %s   exists locally and is not tracked; skipped\n", p)
			continue
		}
		if err := e.Tree.WriteFile(p, data); err != nil {
			return err
		}
		line, err := attach.Entry(e.Tree, res.ID, id, attach.Version(els[id], el), p)
		if err != nil {
			return err
		}
		e.Atts.Put(line)
	}
	if e.Batch {
		return nil
	}
	return e.saveAtts()
}
```
(`Store`'s current `return e.StoreBase(res, oldPath)` becomes `if err := e.StoreBase(res, oldPath); err != nil { return err }` then `return e.storeFiles(res)`; `sortedIDs` is in `attach.go`.)

`saveIndex`: when it writes (forced, or the batch threshold), also `if e.Atts != nil { if err := e.saveAtts(); err != nil { return err } }`.

- [ ] **Step 4: Run** `go test ./internal/...` — Expected: PASS.

- [ ] **Step 5: Commit** `git commit -am "engine: a listing may deliver attachment bytes (Resource.Files)"`

### Task 3: ksef URL, NIP, `since`, roles, credentials

**Files:**
- Create: `internal/adapter/ksef/url.go`, `internal/adapter/ksef/url_test.go`

**Interfaces:**
- Produces:
  ```go
  var roles = []string{"issued", "received", "third-party"}
  var subjectOf = map[string]string{"issued": "Subject1", "received": "Subject2", "third-party": "Subject3"}
  func validNIP(s string) bool
  func normalize(u *url.URL) (*url.URL, error)
  func defaultDir(u *url.URL) string
  func entryName(host, nip string) string            // "ksef:<host>/<nip>"
  type EntryStore interface{ GetEntry(name string) (string, error) }
  type target struct{ host, nip, base, token string; since string; roles []string }
  func parseTarget(u *url.URL, getenv func(string) string, store EntryStore) (target, error)
  func sinceTime(s string, now time.Time) (time.Time, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
package ksef

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestValidNIP(t *testing.T) {
	for _, s := range []string{"6887603807", "5261040828"} {
		if !validNIP(s) {
			t.Error(s)
		}
	}
	for _, s := range []string{"6887603808", "123", "abcdefghij", "0000000000x"} {
		if validNIP(s) {
			t.Error(s)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"ksef://KSeF.mf.gov.pl/6887603807":                               "ksef://ksef.mf.gov.pl/6887603807",
		"ksef://test.ksef.mf.gov.pl/6887603807/":                         "ksef://test.ksef.mf.gov.pl/6887603807",
		"ksef://ksef.mf.gov.pl/6887603807?roles=received,issued":         "ksef://ksef.mf.gov.pl/6887603807?roles=issued,received",
		"ksef://ksef.mf.gov.pl/6887603807?roles=third-party,issued,received": "ksef://ksef.mf.gov.pl/6887603807",
		"ksef://ksef.mf.gov.pl/6887603807?since=-90d&roles=received":     "ksef://ksef.mf.gov.pl/6887603807?since=-90d&roles=received",
		"ksef://ksef.mf.gov.pl/6887603807?since=2026-02-01":              "ksef://ksef.mf.gov.pl/6887603807",
	} {
		n, err := normalize(mustURL(t, in))
		if err != nil || n.String() != want {
			t.Errorf("%s: %v %v, want %s", in, n, err, want)
		}
	}
	for _, in := range []string{
		"ksef://example.com/6887603807", "ksef://ksef.mf.gov.pl/6887603808", "ksef://ksef.mf.gov.pl/",
		"ksef://ksef.mf.gov.pl/6887603807?roles=authorized", "ksef://ksef.mf.gov.pl/6887603807?since=yesterday",
		"ksef://ksef.mf.gov.pl/6887603807?limit=5", "ksef://ksef.mf.gov.pl/6887603807/x",
	} {
		if _, err := normalize(mustURL(t, in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	for in, want := range map[string]string{
		"ksef://ksef.mf.gov.pl/6887603807":      "ksef-6887603807",
		"ksef://test.ksef.mf.gov.pl/6887603807": "ksef-test-6887603807",
		"ksef://demo.ksef.mf.gov.pl/6887603807": "ksef-demo-6887603807",
	} {
		if got := defaultDir(mustURL(t, in)); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestSinceTime(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":           time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		"2025-03-04": time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC),
		"-10d":       now.AddDate(0, 0, -10),
		"-2w":        now.AddDate(0, 0, -14),
		"-6m":        now.AddDate(0, -6, 0),
	} {
		got, err := sinceTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
}

type entries map[string]string

func (e entries) GetEntry(n string) (string, error) {
	if v, ok := e[n]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestParseTarget(t *testing.T) {
	u := mustURL(t, "ksef://test.ksef.mf.gov.pl/6887603807?roles=received")
	env := func(string) string { return "" }
	if _, err := parseTarget(u, env, entries{}); err == nil || !strings.Contains(err.Error(), "run gfs auth login ksef://test.ksef.mf.gov.pl/6887603807") {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, env, entries{"ksef:test.ksef.mf.gov.pl/6887603807": "tok"})
	if err != nil || tg.token != "tok" || tg.base != "https://api-test.ksef.mf.gov.pl/v2" || len(tg.roles) != 1 || tg.roles[0] != "received" {
		t.Fatalf("%+v %v", tg, err)
	}
	tg, _ = parseTarget(u, func(k string) string { return map[string]string{"GFS_KSEF_TOKEN": "envtok"}[k] }, entries{})
	if tg.token != "envtok" {
		t.Fatal(tg.token)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/` — Expected: FAIL (package has no non-test files / undefined).

- [ ] **Step 3: Implement** `internal/adapter/ksef/url.go`:

```go
// Package ksef mirrors the invoices of one company in KSeF 2.0, the Polish
// national e-invoicing system, read-only (ksef spec).
package ksef

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultSince = "2026-02-01"

// apiBase maps the URL host to the API (ksef spec §3).
var apiBase = map[string]string{
	"ksef.mf.gov.pl":      "https://api.ksef.mf.gov.pl/v2",
	"demo.ksef.mf.gov.pl": "https://api-demo.ksef.mf.gov.pl/v2",
	"test.ksef.mf.gov.pl": "https://api-test.ksef.mf.gov.pl/v2",
}

var roles = []string{"issued", "received", "third-party"}

var subjectOf = map[string]string{"issued": "Subject1", "received": "Subject2", "third-party": "Subject3"}

const forms = "want ksef://ksef.mf.gov.pl/<nip>[?since=<date>&roles=issued,received,third-party] (demo.ksef.mf.gov.pl, test.ksef.mf.gov.pl for DEMO and TEST)"

var (
	nipRe   = regexp.MustCompile(`^[0-9]{10}$`)
	sinceRe = regexp.MustCompile(`^-(\d+)([dwm])$`)
)

// validNIP checks the 10 digits and the check digit.
func validNIP(s string) bool {
	if !nipRe.MatchString(s) {
		return false
	}
	w := []int{6, 5, 7, 2, 3, 4, 5, 6, 7}
	sum := 0
	for i, x := range w {
		sum += int(s[i]-'0') * x
	}
	return sum%11 != 10 && sum%11 == int(s[9]-'0')
}

// sinceTime resolves since: "" is 2026-02-01, a date, or -<n>d|w|m before now.
func sinceTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		s = defaultSince
	}
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			return now.AddDate(0, 0, -n), nil
		case "w":
			return now.AddDate(0, 0, -7*n), nil
		default:
			return now.AddDate(0, -n, 0), nil
		}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("since=%s: want a date (2026-02-01) or -90d, -2w, -6m", s)
	}
	return t, nil
}

func roleList(v string) ([]string, error) {
	if v == "" {
		return roles, nil
	}
	want := map[string]bool{}
	for _, r := range strings.Split(v, ",") {
		r = strings.ToLower(strings.TrimSpace(r))
		if !slices.Contains(roles, r) {
			return nil, fmt.Errorf("roles: unknown role %q (want issued, received, third-party)", r)
		}
		want[r] = true
	}
	var out []string
	for _, r := range roles {
		if want[r] {
			out = append(out, r)
		}
	}
	return out, nil
}

// normalize checks a ksef:// URL and gives its canonical form.
func normalize(u *url.URL) (*url.URL, error) {
	host := strings.ToLower(u.Host)
	if u.Scheme != "ksef" || apiBase[host] == "" {
		return nil, fmt.Errorf("%s: %s", u.Redacted(), forms)
	}
	nip := strings.Trim(u.Path, "/")
	if !validNIP(nip) {
		return nil, fmt.Errorf("%s: %q is not a valid NIP (10 digits with a check digit); %s", u.Redacted(), nip, forms)
	}
	q := u.Query()
	for k := range q {
		if k != "since" && k != "roles" && k != "base" {
			return nil, fmt.Errorf("unknown parameter %q (want since, roles)", k)
		}
	}
	var parts []string
	if s := q.Get("since"); s != "" && s != defaultSince {
		if _, err := sinceTime(s, time.Now()); err != nil {
			return nil, err
		}
		parts = append(parts, "since="+s)
	}
	rs, err := roleList(q.Get("roles"))
	if err != nil {
		return nil, err
	}
	if len(rs) < len(roles) {
		parts = append(parts, "roles="+strings.Join(rs, ","))
	}
	if b := q.Get("base"); b != "" {
		parts = append(parts, "base="+url.QueryEscape(b))
	}
	return &url.URL{Scheme: "ksef", Host: host, Path: "/" + nip, RawQuery: strings.Join(parts, "&")}, nil
}

func defaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "ksef"
	}
	env, _, _ := strings.Cut(n.Host, ".ksef.")
	if n.Host == "ksef.mf.gov.pl" {
		env = ""
	}
	if env != "" {
		env += "-"
	}
	return "ksef-" + env + strings.Trim(n.Path, "/")
}

func entryName(host, nip string) string { return "ksef:" + host + "/" + nip }

func loginHint(host, nip string) string { return "gfs auth login ksef://" + host + "/" + nip }

// EntryStore reads keyring entries (creds.Store satisfies it).
type EntryStore interface {
	GetEntry(name string) (string, error)
}

type target struct {
	host, nip, base string
	token           string
	since           string // as written; "" is the default
	roles           []string
}

// parseTarget resolves the URL and the KSeF token (GFS_KSEF_TOKEN, then the
// keyring entry ksef:<host>/<nip>).
func parseTarget(u *url.URL, getenv func(string) string, store EntryStore) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	q := n.Query()
	t := target{host: n.Host, nip: strings.Trim(n.Path, "/"), base: apiBase[n.Host], since: q.Get("since")}
	if b := q.Get("base"); b != "" {
		t.base = b
	}
	if t.roles, err = roleList(q.Get("roles")); err != nil {
		return target{}, err
	}
	if tok := getenv("GFS_KSEF_TOKEN"); tok != "" {
		t.token = tok
		return t, nil
	}
	tok, err := store.GetEntry(entryName(t.host, t.nip))
	if err != nil || tok == "" {
		return target{}, fmt.Errorf("no KSeF token for ksef://%s/%s: run %s", t.host, t.nip, loginHint(t.host, t.nip))
	}
	t.token = tok
	return t, nil
}
```
Note `validNIP`: sum%11 == 10 is never valid; otherwise the check digit equals sum%11.

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/` — Expected: PASS.
- [ ] **Step 5: Commit** `git add internal/adapter/ksef && git commit -m "ksef: URL, NIP, since, roles, credentials"`

### Task 4: client, errors, keys, KSeF-token login, refresh; `kseftest` auth

**Files:**
- Create: `internal/adapter/ksef/client.go`, `internal/adapter/ksef/auth.go`, `internal/adapter/ksef/client_test.go`
- Create: `internal/adapter/ksef/kseftest/server.go`

**Interfaces:**
- Produces (ksef):
  ```go
  type APIError struct{ Status int; Op, Msg, Hint string }
  type Client struct{ Base, NIP, Token, LoginHint string; Now func() time.Time; Sleep func(context.Context, time.Duration) error; OnWait func(string); /* … */ }
  func NewClient(base, nip, token string) *Client
  func (c *Client) json(ctx context.Context, method, path string, auth bool, in, out any) error
  func (c *Client) raw(ctx context.Context, method, rawURL string, auth bool) ([]byte, error)
  func (c *Client) key(ctx context.Context, usage string) (*rsa.PublicKey, error)
  func (c *Client) Login(ctx context.Context) error
  func (c *Client) access(ctx context.Context) (string, error)   // logs in or refreshes as needed
  func encryptOAEP(pub *rsa.PublicKey, b []byte) (string, error)
  func b64sha(b []byte) string
  ```
- Produces (kseftest):
  ```go
  type Server struct{ *httptest.Server; Token string; AccessTTL time.Duration; Requests []string; /* … */ }
  func New() *Server
  func (s *Server) Decrypt(b64 string) []byte // RSA-OAEP with the server key (tests only)
  ```

- [ ] **Step 1: Write the failing tests** `client_test.go`:

```go
package ksef

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

var bg = context.Background()

func testClient(srv *kseftest.Server) *Client {
	c := NewClient(srv.URL, kseftest.NIP, srv.Token)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestLogin(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	tok, err := c.access(bg)
	if err != nil || tok == "" {
		t.Fatal(tok, err)
	}
	want := []string{"GET /security/public-key-certificates", "POST /auth/challenge", "POST /auth/ksef-token", "GET /auth/", "POST /auth/token/redeem"}
	for i, w := range want {
		if !strings.HasPrefix(srv.Requests[i], w) {
			t.Fatalf("%d: %s, want %s", i, srv.Requests[i], w)
		}
	}
	if again, _ := c.access(bg); again != tok || len(srv.Requests) != len(want) {
		t.Fatal("a valid access token is reused")
	}
}

func TestLoginBadToken(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	c.Token = "wrong"
	c.LoginHint = "gfs auth login ksef://test.ksef.mf.gov.pl/" + kseftest.NIP
	_, err := c.access(bg)
	if err == nil || !strings.Contains(err.Error(), "run gfs auth login ksef://") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("refused token: hint, and the token never in the message: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.AccessTTL = 30 * time.Second // under a minute left: refresh before use
	c := testClient(srv)
	first, _ := c.access(bg)
	second, err := c.access(bg)
	if err != nil || second == first || srv.Count("POST /auth/token/refresh") != 1 {
		t.Fatalf("%v %v", err, srv.Requests)
	}
}

func TestAPIErrorShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"exception":{"exceptionDetailList":[{"exceptionCode":21405,"exceptionDescription":"Błąd walidacji danych wejściowych.","details":["dateRange too long"]}]}}`: "Błąd walidacji danych wejściowych. (21405): dateRange too long",
		`{"title":"Unauthorized","status":401,"detail":"Token expired"}`:                                                                                              "Unauthorized: Token expired",
		`{"status":{"code":429,"description":"Too Many Requests","details":["Przekroczono limit 20 żądań na godzinę."]}}`:                                              "Too Many Requests: Przekroczono limit 20 żądań na godzinę.",
		`not json`: "HTTP 500",
	} {
		if got := apiMessage(500, []byte(body)); got != want {
			t.Errorf("%s\n got %q\nwant %q", body, got, want)
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/ -run 'Login|Refresh|APIError'` — Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`kseftest/server.go` (auth part; exports added in Task 5):
```go
// Package kseftest is a fake KSeF 2.0 API for tests: KSeF-token login with
// real RSA-OAEP, exports with real AES-encrypted ZIP parts.
package kseftest

// NIP is the fake company's (valid) NIP.
const NIP = "6887603807"

type Server struct {
	*httptest.Server
	Token     string        // the accepted KSeF token
	AccessTTL time.Duration // access token lifetime (default 15m)
	Requests  []string      // "METHOD /path" of every request, in order

	mu      sync.Mutex
	key     *rsa.PrivateKey
	certDER []byte
	seq     int
	auths   map[string]bool   // authentication token -> token was right
	access  map[string]time.Time // access token -> valid until
	refresh map[string]bool
	// exports (Task 5)
}

func New() *Server
func (s *Server) Count(prefix string) int
func (s *Server) Decrypt(b64 string) []byte
```
Handlers:
- `GET /security/public-key-certificates`: two entries with the same self-signed cert (base64 DER), usages `["KsefTokenEncryption"]` and `["SymmetricKeyEncryption"]`.
- `POST /auth/challenge`: `{"challenge":"C-<n>","timestamp":<RFC3339>,"timestampMs":<ms>,"clientIp":"127.0.0.1"}`; remember ms per challenge.
- `POST /auth/ksef-token`: decrypt `encryptedToken`; ok when it equals `Token|<ms of challenge>` and `contextIdentifier` is `{Nip, NIP}`; answer 202 `{"referenceNumber":"AU-<n>","authenticationToken":{"token":"op-<n>","validUntil":…}}`.
- `GET /auth/<ref>` (bearer op token): `{"status":{"code":200,"description":"Uwierzytelnianie zakończone sukcesem"}}` or, for a wrong token, `{"status":{"code":450,"description":"Uwierzytelnianie zakończone niepowodzeniem z powodu błędnego tokenu","details":["Invalid token"]}}`.
- `POST /auth/token/redeem` (bearer op token): `{"accessToken":{"token":"acc-<n>","validUntil":now+AccessTTL},"refreshToken":{"token":"ref-<n>","validUntil":now+7d}}`.
- `POST /auth/token/refresh` (bearer refresh token): `{"accessToken":{…}}`.
- Every other path requires a live access token, else 401 `application/problem+json` `{"title":"Unauthorized","status":401,"detail":"Brak lub nieprawidłowy token"}`.

`client.go`:
```go
// Client calls KSeF 2.0: JSON over HTTPS, bearer access tokens from a KSeF
// token (ksef spec §7), the shared retry policy (httpx).
type Client struct {
	Base, NIP string
	Token     string // the KSeF token: a secret, never printed
	LoginHint string // "gfs auth login ksef://<host>/<nip>"
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	OnWait    func(msg string)
	hc        *http.Client

	keys         map[string]*rsa.PublicKey
	accessTok    string
	accessUntil  time.Time
	refreshTok   string
}

func NewClient(base, nip, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), NIP: nip, Token: token, Now: time.Now, Sleep: httpx.SleepCtx,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 5 * time.Minute}}
}

// APIError is a KSeF refusal. Msg comes from KSeF; Hint is the command that
// fixes credentials (401/403).
type APIError struct {
	Status   int
	Op, Msg  string
	Hint     string
}

func (e *APIError) Error() string {
	s := "ksef: " + e.Op + ": " + e.Msg
	if e.Hint != "" && (e.Status == 401 || e.Status == 403) {
		s += " (run " + e.Hint + ")"
	}
	return s
}

// apiMessage reads KSeF's three error shapes: ExceptionResponse (400),
// problem+json (401/403) and the 429 status object.
func apiMessage(status int, body []byte) string {
	var v struct {
		Exception *struct {
			List []struct {
				Code    int      `json:"exceptionCode"`
				Desc    string   `json:"exceptionDescription"`
				Details []string `json:"details"`
			} `json:"exceptionDetailList"`
		} `json:"exception"`
		Title  string          `json:"title"`
		Detail string          `json:"detail"`
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(body, &v) != nil {
		return fmt.Sprintf("HTTP %d", status)
	}
	if v.Exception != nil && len(v.Exception.List) > 0 {
		var parts []string
		for _, d := range v.Exception.List {
			p := fmt.Sprintf("%s (%d)", d.Desc, d.Code)
			if len(d.Details) > 0 {
				p += ": " + strings.Join(d.Details, "; ")
			}
			parts = append(parts, p)
		}
		return strings.Join(parts, "; ")
	}
	if v.Title != "" {
		if v.Detail != "" {
			return v.Title + ": " + v.Detail
		}
		return v.Title
	}
	var st struct {
		Description string   `json:"description"`
		Details     []string `json:"details"`
	}
	if json.Unmarshal(v.Status, &st) == nil && st.Description != "" {
		if len(st.Details) > 0 {
			return st.Description + ": " + strings.Join(st.Details, "; ")
		}
		return st.Description
	}
	return fmt.Sprintf("HTTP %d", status)
}

func (c *Client) retrier() *httpx.Retrier {
	return &httpx.Retrier{Service: "KSeF", Sleep: c.Sleep, Now: c.Now, OnWait: c.OnWait}
}

// send runs one request; auth adds the bearer access token. An absolute
// rawURL (export parts) is used as is and never gets a token.
func (c *Client) send(ctx context.Context, method, rawURL string, auth bool, body []byte, read bool) (*http.Response, []byte, error) {
	u := rawURL
	if strings.HasPrefix(u, "/") {
		u = c.Base + u
	}
	var bearer string
	if auth {
		var err error
		if bearer, err = c.access(ctx); err != nil {
			return nil, nil, err
		}
	}
	resp, err := c.retrier().Send(ctx, c.hc, read, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return req, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ksef: %s %s: %w", method, opName(rawURL), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		return resp, data, &APIError{Status: resp.StatusCode, Op: method + " " + opName(rawURL), Msg: apiMessage(resp.StatusCode, data), Hint: c.LoginHint}
	}
	return resp, data, nil
}

// opName is the path without query or host, for messages (never a signed URL).
func opName(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return "request"
}

func (c *Client) json(ctx context.Context, method, path string, auth bool, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	read := method == http.MethodGet || path == "/invoices/query/metadata"
	_, data, err := c.send(ctx, method, path, auth, body, read)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) raw(ctx context.Context, method, rawURL string, auth bool) ([]byte, error) {
	_, data, err := c.send(ctx, method, rawURL, auth, nil, true)
	return data, err
}

func b64sha(b []byte) string {
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func encryptOAEP(pub *rsa.PublicKey, b []byte) (string, error) {
	enc, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, b, nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}
```
(`send`'s `bytes.NewReader(nil)` on GET is fine.)

`auth.go`:
```go
// key is the Ministry's public key for usage (KsefTokenEncryption,
// SymmetricKeyEncryption), read once per client.
func (c *Client) key(ctx context.Context, usage string) (*rsa.PublicKey, error) {
	if c.keys == nil {
		var certs []struct {
			Certificate string   `json:"certificate"`
			Usage       []string `json:"usage"`
		}
		if err := c.json(ctx, http.MethodGet, "/security/public-key-certificates", false, nil, &certs); err != nil {
			return nil, err
		}
		c.keys = map[string]*rsa.PublicKey{}
		for _, ct := range certs {
			der, err := base64.StdEncoding.DecodeString(ct.Certificate)
			if err != nil {
				continue
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}
			if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
				for _, u := range ct.Usage {
					c.keys[u] = pub
				}
			}
		}
	}
	if k := c.keys[usage]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("ksef: no public key for %s", usage)
}

type tokenInfo struct {
	Token      string    `json:"token"`
	ValidUntil time.Time `json:"validUntil"`
}

// Login authenticates with the KSeF token (ksef spec §7 steps 1-4).
func (c *Client) Login(ctx context.Context) error {
	pub, err := c.key(ctx, "KsefTokenEncryption")
	if err != nil {
		return err
	}
	var ch struct {
		Challenge   string `json:"challenge"`
		TimestampMs int64  `json:"timestampMs"`
	}
	if err := c.json(ctx, http.MethodPost, "/auth/challenge", false, nil, &ch); err != nil {
		return err
	}
	enc, err := encryptOAEP(pub, []byte(fmt.Sprintf("%s|%d", c.Token, ch.TimestampMs)))
	if err != nil {
		return err
	}
	var init struct {
		ReferenceNumber     string    `json:"referenceNumber"`
		AuthenticationToken tokenInfo `json:"authenticationToken"`
	}
	req := map[string]any{"challenge": ch.Challenge, "contextIdentifier": map[string]string{"type": "Nip", "value": c.NIP}, "encryptedToken": enc}
	if err := c.json(ctx, http.MethodPost, "/auth/ksef-token", false, req, &init); err != nil {
		return err
	}
	op := init.AuthenticationToken.Token
	wait := 500 * time.Millisecond
	for deadline := c.Now().Add(2 * time.Minute); ; {
		var st struct {
			Status struct {
				Code        int      `json:"code"`
				Description string   `json:"description"`
				Details     []string `json:"details"`
			} `json:"status"`
		}
		if err := c.withBearer(ctx, http.MethodGet, "/auth/"+init.ReferenceNumber, op, &st); err != nil {
			return err
		}
		switch {
		case st.Status.Code == 200:
		case st.Status.Code >= 400:
			msg := st.Status.Description
			if len(st.Status.Details) > 0 {
				msg += ": " + strings.Join(st.Status.Details, "; ")
			}
			return &APIError{Status: 401, Op: "login", Msg: msg, Hint: c.LoginHint}
		case c.Now().After(deadline):
			return &APIError{Op: "login", Msg: "authentication did not finish in 2 minutes"}
		default:
			if err := c.Sleep(ctx, wait); err != nil {
				return err
			}
			wait = min(2*wait, 5*time.Second)
			continue
		}
		break
	}
	var tok struct {
		AccessToken  tokenInfo `json:"accessToken"`
		RefreshToken tokenInfo `json:"refreshToken"`
	}
	if err := c.withBearer(ctx, http.MethodPost, "/auth/token/redeem", op, &tok); err != nil {
		return err
	}
	c.accessTok, c.accessUntil, c.refreshTok = tok.AccessToken.Token, tok.AccessToken.ValidUntil, tok.RefreshToken.Token
	return nil
}

// withBearer is a JSON call with an explicit bearer (the operation or the
// refresh token) instead of the access token.
func (c *Client) withBearer(ctx context.Context, method, path, bearer string, out any) error {
	resp, err := c.retrier().Send(ctx, c.hc, method == http.MethodGet, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("ksef: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Op: method + " " + path, Msg: apiMessage(resp.StatusCode, data), Hint: c.LoginHint}
	}
	return json.Unmarshal(data, out)
}

// access is a live access token: logs in the first time, refreshes when less
// than a minute is left (ksef spec §7).
func (c *Client) access(ctx context.Context) (string, error) {
	switch {
	case c.accessTok == "":
		if err := c.Login(ctx); err != nil {
			return "", err
		}
	case c.Now().Add(time.Minute).After(c.accessUntil):
		var r struct {
			AccessToken tokenInfo `json:"accessToken"`
		}
		if err := c.withBearer(ctx, http.MethodPost, "/auth/token/refresh", c.refreshTok, &r); err != nil {
			if err := c.Login(ctx); err != nil {
				return "", err
			}
		} else {
			c.accessTok, c.accessUntil = r.AccessToken.Token, r.AccessToken.ValidUntil
		}
	}
	return c.accessTok, nil
}
```

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git add internal/adapter/ksef && git commit -m "ksef: client, KSeF-token login and refresh, fake auth"`

### Task 5: exports

**Files:**
- Create: `internal/adapter/ksef/export.go`, `internal/adapter/ksef/export_test.go`
- Modify: `internal/adapter/ksef/kseftest/server.go` (exports, invoices, faults)

**Interfaces:**
- Consumes: `Client.json`, `Client.raw`, `Client.key`, `b64sha`, `encryptOAEP` (Task 4)
- Produces:
  ```go
  type apiInvoice struct { /* InvoiceMetadata, JSON tags as KSeF */ }
  type invoice struct{ meta apiInvoice; xml []byte }
  type exportPage struct{ invoices []invoice; truncated bool; hwm, last string }
  func (c *Client) export(ctx context.Context, subject string, from time.Time, to *time.Time) (exportPage, error)
  func (c *Client) exportRole(ctx context.Context, subject string, from, now time.Time) (invs []invoice, next time.Time, err error)
  const window = 90 * 24 * time.Hour
  // kseftest
  type Invoice struct{ Subject, KsefNumber, Number, SellerNIP, SellerName, BuyerNIP, BuyerName string; Gross, Net, VAT float64; Stored time.Time; XML []byte; BadHash bool }
  func (s *Server) Add(inv Invoice)
  // fields: TruncateAt int; PartSize int; Fail429 int; ExportFail string; DamagePart bool; HWM func() time.Time; Exports []ExportCall
  type ExportCall struct{ Subject string; From, To string }
  ```

- [ ] **Step 1: Write the failing tests** `export_test.go`:

```go
package ksef

import (
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func addN(srv *kseftest.Server, subject string, n int, from time.Time) {
	for i := range n {
		srv.Add(kseftest.Invoice{Subject: subject, Stored: from.Add(time.Duration(i) * time.Hour)})
	}
}

func TestExportRole(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject2", 3, t0)
	c := testClient(srv)
	now := t0.Add(24 * time.Hour)
	srv.HWM = func() time.Time { return now.Add(-2 * time.Minute) }
	invs, next, err := c.exportRole(bg, "Subject2", t0.Add(-time.Hour), now)
	if err != nil || len(invs) != 3 {
		t.Fatalf("%d %v", len(invs), err)
	}
	for _, inv := range invs {
		if b64sha(inv.xml) != inv.meta.InvoiceHash || !strings.HasPrefix(string(inv.xml), "<?xml") {
			t.Fatalf("original bytes with their hash: %s", inv.meta.KsefNumber)
		}
	}
	if !next.Equal(now.Add(-2 * time.Minute)) {
		t.Fatalf("continue from the HWM: %v", next)
	}
	if srv.Count("POST /invoices/exports") != 1 || srv.Count("GET /invoices/ksef/") != 0 {
		t.Fatal(srv.Requests)
	}
}

func TestWindowsFromOldCursor(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	now := t0.AddDate(0, 7, 0)
	if _, _, err := c.exportRole(bg, "Subject1", t0, now); err != nil {
		t.Fatal(err)
	}
	if len(srv.Exports) != 3 {
		t.Fatalf("7 months in 90-day windows: %+v", srv.Exports)
	}
	for i, e := range srv.Exports[:2] {
		from, _ := time.Parse(time.RFC3339Nano, e.From)
		to, _ := time.Parse(time.RFC3339Nano, e.To)
		if to.Sub(from) > 90*24*time.Hour || (i > 0 && e.From != srv.Exports[i-1].To) {
			t.Fatalf("adjacent windows of at most 90 days: %+v", srv.Exports)
		}
	}
	if srv.Exports[2].To != "" {
		t.Fatal("the last window is open")
	}
}

func TestExportDedupAcrossTruncation(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.TruncateAt = 2
	addN(srv, "Subject1", 5, t0)
	c := testClient(srv)
	invs, _, err := c.exportRole(bg, "Subject1", t0.Add(-time.Hour), t0.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, inv := range invs {
		if seen[inv.meta.KsefNumber] {
			t.Fatalf("duplicate %s", inv.meta.KsefNumber)
		}
		seen[inv.meta.KsefNumber] = true
	}
	if len(seen) != 5 || srv.Count("POST /invoices/exports") < 3 {
		t.Fatalf("%d invoices, %d exports", len(seen), srv.Count("POST /invoices/exports"))
	}
}

func TestExportMultiPart(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.PartSize = 700
	addN(srv, "Subject2", 4, t0)
	c := testClient(srv)
	invs, _, err := c.exportRole(bg, "Subject2", t0.Add(-time.Hour), t0.Add(48*time.Hour))
	if err != nil || len(invs) != 4 || srv.Count("GET /storage/") < 2 {
		t.Fatalf("%d %v %v", len(invs), err, srv.Requests)
	}
}

func TestExport429(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Fail429 = 1
	c := testClient(srv)
	var waits []string
	c.OnWait = func(m string) { waits = append(waits, m) }
	if _, _, err := c.exportRole(bg, "Subject1", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || !strings.Contains(waits[0], "Rate limited by KSeF") {
		t.Fatal(waits)
	}
}

func TestExportFailed(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.ExportFail = "Błąd eksportu"
	_, _, err := testClient(srv).exportRole(bg, "Subject1", t0, t0.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "Błąd eksportu") || !strings.Contains(err.Error(), "Subject1") {
		t.Fatal(err)
	}
}

func TestExportDamaged(t *testing.T) {
	for name, set := range map[string]func(*kseftest.Server){
		"part": func(s *kseftest.Server) { s.DamagePart = true },
		"hash": func(s *kseftest.Server) { s.Add(kseftest.Invoice{Subject: "Subject1", Stored: t0, BadHash: true}) },
	} {
		srv := kseftest.New()
		addN(srv, "Subject1", 1, t0)
		set(srv)
		_, _, err := testClient(srv).exportRole(bg, "Subject1", t0.Add(-time.Hour), t0.Add(time.Hour))
		srv.Close()
		if err == nil || !(strings.Contains(err.Error(), "damaged") || strings.Contains(err.Error(), "does not match KSeF's hash")) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/ -run Export` — Expected: FAIL (undefined `exportRole`, `kseftest.Invoice`).

- [ ] **Step 3: Implement**

`kseftest`: `Add` fills defaults (KSeF number `<seller>-<yyyymmdd of Stored>-<12 hex of seq>-<2 hex>`, seller = `NIP` for `Subject1` else `5261040828`, buyer the other, amounts 100/23/123 PLN, `Number` `FA/<seq>/2026`, XML a small FA(3)-shaped document with a tab-indented body so byte-exactness is visible). `POST /invoices/exports` (needs access token): decrypt `encryption.encryptedSymmetricKey` with the server key, keep key+IV; when `Fail429 > 0` answer 429 with `Retry-After: 1` and decrement; record `ExportCall`; select invoices of `filters.subjectType` with `from <= Stored`, `Stored < to` (if `to`), `Stored <= HWM()`; sort by `Stored`; cut at `TruncateAt`; build a ZIP (`<ksef>.xml` each + `_metadata.json` `{"invoices":[…]}`) and AES-256-CBC/PKCS#7 encrypt it, split into `PartSize` chunks (default whole). `GET /invoices/exports/<ref>`: first poll `{"status":{"code":100,…}}`, then 200 with `package` (`invoiceCount`, `parts[]` with `url` = `<srv>/storage/<ref>/<n>`, `method` `GET`, `partHash`, `encryptedPartHash`, sizes; `isTruncated`, `lastPermanentStorageDate`, `permanentStorageHwmDate`), or `{"status":{"code":500,"description":ExportFail}}` when set. `GET /storage/<ref>/<n>`: the bytes (no auth); with `DamagePart` the last byte flipped. `BadHash` stores a wrong `invoiceHash`. `GET /invoices/ksef/<n>`: the XML with header `X-Ms-Meta-Hash`. `HWM` defaults to `time.Now`.

`export.go`:
```go
// window is the longest export range gfs asks for: KSeF allows 100 days.
const window = 90 * 24 * time.Hour

// apiInvoice is KSeF's InvoiceMetadata (export _metadata.json).
type apiInvoice struct {
	KsefNumber           string  `json:"ksefNumber"`
	InvoiceNumber        string  `json:"invoiceNumber"`
	IssueDate            string  `json:"issueDate"`
	InvoicingDate        string  `json:"invoicingDate"`
	AcquisitionDate      string  `json:"acquisitionDate"`
	PermanentStorageDate string  `json:"permanentStorageDate"`
	Seller               struct{ Nip, Name string } `json:"seller"`
	Buyer                struct {
		Identifier struct{ Type, Value string } `json:"identifier"`
		Name       string                       `json:"name"`
	} `json:"buyer"`
	NetAmount     float64 `json:"netAmount"`
	GrossAmount   float64 `json:"grossAmount"`
	VatAmount     float64 `json:"vatAmount"`
	Currency      string  `json:"currency"`
	InvoicingMode string  `json:"invoicingMode"`
	InvoiceType   string  `json:"invoiceType"`
	FormCode      struct{ SystemCode, SchemaVersion, Value string } `json:"formCode"`
	IsSelfInvoicing        bool   `json:"isSelfInvoicing"`
	HasAttachment          bool   `json:"hasAttachment"`
	InvoiceHash            string `json:"invoiceHash"`
	HashOfCorrectedInvoice string `json:"hashOfCorrectedInvoice"`
	ThirdSubjects          []struct {
		Identifier struct{ Type, Value string } `json:"identifier"`
		Name       string                       `json:"name"`
		Role       int                          `json:"role"`
	} `json:"thirdSubjects"`
}

type invoice struct {
	meta apiInvoice
	xml  []byte
}

type exportPage struct {
	invoices  []invoice
	truncated bool
	hwm, last string // permanentStorageHwmDate, lastPermanentStorageDate
}

type aesKey struct{ key, iv []byte }

func (k aesKey) decrypt(c []byte) ([]byte, error) {
	if len(c) == 0 || len(c)%aes.BlockSize != 0 {
		return nil, errors.New("not whole AES blocks")
	}
	b, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(c))
	cipher.NewCBCDecrypter(b, k.iv).CryptBlocks(out, c)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("bad padding")
	}
	return out[:len(out)-pad], nil
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// export runs one export (ksef spec §6.1) and returns its invoices, checked.
func (c *Client) export(ctx context.Context, subject string, from time.Time, to *time.Time) (exportPage, error) {
	where := fmt.Sprintf("export of %s from %s", subject, utc(from))
	fail := func(err error) (exportPage, error) { return exportPage{}, fmt.Errorf("%s: %w", where, err) }
	pub, err := c.key(ctx, "SymmetricKeyEncryption")
	if err != nil {
		return fail(err)
	}
	k := aesKey{make([]byte, 32), make([]byte, 16)}
	rand.Read(k.key)
	rand.Read(k.iv)
	encKey, err := encryptOAEP(pub, k.key)
	if err != nil {
		return fail(err)
	}
	dr := map[string]any{"dateType": "PermanentStorage", "from": utc(from), "restrictToPermanentStorageHwmDate": true}
	if to != nil {
		dr["to"] = utc(*to)
	}
	req := map[string]any{
		"encryption": map[string]string{"encryptedSymmetricKey": encKey, "initializationVector": base64.StdEncoding.EncodeToString(k.iv)},
		"filters":    map[string]any{"subjectType": subject, "dateRange": dr},
	}
	var started struct{ ReferenceNumber string }
	if err := c.json(ctx, http.MethodPost, "/invoices/exports", true, req, &started); err != nil {
		return fail(err)
	}
	var st struct {
		Status struct {
			Code        int
			Description string
			Details     []string
		}
		Package *struct {
			IsTruncated              bool
			LastPermanentStorageDate string
			PermanentStorageHwmDate  string
			Parts                    []struct {
				OrdinalNumber     int
				URL               string `json:"url"`
				Method            string
				PartHash          string
				EncryptedPartHash string
			}
		}
	}
	wait := time.Second
	for deadline := c.Now().Add(10 * time.Minute); ; {
		if err := c.json(ctx, http.MethodGet, "/invoices/exports/"+started.ReferenceNumber, true, nil, &st); err != nil {
			return fail(err)
		}
		if st.Status.Code == 200 {
			break
		}
		if st.Status.Code >= 400 {
			msg := st.Status.Description
			if len(st.Status.Details) > 0 {
				msg += ": " + strings.Join(st.Status.Details, "; ")
			}
			return fail(errors.New(msg))
		}
		if c.Now().After(deadline) {
			return fail(errors.New("did not finish in 10 minutes"))
		}
		if err := c.Sleep(ctx, wait); err != nil {
			return exportPage{}, err
		}
		wait = min(2*wait, 5*time.Second)
	}
	page := exportPage{}
	if st.Package == nil {
		return page, nil
	}
	page.truncated, page.hwm, page.last = st.Package.IsTruncated, st.Package.PermanentStorageHwmDate, st.Package.LastPermanentStorageDate
	if len(st.Package.Parts) == 0 {
		return page, nil
	}
	parts := st.Package.Parts
	sort.Slice(parts, func(i, j int) bool { return parts[i].OrdinalNumber < parts[j].OrdinalNumber })
	var zipped []byte
	for _, p := range parts {
		method := p.Method
		if method == "" {
			method = http.MethodGet
		}
		enc, err := c.raw(ctx, method, p.URL, false)
		if err != nil {
			return fail(err)
		}
		damaged := fmt.Errorf("package part %d is damaged", p.OrdinalNumber)
		if b64sha(enc) != p.EncryptedPartHash {
			return fail(damaged)
		}
		plain, err := k.decrypt(enc)
		if err != nil || b64sha(plain) != p.PartHash {
			return fail(damaged)
		}
		zipped = append(zipped, plain...)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return fail(errors.New("the package is not a ZIP: damaged"))
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return fail(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fail(err)
		}
		files[f.Name] = b
	}
	var meta struct{ Invoices []apiInvoice }
	if err := json.Unmarshal(files["_metadata.json"], &meta); err != nil {
		return fail(fmt.Errorf("_metadata.json: %w", err))
	}
	for _, m := range meta.Invoices {
		x, ok := files[m.KsefNumber+".xml"]
		if !ok {
			return fail(fmt.Errorf("invoice %s: missing from the package", m.KsefNumber))
		}
		if b64sha(x) != m.InvoiceHash {
			return fail(fmt.Errorf("invoice %s: content does not match KSeF's hash", m.KsefNumber))
		}
		page.invoices = append(page.invoices, invoice{meta: m, xml: x})
	}
	return page, nil
}

// exportRole reads every invoice of subject stored since from (ksef spec
// §6.2): 90-day windows, the last one open, truncated packages continued from
// their last storage date. next is where the following pull starts.
func (c *Client) exportRole(ctx context.Context, subject string, from, now time.Time) ([]invoice, time.Time, error) {
	var out []invoice
	seen := map[string]bool{}
	for {
		var to *time.Time
		if end := from.Add(window); end.Before(now) {
			to = &end
		}
		page, err := c.export(ctx, subject, from, to)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, inv := range page.invoices {
			if !seen[inv.meta.KsefNumber] {
				seen[inv.meta.KsefNumber] = true
				out = append(out, inv)
			}
		}
		switch {
		case page.truncated:
			last, err := time.Parse(time.RFC3339Nano, page.last)
			if err != nil || !last.After(from) {
				return nil, time.Time{}, fmt.Errorf("export of %s from %s: truncated without progress", subject, utc(from))
			}
			from = last
		case to != nil:
			from = *to
		default:
			if hwm, err := time.Parse(time.RFC3339Nano, page.hwm); err == nil && hwm.After(from) {
				from = hwm
			}
			return out, from, nil
		}
	}
}
```
(imports `archive/zip`, `bytes`, `context`, `crypto/aes`, `crypto/cipher`, `crypto/rand`, `encoding/base64`, `encoding/json`, `errors`, `fmt`, `io`, `net/http`, `sort`, `strings`, `time`.) Subject names in errors come from the caller; the session maps them back to role names in its own messages.

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -am "ksef: exports in windows, encrypted parts, hash checks; fake exports"` (add new files first).

### Task 6: metadata → `<invoice>`, schema, RELAX NG

**Files:**
- Create: `internal/adapter/ksef/xml.go`, `internal/adapter/ksef/schema.go`, `internal/adapter/ksef/relaxng.go`, `internal/adapter/ksef/xml_test.go`

**Interfaces:**
- Consumes: `apiInvoice`, `invoice` (Task 5)
- Produces:
  ```go
  var invoiceSchema *schema.Schema
  func RelaxNG() string
  func metaPath(role, ksef string) string    // _gfs/<role>/<yyyy>/<mm>/<ksef>.xml
  func origPath(role, ksef string) string    // <role>/<yyyy>/<mm>/<ksef>.xml
  func resourceOf(role string, inv invoice) adapter.Resource
  ```

- [ ] **Step 1: Write the failing test**

```go
package ksef

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestResourceOf(t *testing.T) {
	var m apiInvoice
	m.KsefNumber = "5261040828-20261003-0A12BC340000-7F"
	m.InvoiceNumber, m.InvoiceType, m.FormCode.SystemCode = "FA/123/10/2026", "Kor", "FA (3)"
	m.IssueDate, m.InvoicingDate, m.AcquisitionDate, m.PermanentStorageDate = "2026-10-03", "2026-10-03T08:12:30.5+00:00", "2026-10-03T08:12:30.6+00:00", "2026-10-03T08:12:31+00:00"
	m.InvoicingMode = "Online"
	m.Seller.Nip, m.Seller.Name = "5261040828", "ORLEN S.A."
	m.Buyer.Identifier.Type, m.Buyer.Identifier.Value, m.Buyer.Name = "Nip", "6887603807", "Acme sp. z o.o."
	m.Currency, m.NetAmount, m.VatAmount, m.GrossAmount = "PLN", 43.6, 10.03, 53.63
	m.HashOfCorrectedInvoice, m.InvoiceHash = "abc=", "qAdJ="
	m.IsSelfInvoicing = true
	r := resourceOf("received", invoice{meta: m, xml: []byte("<x/>")})
	if r.ID != "received/"+m.KsefNumber || r.Path != "_gfs/received/2026/10/"+m.KsefNumber+".xml" || r.Version != "qAdJ=" {
		t.Fatalf("%+v", r)
	}
	if string(r.Files["fa"]) != "<x/>" {
		t.Fatal("original bytes carried as attachment fa")
	}
	canon.Normalize(r.Root, invoiceSchema)
	got := xmltree.Print(r.Root, 0)
	for _, want := range []string{
		`<invoice id="received/5261040828-20261003-0A12BC340000-7F" ksef="5261040828-20261003-0A12BC340000-7F" role="received" version="qAdJ=" self-invoicing="true">`,
		`<number>FA/123/10/2026</number>`, `<type>Kor</type>`, `<form>FA (3)</form>`, `<issued>2026-10-03</issued>`,
		`<invoicing>2026-10-03T08:12:30.5Z</invoicing>`, `<stored>2026-10-03T08:12:31Z</stored>`, `<mode>online</mode>`,
		`<seller nip="5261040828">ORLEN S.A.</seller>`, `<buyer type="nip" id="6887603807">Acme sp. z o.o.</buyer>`,
		`<amounts currency="PLN" net="43.60" vat="10.03" gross="53.63"/>`, `<corrects hash="abc="/>`,
		`<original id="fa" name="5261040828-20261003-0A12BC340000-7F.xml" size="4" sha256="qAdJ="/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "has-attachment") || strings.Contains(got, "third-party") {
		t.Errorf("absent optionals stay absent:\n%s", got)
	}
}

func TestPaths(t *testing.T) {
	k := "6887603807-20261006-6CA868C00000-35"
	if metaPath("issued", k) != "_gfs/issued/2026/10/"+k+".xml" || origPath("issued", k) != "issued/2026/10/"+k+".xml" {
		t.Fatal(metaPath("issued", k), origPath("issued", k))
	}
}
```
Check `canon.Normalize`'s exact name/signature in `internal/canon` before writing (it is called as `canon.Normalize(c, s)` in `changes/actions.go`).

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/ -run 'ResourceOf|Paths'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`schema.go`:
```go
func ro(names ...string) []schema.Attr {
	out := make([]schema.Attr, len(names))
	for i, n := range names {
		out[i] = schema.Attr{Name: n, ReadOnly: true}
	}
	return out
}

// invoiceSchema is an <invoice> (ksef spec §5); all of it is KSeF's.
var invoiceSchema = &schema.Schema{
	Root: "invoice", ID: "id", Version: "version",
	RootAttrs: ro("id", "ksef", "role", "version", "self-invoicing", "has-attachment"),
	Elems: []schema.Elem{
		{Name: "number", Kind: schema.Field},
		{Name: "type", Kind: schema.Field},
		{Name: "form", Kind: schema.Field},
		{Name: "issued", Kind: schema.Field},
		{Name: "invoicing", Kind: schema.Field},
		{Name: "acquired", Kind: schema.Field},
		{Name: "stored", Kind: schema.Field},
		{Name: "mode", Kind: schema.Field},
		{Name: "seller", Kind: schema.Field, Attrs: ro("nip")},
		{Name: "buyer", Kind: schema.Field, Attrs: ro("type", "id")},
		{Name: "third-party", Kind: schema.Field, Repeated: true, SortKey: "id", Attrs: ro("type", "id", "role")},
		{Name: "amounts", Kind: schema.Field, Attrs: ro("currency", "net", "vat", "gross")},
		{Name: "corrects", Kind: schema.Field, Attrs: ro("hash")},
		{Name: "original", Kind: schema.Attachment, ID: "id", SortKey: "id", NameAttr: "name", Attrs: ro("id", "name", "size", "sha256")},
	},
}
```
The engine identity must be unique across roles (an invoice can be in two), so the root carries `id="<role>/<ksef>"` and `Schema.ID` is `id`; `ksef` is a plain attribute.

`xml.go`:
```go
const roleRoot = "_gfs"

// ksefMonth is <yyyy>/<mm> from the date inside a KSeF number
// (<nip>-<YYYYMMDD>-<technical>-<check>).
func ksefMonth(k string) string {
	parts := strings.Split(k, "-")
	if len(parts) < 2 || len(parts[1]) != 8 {
		return "unknown"
	}
	return parts[1][:4] + "/" + parts[1][4:6]
}

func origPath(role, k string) string { return role + "/" + ksefMonth(k) + "/" + k + ".xml" }
func metaPath(role, k string) string { return roleRoot + "/" + origPath(role, k) }

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], attrs[i+1])
		}
	}
	return n
}

func text(name, v string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	if v != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: v}}
	}
	return n
}

// stamp is a KSeF time in UTC, as short as it is exact.
func stamp(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func money(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// resourceOf is the metadata file of one invoice in one role, with its
// original's bytes as attachment fa (ksef spec §5).
func resourceOf(role string, inv invoice) adapter.Resource {
	m := inv.meta
	id := role + "/" + m.KsefNumber
	root := el("invoice", "id", id, "ksef", m.KsefNumber, "role", role, "version", m.InvoiceHash)
	if m.IsSelfInvoicing {
		root.SetAttr("self-invoicing", "true")
	}
	if m.HasAttachment {
		root.SetAttr("has-attachment", "true")
	}
	add := func(n *xmltree.Node) { root.Children = append(root.Children, n) }
	opt := func(name, v string) {
		if v != "" {
			add(text(name, v))
		}
	}
	opt("number", m.InvoiceNumber)
	opt("type", m.InvoiceType)
	opt("form", m.FormCode.SystemCode)
	opt("issued", m.IssueDate)
	opt("invoicing", stamp(m.InvoicingDate))
	opt("acquired", stamp(m.AcquisitionDate))
	opt("stored", stamp(m.PermanentStorageDate))
	opt("mode", strings.ToLower(m.InvoicingMode))
	add(text("seller", m.Seller.Name, "nip", m.Seller.Nip))
	add(text("buyer", m.Buyer.Name, "type", strings.ToLower(m.Buyer.Identifier.Type), "id", m.Buyer.Identifier.Value))
	for _, ts := range m.ThirdSubjects {
		add(text("third-party", ts.Name, "type", strings.ToLower(ts.Identifier.Type), "id", ts.Identifier.Value, "role", strconv.Itoa(ts.Role)))
	}
	add(el("amounts", "currency", m.Currency, "net", money(m.NetAmount), "vat", money(m.VatAmount), "gross", money(m.GrossAmount)))
	if m.HashOfCorrectedInvoice != "" {
		add(el("corrects", "hash", m.HashOfCorrectedInvoice))
	}
	add(el("original", "id", "fa", "name", m.KsefNumber+".xml", "size", strconv.Itoa(len(inv.xml)), "sha256", m.InvoiceHash))
	return adapter.Resource{ID: id, Version: m.InvoiceHash, Path: metaPath(role, m.KsefNumber), Root: root,
		Files: map[string][]byte{"fa": inv.xml}}
}
```
Text needing XML escaping: check how `xmltree.Print` escapes text and attributes; the DNS adapter passes text through `dnsx.XMLText` (removes characters XML cannot hold). Use the same: `dnsx.XMLText(v)` for names (company names may contain control characters).

`relaxng.go`: as `cloudns.RelaxNG`/`bitbucket.RelaxNG` but one root, from `invoiceSchema` (envelope or bare root; every element any attributes + text).

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "ksef: invoice metadata file, schema, grammar"`

### Task 7: session and adapter

**Files:**
- Create: `internal/adapter/ksef/session.go`, `internal/adapter/ksef/adapter.go`, `internal/adapter/ksef/session_test.go`

**Interfaces:**
- Consumes: Tasks 3–6
- Produces: `Adapter` (registered as `ksef`), implementing `adapter.Adapter`, `adapter.Normalizer`, `adapter.Placer`, `adapter.Loginer` (Task 8); `session` implementing `Session`, `Reporter`, `Identified`.
  ```go
  func newSession(t target) *session
  type cursor map[string]string // role -> continuation time (RFC 3339)
  ```

- [ ] **Step 1: Write the failing tests**

```go
package ksef

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func testSession(srv *kseftest.Server, rs ...string) *session {
	if len(rs) == 0 {
		rs = roles
	}
	s := newSession(target{host: "test.ksef.mf.gov.pl", nip: kseftest.NIP, base: srv.URL, token: srv.Token, since: "2026-02-01", roles: rs})
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func TestListAndCursor(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject1", 1, t0)
	addN(srv, "Subject2", 2, t0)
	s := testSession(srv)
	l, err := s.List(bg, "")
	if err != nil || len(l.Resources) != 3 || l.Full || len(l.Deleted) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
	var cur map[string]string
	if json.Unmarshal([]byte(l.Cursor), &cur) != nil || len(cur) != 3 {
		t.Fatalf("one continuation per role: %s", l.Cursor)
	}
	r := l.Resources[0]
	if !strings.HasPrefix(r.Path, "_gfs/") || r.Root == nil || len(r.Files["fa"]) == 0 {
		t.Fatalf("%+v", r)
	}
	n := srv.Count("POST /invoices/exports")
	addN(srv, "Subject2", 1, time.Now().Add(-time.Hour))
	l2, err := s.List(bg, l.Cursor)
	if err != nil || srv.Count("POST /invoices/exports")-n != 3 {
		t.Fatalf("incremental: one export per role: %v", err)
	}
	if len(l2.Resources) != 1 {
		t.Fatalf("only the new invoice: %d", len(l2.Resources))
	}
}

func TestListRolesNarrowed(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	s := testSession(srv, "received")
	l, err := s.List(bg, `{"issued":"2026-03-01T00:00:00Z","received":"2026-03-01T00:00:00Z"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.FullDirs, " ") != "_gfs/issued issued _gfs/third-party third-party" {
		t.Fatalf("roles outside the selection are full (and empty): %v", l.FullDirs)
	}
}

func TestListErrorKeepsNothing(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject1", 1, t0)
	srv.ExportFailFor = "Subject2"
	srv.ExportFail = "boom"
	l, err := testSession(srv).List(bg, "")
	if err == nil || len(l.Resources) != 0 || l.Cursor != "" || !strings.Contains(err.Error(), "received") {
		t.Fatalf("a failed role fails the listing and names the role: %+v %v", l, err)
	}
}

func TestCheckRefusesEverything(t *testing.T) {
	s := &session{}
	res := s.Check(bg, adapter.ApplyRequest{Actions: []adapter.Action{{Verb: "update"}, {Verb: "delete"}}})
	for _, r := range res {
		if r.Err == nil || r.Err.Error() != "KSeF invoices are read-only" {
			t.Fatal(res)
		}
	}
}

func TestDownload(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject2", 1, t0)
	s := testSession(srv)
	l, _ := s.List(bg, "")
	var b bytes.Buffer
	info, err := s.Download(bg, l.Resources[0].ID, "fa", &b)
	if err != nil || !bytes.Equal(b.Bytes(), l.Resources[0].Files["fa"]) || info.Size != int64(b.Len()) {
		t.Fatal(err)
	}
}

func TestPlacer(t *testing.T) {
	a := &Adapter{}
	k := "6887603807-20261006-6CA868C00000-35"
	if a.AttachmentPath(metaPath("issued", k), k+".xml") != origPath("issued", k) {
		t.Fatal(a.AttachmentPath(metaPath("issued", k), k+".xml"))
	}
	if r, ok := a.ResourceOf(origPath("issued", k)); !ok || r != metaPath("issued", k) {
		t.Fatal(r)
	}
	for _, p := range []string{"issued/2026/10/scan.pdf", "_gfs/issued/2026/10/" + k + ".xml", "other/2026/10/" + k + ".xml", k + ".xml"} {
		if _, ok := a.ResourceOf(p); ok {
			t.Error(p)
		}
	}
}
```
(`kseftest` gets `ExportFailFor string`: the subject `ExportFail` applies to; empty means all.)

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/ -run 'List|Check|Download|Placer'` — Expected: FAIL.

- [ ] **Step 3: Implement**

`adapter.go`:
```go
func init() { adapter.Register(&Adapter{}) }

// Adapter is ksef://<host>/<nip>.
type Adapter struct{}

var (
	_ adapter.Normalizer = (*Adapter)(nil)
	_ adapter.Placer     = (*Adapter)(nil)
)

func (*Adapter) Name() string                 { return "ksef" }
func (*Adapter) Schemes() []string            { return []string{"ksef"} }
func (*Adapter) Schema() *schema.Schema       { return invoiceSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

func (*Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	a.Class = a.Verb
	a.Detail = "KSeF invoices are read-only"
}

func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, os.Getenv, creds.Store{Dirs: creds.DefaultDirs()})
	if err != nil {
		return nil, err
	}
	return newSession(t), nil
}

// Placer (ksef spec §2.1, §4): metadata under _gfs/, originals beside in
// <role>/<yyyy>/<mm>/.
func (*Adapter) ResourceDir() string { return roleRoot }

func (*Adapter) AttachmentPath(resPath, name string) string {
	return path.Dir(strings.TrimPrefix(resPath, roleRoot+"/")) + "/" + name
}

var origRe = regexp.MustCompile(`^(issued|received|third-party)/[0-9]{4}/[0-9]{2}/[^/]+\.xml$`)

func (*Adapter) ResourceOf(p string) (string, bool) {
	if !origRe.MatchString(p) {
		return "", false
	}
	return roleRoot + "/" + p, true
}
```

`session.go`:
```go
var (
	_ adapter.Session    = (*session)(nil)
	_ adapter.Reporter   = (*session)(nil)
	_ adapter.Identified = (*session)(nil)
)

var errReadOnly = errors.New("KSeF invoices are read-only")

type session struct {
	c      *Client
	t      target
	report func(adapter.Progress)
	now    func() time.Time
}

func newSession(t target) *session {
	s := &session{c: NewClient(t.base, t.nip, t.token), t: t, report: func(adapter.Progress) {}, now: time.Now}
	s.c.LoginHint = loginHint(t.host, t.nip)
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *session) Identity() string                     { return "NIP " + s.t.nip }
func (s *session) Close() error                          { return nil }

// List exports every selected role from its cursor, or from since for a role
// the cursor lacks (ksef spec §6). Roles outside the selection are listed as
// full and empty so their files are removed.
func (s *session) List(ctx context.Context, cur string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	from := map[string]string{}
	if cur != "" {
		if err := json.Unmarshal([]byte(cur), &from); err != nil {
			from = map[string]string{}
		}
	}
	now := s.now()
	since, err := sinceTime(s.t.since, now)
	if err != nil {
		return adapter.Listing{}, err
	}
	l := adapter.Listing{}
	next := map[string]string{}
	for i, role := range s.t.roles {
		s.report(adapter.Progress{Phase: "pages", Done: i, Total: len(s.t.roles), Unit: "roles", Item: role})
		start := since
		if v, ok := from[role]; ok {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				start = t
			}
		}
		invs, n, err := s.c.exportRole(ctx, subjectOf[role], start, now)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("%s: %w", role, err)
		}
		for _, inv := range invs {
			l.Resources = append(l.Resources, resourceOf(role, inv))
		}
		next[role] = utc(n)
	}
	for _, role := range roles {
		if !slices.Contains(s.t.roles, role) {
			l.FullDirs = append(l.FullDirs, roleRoot+"/"+role, role)
		}
	}
	b, _ := json.Marshal(next)
	l.Cursor = string(b)
	return l, nil
}

// Fetch is never needed: listings carry full content (ksef spec §6.4).
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	return nil, fmt.Errorf("%s: %w (KSeF invoices are only listed)", id, adapter.ErrNotFound)
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := make([]adapter.Result, len(req.Actions))
	for i, a := range req.Actions {
		out[i] = adapter.Result{Action: a, Err: errReadOnly}
	}
	return out
}

func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	return s.Check(ctx, req)
}

// Download re-reads an original (gfs get), checked against its hash.
func (s *session) Download(ctx context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	_, k, ok := strings.Cut(resID, "/")
	if attID != "fa" || !ok {
		return adapter.AttachmentInfo{}, adapter.ErrNotFound
	}
	resp, data, err := s.c.send(ctx, http.MethodGet, "/invoices/ksef/"+url.PathEscape(k), true, nil, true)
	if err != nil {
		return adapter.AttachmentInfo{}, err
	}
	if h := resp.Header.Get("X-Ms-Meta-Hash"); h != "" && h != b64sha(data) {
		return adapter.AttachmentInfo{}, fmt.Errorf("invoice %s: content does not match KSeF's hash", k)
	}
	if _, err := w.Write(data); err != nil {
		return adapter.AttachmentInfo{}, err
	}
	return adapter.AttachmentInfo{Version: "-", Size: int64(len(data))}, nil
}
```
`Check`'s error text is the spec's message exactly. Version `"-"`: the schema's attachment kind has no `VersionAttr`, so `attach.Version` gives `"-"`, matching what `storeFiles` records.

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "ksef: session (exports per role, cursor, read-only) and Placer"`

### Task 8: `gfs auth login ksef://…`

**Files:**
- Create: `internal/adapter/ksef/login.go`, `internal/adapter/ksef/login_test.go`

**Interfaces:**
- Consumes: `normalize`, `NewClient`, `Client.access`, `Client.json`, `entryName` (Tasks 3–4); `adapter.LoginIO`, `adapter.LoginRefused`
- Produces: `func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error`; package var `storeEntry func(name, value string) error` (tests replace it).

- [ ] **Step 1: Write the failing test**

```go
package ksef

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func TestLoginStoresVerifiedToken(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	stored := map[string]string{}
	storeEntry = func(n, v string) error { stored[n] = v; return nil }
	t.Cleanup(func() { storeEntry = defaultStore })
	var out bytes.Buffer
	u, _ := url.Parse("ksef://test.ksef.mf.gov.pl/" + kseftest.NIP)
	io := adapter.LoginIO{Out: &out, ReadSecret: func(string) (string, error) { return srv.Token, nil },
		Flags: map[string]string{"base": srv.URL}}
	if err := (&Adapter{}).Login(bg, u, io); err != nil {
		t.Fatal(err, out.String())
	}
	if stored["ksef:test.ksef.mf.gov.pl/"+kseftest.NIP] != srv.Token || !strings.Contains(out.String(), "InvoiceRead") {
		t.Fatalf("%v\n%s", stored, out.String())
	}
	if srv.Count("POST /invoices/query/metadata") != 1 {
		t.Fatal("verified with one metadata query")
	}
	io.ReadSecret = func(string) (string, error) { return "wrong", nil }
	stored = map[string]string{}
	err := (&Adapter{}).Login(bg, u, io)
	var refused *adapter.LoginRefused
	if !errors.As(err, &refused) || len(stored) != 0 {
		t.Fatalf("a refused token stores nothing: %v", err)
	}
}
```
(`kseftest` answers `POST /invoices/query/metadata` with `{"hasMore":false,"isTruncated":false,"invoices":[]}`.)

- [ ] **Step 2: Run** `go test ./internal/adapter/ksef/ -run TestLoginStores` — Expected: FAIL.

- [ ] **Step 3: Implement** `login.go`:
```go
var _ adapter.Loginer = (*Adapter)(nil)

func defaultStore(name, value string) error {
	return creds.Store{Dirs: creds.DefaultDirs()}.SetEntry(name, value)
}

var storeEntry = defaultStore

// Login guides the user to a KSeF token, checks it and stores it under
// ksef:<host>/<nip> (ksef spec §7.1).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	nip := strings.Trim(n.Path, "/")
	base := apiBase[n.Host]
	if b := io.Flags["base"]; b != "" {
		base = b
	}
	if io.Flags["paste"] == "" {
		fmt.Fprintf(io.Out, `A KSeF token is generated in the KSeF taxpayer application (see https://ksef.podatki.gov.pl):
  1. log in with a trusted profile, e-signature or seal, in the context of NIP %s;
  2. generate a token with only the InvoiceRead permission ("Przeglądanie faktur");
  3. copy it: KSeF shows it once.
`, nip)
	}
	tok, err := io.ReadSecret(fmt.Sprintf("KSeF token for NIP %s: ", nip))
	if err != nil {
		return err
	}
	c := NewClient(base, nip, strings.TrimSpace(tok))
	c.LoginHint = loginHint(n.Host, nip)
	if _, err := c.access(ctx); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on ksef://%s/%s: %w", n.Host, nip, err)}
	}
	now := c.Now()
	q := map[string]any{"subjectType": "Subject1", "dateRange": map[string]any{"dateType": "PermanentStorage", "from": utc(now.Add(-24 * time.Hour))}}
	if err := c.json(ctx, http.MethodPost, "/invoices/query/metadata?pageSize=10", true, q, nil); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("reading invoices on ksef://%s/%s (does the token have InvoiceRead?): %w", n.Host, nip, err)}
	}
	fmt.Fprintf(io.Out, "verified: the token reads invoices of NIP %s\n", nip)
	if err := storeEntry(entryName(n.Host, nip), c.Token); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "stored in keyring: gfs %s\n", entryName(n.Host, nip))
	return nil
}
```
(`Client.json`'s `read` check compares the path; with a query string, match on `strings.HasPrefix(path, "/invoices/query/metadata")`.)

- [ ] **Step 4: Run** `go test ./internal/adapter/ksef/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "ksef: gfs auth login"`

### Task 9: CLI registration, schema, help docs

**Files:**
- Modify: `internal/cli/root.go` (blank import), `internal/cli/docs.go` (`schema ksef`, help topic), `embed.go` (`docs/ksef.md`)
- Create: `docs/ksef.md`
- Modify: `start.md` (adapter list, clone example, auth login line), `internal/cli/docs_test.go`

- [ ] **Step 1: Failing test** in `docs_test.go`:
```go
func TestKSeFDocs(t *testing.T) {
	mustContain(t, mustRun(t, 0, "schema", "ksef"), `<element name="original">`)
	mustContain(t, mustRun(t, 0, "help", "ksef"), "ksef://ksef.mf.gov.pl/<nip>", "20 exports an hour")
}
```
- [ ] **Step 2: Run** `go test ./internal/cli/ -run TestKSeFDocs` — Expected: FAIL.
- [ ] **Step 3: Implement**: `_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef" // registers ksef://` in `root.go`; `case "ksef": fmt.Fprint(…, ksef.RelaxNG())` and the usage text lists ksef; help topic `{Use: "ksef", Short: "KSeF e-invoices (Poland): URL, layout, tokens, limits; read-only", Long: doc("docs/ksef.md")}`; `embed.go` adds `docs/ksef.md`. `docs/ksef.md` covers, in the voice of `docs/bitbucket.md`: what is mirrored; the shell block (`gfs auth login ksef://ksef.mf.gov.pl/6887603807`, `gfs clone …`, `gfs pull`); URL table (§3); layout tree (§4) with "zip a month folder and send it"; the metadata file (§5) example; pull and limits (§6.5: a clone since 2026-02-01 ≈ 9 exports, a pull 3, at most 20 exports an hour so about 6 pulls; pull at most every 15 minutes; invoices appear ~2 minutes after KSeF accepts them); read-only and restoring (`pull --force` for metadata, `gfs get <original>` for an original, 64 per hour); getting a token (§7.1); no PDFs yet. `start.md`: KSeF in the adapter list line 3, a `# or: gfs clone ksef://ksef.mf.gov.pl/<nip>   # invoices, read-only` example, KSeF in the `gfs auth login` row and the help topic list.
- [ ] **Step 4: Run** `go test ./internal/cli/` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "cli: ksef adapter, schema and help"`

### Task 10: end-to-end tests

**Files:**
- Create: `internal/cli/ksef_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func TestKSeFCloneStatusPull(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject1", Stored: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)})
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)})
	t.Setenv("GFS_KSEF_TOKEN", srv.Token)
	dir := t.TempDir()
	t.Chdir(dir)
	remote := "ksef://test.ksef.mf.gov.pl/" + kseftest.NIP + "?base=" + url.QueryEscape(srv.URL)
	mustContain(t, mustRun(t, 0, "clone", remote), "Cloned 2 resources")
	t.Chdir(filepath.Join(dir, "ksef-test-"+kseftest.NIP))
	inv := srv.Invoices()
	orig := filepath.Join("issued", "2026", "03", inv[0].KsefNumber+".xml")
	b, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if base64.StdEncoding.EncodeToString(h[:]) != inv[0].Hash {
		t.Fatal("original byte-identical to KSeF")
	}
	meta := filepath.Join("_gfs", "issued", "2026", "03", inv[0].KsefNumber+".xml")
	mustContain(t, readFile(t, meta), `role="issued"`, `<original id="fa"`)
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// read-only: an edited original, an edited metadata file, a stray
	os.WriteFile(orig, append(b, ' '), 0o644)
	replaceIn(t, meta, "<number>", "<number>X")
	os.WriteFile(filepath.Join("received", "2026", "04", "scan.pdf"), []byte("x"), 0o644)
	st := mustRun(t, 0, "status")
	mustContain(t, st, "attachments of this resource are read-only", "KSeF invoices are read-only", "not part of the mirror")
	mustContain(t, mustRun(t, 1, "commit"), "FAIL")
	if srv.Count("POST /sessions") != 0 {
		t.Fatal("nothing is ever sent")
	}

	// restore: metadata from base, original with gfs get
	mustRun(t, 0, "pull", "--force")
	os.Remove(filepath.Join("received", "2026", "04", "scan.pdf"))
	os.Remove(orig)
	mustRun(t, 0, "get", orig)
	if b2, _ := os.ReadFile(orig); string(b2) != string(b) {
		t.Fatal("gfs get restores the original")
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// pull adds only new invoices, one export per role
	n := srv.Count("POST /invoices/exports")
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Now().Add(-time.Hour)})
	mustContain(t, mustRun(t, 0, "pull"), "+  _gfs/received/")
	if srv.Count("POST /invoices/exports")-n != 3 {
		t.Fatal("3 exports per pull")
	}
}
```
Adjust expected strings to the real `status`/`commit` output while making it pass, keeping each assertion's intent: originals exact, read-only refusals visible, strays flagged, nothing sent, restore works, pull adds only new. `readFile`/`replaceIn` exist in `cli_test.go` (check names; add `readFile` if missing). `kseftest.Invoices()` returns the stored invoices with `KsefNumber` and `Hash`.

- [ ] **Step 2: Run** `go test ./internal/cli/ -run TestKSeF -v` — fix what fails in the code under test, not the intent of the assertions.
- [ ] **Step 3: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 4: Commit** `git commit -m "ksef: end-to-end clone, status, restore, pull"`

### Task 11: real-site checks on TEST

**Files:**
- Create: `internal/adapter/ksef/realsite_test.go`, `internal/adapter/ksef/xades_test.go`, `internal/adapter/ksef/testdata/invoice-fa3.xml`

The XAdES helper lives in a `_test.go` file only (never in the adapter). It is the spike's code: self-signed personal certificate (`givenName`, `surname`, `serialNumber=TINPL-<nip>`, `CN`, `C=PL`), enveloped XAdES-BES built directly in exclusive-c14n form (SignedInfo with the root reference — enveloped + exc-c14n transforms — and the `#SignedProperties` reference), RSA-SHA256, posted to `/auth/xades-signature?verifyCertificateChain=false`, then the same status/redeem steps as `Client.Login`.

- [ ] **Step 1: Write the test** (skipped unless `GFS_KSEF_REALSITE=1`):
  1. random valid NIP; XAdES login → access token;
  2. `POST /tokens` `{"permissions":["InvoiceRead","InvoiceWrite"],"description":"gfs realsite"}`, poll `GET /tokens/<ref>` until `Active`;
  3. send one invoice through `POST /sessions/online` + `…/invoices` + close (AES-256-CBC, key under `SymmetricKeyEncryption`), wait for its KSeF number;
  4. `newSession` with the token and `roles=issued`, `since=-1d`; wait until the HWM passes the invoice (poll `List` at most every 30 s for 5 minutes); check the resource, its `Files["fa"]` hash = `invoiceHash`, metadata fields;
  5. send a second invoice, wait, `List(cursor)` returns exactly it;
  6. `Download` returns the same bytes.

  `testdata/invoice-fa3.xml` is our own minimal FA(3) invoice (seller `#nip#`, buyer `1111111111`, one line, totals consistent) written against `schemat_FA(3)_v1-0E.xsd` (namespace `http://crd.gov.pl/wzor/2025/06/25/13775/`, `KodFormularza kodSystemowy="FA (3)" wersjaSchemy="1-0E"`, `WariantFormularza 3`).
- [ ] **Step 2: Run** `GFS_KSEF_REALSITE=1 go test ./internal/adapter/ksef/ -run TestReal -v -timeout 15m` — Expected: PASS against TEST. Record findings (problem-details shape seen, timings) in spec §10.4.
- [ ] **Step 3: Commit** `git commit -m "ksef: real-site checks on TEST"`

### Task 12: spec touch-ups and final verification

**Files:**
- Modify: `docs/superpowers/specs/2026-10-09-ksef-invoices-design.md`

- [ ] **Step 1:** Align the spec with decisions made in this plan:
  - §2.1: strays are `!` "not part of the mirror; gfs ignores it" and fail `commit` like an orphan sidecar.
  - §5: root carries `id="<role>/<ksef>"` (the engine identity) before `ksef`; example updated.
  - §6.1: role exports run one after another (simpler; KSeF allows parallel, gfs does not need it).
  - §6.2: windows of at most 90 days.
  - §6.4 / §5: a deleted original is evicted (silent in `status`, attachments spec), restored by `gfs get`.
  - §9: errors are read in KSeF's default shapes (ExceptionResponse, problem+json, 429 status); no `X-Error-Format` header.
- [ ] **Step 2:** `gofmt -l .`, `go vet ./...`, `go test ./...` — all clean.
- [ ] **Step 3: Commit** `git commit -m "spec: KSeF as built"`
