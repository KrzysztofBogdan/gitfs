# gfs attachments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Attachment bytes fetched on demand with `gfs get`, tracked in `.gfs/attachments`, and synced both ways by `pull` and `commit`, in the shared core and in the Confluence adapter.

**Architecture:** A new schema kind `schema.Attachment` makes attachment metadata part of every resource file (canonical order, validation, merge). A new core package `internal/attach` owns sidecar paths (`X.files/` next to `X.xml`), derived file names, conflict-copy names and change detection. `workdir` stores tracking lines and streams files. `changes.Compute` adds per-attachment status (`AttChange`) and attachment actions to each `FileChange`. `engine` gains `Get`, and its commit, pull and resolve handle attachment actions, conflict copies and file placement. Adapters get a streaming `Session.Download` and handle attachment actions (`Action.File != ""`) in `Apply`. Confluence maps them to REST v2 list/download/delete and the v1 multipart upload endpoints.

**Tech Stack:** Go 1.25 (`go.mod` says `go 1.25.0`), standard library only (`crypto/sha256`, `mime/multipart`, `net/http`), `github.com/spf13/cobra` for the CLI.

**Spec:** `docs/superpowers/specs/2026-09-23-gfs-attachments-design.md` (read it before any task), which extends `docs/superpowers/specs/2026-09-23-gfs-cli-design.md`. Illustrations: `example/confluence/`, `example/mail/`, `example/slack/`, "Attachments" in `example/README.md`.

## Global Constraints

- Module path `github.com/KrzysztofBogdan/gitfs`; binary `gfs` from `./cmd/gfs`. Go toolchain `go 1.25.0`; this machine's system Go is older, so run every `go` command with `GOTOOLCHAIN=go1.25.0` exported.
- Run `gofmt -l .` (must print nothing) and `go vet ./...` before every commit. Struct literals of types from another package use keyed fields (`schema.Attr{Name: "id", ReadOnly: true}`), or `go vet` fails.
- Tests use the standard `testing` package only. No test talks to the network; Confluence is tested against `cftest` (an `httptest.Server`).
- Sidecar of `a/b/Name.xml` is `a/b/Name.files/`. Reserved suffix `.files`: a Confluence page title whose sanitised name ends in `.files` gets `_` appended.
- `.gfs/attachments` line, tab-separated, sorted by path: `<resource id>\t<attachment id>\t<version>\t<sha256 hex>\t<size>\t<mtime ns>\t<path>`. `version` is `-` for services without versions. A file younger than 2 seconds when its line is written gets mtime `0`.
- Duplicate names: sort a resource's attachment elements by id (numeric when both ids are numeric, else string order); the first keeps the name, later ones get ` (2)`, ` (3)` before the extension (`scan.pdf`, `scan (2).pdf`).
- Conflict copy of `dir/stem.ext` at remote version N: `dir/stem.remote-vN.ext` (`dir/stem.remote.ext` when the version is `-`).
- Attachment action targets: `attachment[id=<id>]` for existing attachments, `attachment[file=<file name>]` for new files. An action is an attachment action exactly when `Action.File != ""`.
- Commit order inside a resource: resource actions, then attachment creates and updates, then attachment deletes.
- Status letters for attachments: `A` new file, `M` changed locally, `D` element removed, `C` conflict, `!` refused. Unfetched, clean, evicted and remote-only changes are silent.
- `gfs get` output: `  +  <path>   <size>   v<N>`, `  =  <path>   up to date`, `  !  <path>   <reason>`, footer `<n> fetched, <n> up to date, <n> refused, <n> failed (<size>)`. Exit 1 if anything was refused or failed; `gfs get` without a path is a usage error (exit 2).
- Pull report lines for attachments: `  ~  <path>` refreshed, `  -  <path>   (deleted on remote)`, `  C  <path>   <reason>`, `  ~  <old> -> <new>` moved.

## Design decisions this plan makes

1. **`changes.Compute` keeps its signature** and loads `.gfs/attachments` from disk itself. Engine code holds `Env.Atts` in memory and saves it after every change, before anything calls `Compute` again.
2. **A resource whose XML is unchanged but whose attachments changed** is a `FileChange` with `Status: 'M'` and `Quiet: true`. Status prints only its attachment lines; commit runs only its attachment actions.
3. **A path filter that matches only attachments** of a resource (`gfs commit X.files/a.png`) keeps that resource as `Quiet` with only the matching attachment actions. This applies only when the resource's own status is `M`. For `A`, `R`, `D` the whole resource must be selected.
4. **Attachment conflicts at commit time** (remote version moved since the line) download the remote copy, skip that upload, and count a conflict. If nothing else is left to do for a `Quiet` resource, the remote resource is stored (a fast-forward, because the local XML equals base), so the element shows the new version and status reads `C`.
5. **Eviction** (bytes deleted, element kept) is silent in status. The line is dropped by the next commit write-back or pull of that resource (`placeAttachments`), not by a read-only command.
6. **Racy mtime:** tracking lines for files modified less than 2 s ago store mtime `0`, so the next check always hashes (same idea as git's racy index).
7. **Nested attachments** (Slack files inside messages) are declared in the contract through `PrefixAttr` but implemented only at the resource root, as the spec scopes it.

## File structure

```
internal/schema/schema.go            + Attachment kind, Elem.NameAttr/VersionAttr/Ops/PrefixAttr/MaxSize, Schema.Attachment(), Elem.Allows
internal/canon/canon.go              attachments sorted by SortKey then ID
internal/validate/validate.go        attachment rules
internal/merge/merge.go              attachments grouped by identity
internal/changes/actions.go          ResolveActions skips attachment elements
internal/engine/pull.go              StripReadOnly drops attachment elements
internal/workdir/attachments.go      AttEntry, Attachments, Load/SaveAttachments         (new)
internal/workdir/workdir.go          Open, Stat, WriteStream, ListSidecar, Sidecars; Scan skips *.files
internal/attach/attach.go            sidecar paths, Sanitize, Derive, Elements, Version, conflict copies  (new)
internal/attach/hash.go              Hash, Changed, Entry, LineMTime                      (new)
internal/adapter/adapter.go          Action.File, IsAttachment, ApplyRequest.Open/Files, Result.Version,
                                     AttachmentInfo, Session.Download, attachment target helpers
internal/adapter/fake/fake.go        attachments in the fake remote
internal/changes/attachments.go      AttChange, per-resource attachment status, orphans (new)
internal/changes/changes.go          Compute wires attachments in; FileChange.Attachments, Quiet
internal/engine/attach.go            download, placeAttachments, dropAttachments, forgetAttachment, Get (new)
internal/engine/commit.go            attachment conflicts, write-back sync, paths in report
internal/engine/pull.go              pullAttachments
internal/engine/resolve.go           resolveAttachment
internal/engine/engine.go            Env.Atts; Forget drops attachments
internal/engine/clone.go             Env.Atts initialised
internal/cli/env.go                  loads .gfs/attachments
internal/cli/get.go                  gfs get                                             (new)
internal/cli/status.go, diff.go      attachment lines, binary notice
internal/cli/root.go                 registers get
internal/adapter/confluence/cftest/server.go   attachment endpoints, multipart, redirect, fault injection
internal/adapter/confluence/client.go          apiError, download, upload (streaming)
internal/adapter/confluence/schema.go          <attachment> element
internal/adapter/confluence/convert.go         apiAttachment, attachmentNodes
internal/adapter/confluence/session.go         Fetch lists attachments, Download, Apply/Check attachment actions
internal/adapter/confluence/adapter.go         Describe attachment actions
internal/adapter/confluence/paths.go           .files suffix reserved
```

---

### Task 1: `schema.Attachment` and the XML-side rules

The attachment element is part of the resource's canonical content. This task makes the existing XML machinery understand it: canonical order, validation, merge by identity, no implicit actions from the XML diff (Task 5 adds attachment actions from the sidecar state instead), and no attachment elements in re-created content.

**Files:**
- Modify: `internal/schema/schema.go`, `internal/canon/canon.go:79-94`, `internal/validate/validate.go:66-88`, `internal/merge/merge.go:36`, `internal/changes/actions.go:58-65`, `internal/engine/pull.go:230-243`
- Test: `internal/canon/attachment_test.go`, `internal/validate/attachment_test.go`, `internal/merge/attachment_test.go`, `internal/engine/strip_test.go` (all new)

**Interfaces:**
- Produces (package `schema`):
  - `const Attachment Kind` (after `Sub`)
  - `Elem` fields `NameAttr, VersionAttr string; Ops []string; PrefixAttr string; MaxSize int64`
  - `func (s *Schema) Attachment() *Elem` (root-level attachment element or nil)
  - `func (e *Elem) Allows(op string) bool`
- Behaviour relied on later: `canon.Normalize` sorts attachment elements by `SortKey`, then `ID`; `validate.Resource` rejects attachment elements without `ID`, with an unknown `ID`, with changed read-only attributes, or with content; `merge.Merge` keys attachments by `ID`; `changes.ResolveActions` never emits actions for attachment elements; `engine.StripReadOnly` removes attachment elements.

- [ ] **Step 1: Write the failing tests**

`internal/canon/attachment_test.go`:

```go
package canon

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true},
				{Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

func TestAttachmentsSortedByKeyThenID(t *testing.T) {
	n, err := xmltree.ParseString(`<page><attachment created="2" id="b" name="y"/><attachment version="1" id="c" name="z" created="1"/><title>T</title><attachment id="a" name="x" created="2"/></page>`)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(n, attSchema)
	want := "<page>\n  <title>T</title>\n  <attachment id=\"c\" name=\"z\" version=\"1\" created=\"1\"/>\n  <attachment id=\"a\" name=\"x\" created=\"2\"/>\n  <attachment id=\"b\" name=\"y\" created=\"2\"/>\n</page>"
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
```

`internal/validate/attachment_test.go`:

```go
package validate

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
	},
}

func TestAttachmentRules(t *testing.T) {
	ref := p(t, `<page><attachment id="a1" name="x.png" version="2"/></page>`)
	cases := []struct{ name, in, wantErr string }{
		{"unchanged", `<page><attachment id="a1" name="x.png" version="2"/></page>`, ""},
		{"removed is fine", `<page/>`, ""},
		{"new element without id", `<page><attachment name="y.png"/></page>`, "new attachments are created by adding a file"},
		{"unknown id", `<page><attachment id="zz" name="x.png"/></page>`, "attachment[id=zz]: no such attachment on the remote"},
		{"read-only changed", `<page><attachment id="a1" name="x.png" version="3"/></page>`, `attachment[id=a1]: read-only attribute "version" changed`},
		{"content", `<page><attachment id="a1" name="x.png" version="2">data</attachment></page>`, "attachment[id=a1]: must be empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Resource(p(t, c.in), ref, attSchema)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
```

`internal/merge/attachment_test.go`:

```go
package merge

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var attSchema = &schema.Schema{
	Root: "page", ID: "id",
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "id", NameAttr: "name", VersionAttr: "version",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
	},
}

func TestAttachmentsMergeByIdentity(t *testing.T) {
	base := p(t, `<page><title>T</title><attachment id="a1" name="x" version="1"/></page>`)
	local := p(t, `<page><title>Local</title><attachment id="a1" name="x" version="1"/></page>`)
	remote := p(t, `<page><title>T</title><attachment id="a1" name="x" version="2"/><attachment id="a2" name="y" version="1"/></page>`)
	res, err := Merge(base, local, remote, attSchema, "remote v2")
	if err != nil || res.Conflicted() {
		t.Fatalf("%+v %v", res, err)
	}
	got := xmltree.Print(res.Root, 0)
	for _, want := range []string{"<title>Local</title>", `<attachment id="a1" name="x" version="2"/>`, `<attachment id="a2" name="y" version="1"/>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
```

`internal/engine/strip_test.go`:

```go
package engine

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestStripReadOnlyDropsAttachments(t *testing.T) {
	s := &schema.Schema{
		Root: "note", ID: "id", Version: "version",
		RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
		Elems: []schema.Elem{
			{Name: "title", Kind: schema.Field},
			{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name",
				Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}}},
		},
	}
	root, err := xmltree.ParseString(`<note id="1" version="2"><title>T</title><attachment id="a1" name="x.png"/></note>`)
	if err != nil {
		t.Fatal(err)
	}
	StripReadOnly(root, s)
	if got := xmltree.Print(root, 0); got != "<note>\n  <title>T</title>\n</note>" {
		t.Fatalf("got\n%s", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/canon/ ./internal/validate/ ./internal/merge/ ./internal/engine/ -run 'Attachment|Strip'`
Expected: FAIL, `undefined: schema.Attachment`.

- [ ] **Step 3: Implement**

`internal/schema/schema.go`: add the kind, the fields and two helpers. The file becomes:

```go
// Package schema declares the shape of an adapter's resource root.
package schema

import "slices"

type Kind int

const (
	Field      Kind = iota // scalar text element (Repeated: may occur several times)
	List                   // container of repeated Item elements
	Body                   // native service content, passed through
	Sub                    // sub-resource with identity attribute ID
	Attachment             // metadata of binary content whose bytes live in the sidecar folder
)

type Attr struct {
	Name     string
	ReadOnly bool // written by gfs from the service; a local change is a commit error
}

type Elem struct {
	Name      string
	Kind      Kind
	Repeated  bool     // Field only
	Item      string   // List only: item element name
	Sorted    bool     // List only: items are unordered, sort by text
	Attrs     []Attr   // declared attributes in canonical order
	ID        string   // Sub, Attachment: identity attribute
	SortKey   string   // Sub, Attachment: attribute to sort by
	Children  []Elem   // Sub only: declared nested elements (e.g. reply)
	BodyTypes []string // Body only: allowed values of the type attribute

	NameAttr    string   // Attachment: attribute holding the service's file name
	VersionAttr string   // Attachment: version attribute, "" if the service has none
	Ops         []string // Attachment: allowed operations (create, update, delete); none = read-only
	PrefixAttr  string   // Attachment: attribute of the parent element prepended to file names
	MaxSize     int64    // Attachment: largest upload in bytes, 0 = no limit
}

type Schema struct {
	Root      string
	RootAttrs []Attr
	ID        string // identity attribute of the root
	Version   string // version attribute of the root, "" if none
	Elems     []Elem // root children in canonical order
}

func Find(elems []Elem, name string) *Elem {
	for i := range elems {
		if elems[i].Name == name {
			return &elems[i]
		}
	}
	return nil
}

func AttrDecl(attrs []Attr, name string) (Attr, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a, true
		}
	}
	return Attr{}, false
}

// Attachment returns the root-level attachment element, or nil if the
// adapter has no attachments.
func (s *Schema) Attachment() *Elem {
	for i := range s.Elems {
		if s.Elems[i].Kind == Attachment {
			return &s.Elems[i]
		}
	}
	return nil
}

// Allows reports whether an attachment kind permits op (create, update, delete).
func (e *Elem) Allows(op string) bool { return slices.Contains(e.Ops, op) }
```

`internal/canon/canon.go`: in `normalizeChildren`, replace the final grouping loop (currently lines 79-94) with a version that also sorts attachments and breaks ties by identity:

```go
	var out []*xmltree.Node
	for i, g := range groups {
		kind := elems[i].Kind
		if (kind == schema.Sub || kind == schema.Attachment) && elems[i].SortKey != "" {
			key, idAttr := elems[i].SortKey, elems[i].ID
			sort.SliceStable(g, func(a, b int) bool {
				ka, oka := g[a].Attr(key)
				kb, okb := g[b].Attr(key)
				if oka != okb {
					return oka // keyed before unkeyed
				}
				if !oka {
					return false
				}
				if ka != kb || kind != schema.Attachment {
					return ka < kb
				}
				ia, _ := g[a].Attr(idAttr)
				ib, _ := g[b].Attr(idAttr)
				return ia < ib // attachments: stable order even when timestamps tie
			})
		}
		out = append(out, g...)
	}
	parent.Children = append(out, unknown...)
```

Attachment elements need no other normalisation: `orderAttrs(c, e.Attrs)` already runs for every declared element. `schema.Attachment` falls through the `switch e.Kind`.

`internal/validate/validate.go`: in `children`, add a case to the second `switch e.Kind` (after `case schema.Sub:`):

```go
		case schema.Attachment:
			errs = append(errs, attachment(c, ref, e)...)
```

and add the function below `sub`:

```go
// attachment checks one attachment element (attachments spec 3.1): it must name
// an existing attachment, keep its service-owned attributes and stay empty.
func attachment(c, parentRef *xmltree.Node, e *schema.Elem) []error {
	id, ok := c.Attr(e.ID)
	if !ok {
		return []error{fmt.Errorf("<%s> without %s: new attachments are created by adding a file to the sidecar folder", c.Name, e.ID)}
	}
	label := fmt.Sprintf("%s[id=%s]", c.Name, id)
	ref := FindSub(parentRef, c.Name, e.ID, id)
	if ref == nil {
		return []error{fmt.Errorf("%s: no such %s on the remote", label, c.Name)}
	}
	errs := readOnly(label, c, ref, e.Attrs)
	if len(c.Elements()) > 0 || strings.TrimSpace(c.TextContent()) != "" {
		errs = append(errs, fmt.Errorf("%s: must be empty", label))
	}
	return errs
}
```

`internal/merge/merge.go`, in `group` (line 36): key attachments by identity like subs:

```go
		if e := schema.Find(s.Elems, c.Name); e != nil && (e.Kind == schema.Sub || e.Kind == schema.Attachment) {
```

`internal/changes/actions.go`, in `ResolveActions`, at the top of the `for i := range s.Elems` loop body, after `e := &s.Elems[i]`:

```go
			if e.Kind == schema.Attachment {
				continue // attachment actions come from the sidecar state (changes/attachments.go)
			}
```

`internal/engine/pull.go`: replace `stripSubs` with a version that also drops attachment elements (they cannot be re-created from metadata):

```go
func stripSubs(n *xmltree.Node, elems []schema.Elem) {
	var kept []*xmltree.Node
	for _, c := range n.Children {
		if c.Kind == xmltree.Element {
			if e := schema.Find(elems, c.Name); e != nil && e.Kind == schema.Attachment {
				continue // attachments cannot be re-created from metadata
			}
		}
		kept = append(kept, c)
	}
	n.Children = kept
	for _, c := range n.Elements() {
		e := schema.Find(elems, c.Name)
		if e == nil || e.Kind != schema.Sub {
			continue
		}
		for _, a := range e.Attrs {
			if a.ReadOnly || a.Name == e.ID {
				c.DelAttr(a.Name)
			}
		}
		stripSubs(c, e.Children)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS. `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/schema internal/canon internal/validate internal/merge internal/changes/actions.go internal/engine/pull.go internal/engine/strip_test.go
git commit -m "schema: Attachment kind; canon, validate, merge and actions understand it"
```

---

### Task 2: `workdir` — tracking file, streaming writes, sidecar listing

**Files:**
- Create: `internal/workdir/attachments.go`
- Modify: `internal/workdir/workdir.go` (Scan, writeAtomic, new helpers)
- Test: `internal/workdir/attachments_test.go` (new)

**Interfaces:**
- Produces:
  - `type AttEntry struct{ ResID, AttID, Version, SHA string; Size, MTime int64; Path string }`
  - `type Attachments struct{...}`; `func NewAttachments() *Attachments`; methods `Get(resID, attID string) (AttEntry, bool)`, `ByPath(p string) (AttEntry, bool)`, `ForResource(resID string) []AttEntry` (sorted by path), `Put(e AttEntry)`, `Delete(resID, attID string)`, `All() []AttEntry` (sorted by path)
  - `func (t *Tree) LoadAttachments() (*Attachments, error)` (missing file = empty), `func (t *Tree) SaveAttachments(a *Attachments) error`
  - `func (t *Tree) Open(rel string) (*os.File, error)`, `Stat(rel string) (os.FileInfo, error)`, `WriteStream(rel string, fill func(io.Writer) error) error`
  - `func (t *Tree) ListSidecar(dir string) (files, dirs []string, err error)`: direct children of a tree-relative dir, as tree-relative paths, sorted. Temp files `.gfs-tmp-*` are skipped; a missing dir is empty.
  - `func (t *Tree) Sidecars() ([]string, error)`: every directory whose name ends in `.files`, outside `.gfs/`, sorted.
  - `Scan` no longer descends into directories whose name ends in `.files`.

- [ ] **Step 1: Write the failing tests** `internal/workdir/attachments_test.go`

```go
package workdir

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestAttachmentsRoundTrip(t *testing.T) {
	tr := newTree(t)
	a, err := tr.LoadAttachments()
	if err != nil || len(a.All()) != 0 {
		t.Fatalf("missing file must load empty: %v %v", a, err)
	}
	a.Put(AttEntry{ResID: "98130", AttID: "att2", Version: "3", SHA: "bb", Size: 7, MTime: 11, Path: "eng/R.files/b b.png"})
	a.Put(AttEntry{ResID: "98130", AttID: "att1", Version: "-", SHA: "aa", Size: 5, MTime: 0, Path: "eng/R.files/a.pdf"})
	a.Put(AttEntry{ResID: "7", AttID: "x", Version: "1", SHA: "cc", Size: 1, MTime: 1, Path: "z.files/x"})
	if err := tr.SaveAttachments(a); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(tr.Root, ".gfs", "attachments"))
	want := "98130\tatt1\t-\taa\t5\t0\teng/R.files/a.pdf\n98130\tatt2\t3\tbb\t7\t11\teng/R.files/b b.png\n7\tx\t1\tcc\t1\t1\tz.files/x\n"
	if string(raw) != want {
		t.Fatalf("got\n%q\nwant\n%q", raw, want)
	}
	b, err := tr.LoadAttachments()
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := b.Get("98130", "att2"); !ok || e.Path != "eng/R.files/b b.png" || e.Size != 7 || e.MTime != 11 {
		t.Fatalf("%+v", e)
	}
	if e, ok := b.ByPath("eng/R.files/a.pdf"); !ok || e.AttID != "att1" {
		t.Fatalf("%+v", e)
	}
	if got := b.ForResource("98130"); len(got) != 2 || got[0].AttID != "att1" {
		t.Fatalf("%+v", got)
	}
	b.Delete("98130", "att1")
	if _, ok := b.Get("98130", "att1"); ok {
		t.Fatal("Delete")
	}
	os.WriteFile(filepath.Join(tr.Root, ".gfs", "attachments"), []byte("bad line\n"), 0o644)
	if _, err := tr.LoadAttachments(); err == nil {
		t.Fatal("malformed line must fail")
	}
}

func TestSidecarsAndScan(t *testing.T) {
	tr := newTree(t)
	must(t, tr.WriteFile("eng/R.xml", []byte("x")))
	must(t, tr.WriteFile("eng/R.files/a.png", []byte("a")))
	must(t, tr.WriteFile("eng/R.files/sub/b.png", []byte("b")))
	must(t, tr.WriteFile("eng/R.files/.gfs-tmp-123", []byte("t")))
	must(t, tr.WriteFile("eng/R/child.xml", []byte("c")))
	files, err := tr.Scan()
	if err != nil || !slices.Equal(files, []string{"eng/R.xml", "eng/R/child.xml"}) {
		t.Fatalf("Scan must skip sidecars: %v %v", files, err)
	}
	fs, ds, err := tr.ListSidecar("eng/R.files")
	if err != nil || !slices.Equal(fs, []string{"eng/R.files/a.png"}) || !slices.Equal(ds, []string{"eng/R.files/sub"}) {
		t.Fatalf("%v %v %v", fs, ds, err)
	}
	if fs, ds, err := tr.ListSidecar("nope.files"); err != nil || fs != nil || ds != nil {
		t.Fatalf("missing dir must be empty: %v %v %v", fs, ds, err)
	}
	if sc, err := tr.Sidecars(); err != nil || !slices.Equal(sc, []string{"eng/R.files"}) {
		t.Fatalf("%v %v", sc, err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestWriteStreamDoesNotBuffer(t *testing.T) {
	tr := newTree(t)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := tr.WriteStream("big.files/blob.bin", func(w io.Writer) error {
		_, err := io.CopyN(w, zeros{}, 64<<20)
		return err
	})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 8<<20 {
		t.Fatalf("WriteStream allocated %d MB for a 64 MB file", d>>20)
	}
	st, err := tr.Stat("big.files/blob.bin")
	if err != nil || st.Size() != 64<<20 {
		t.Fatalf("%v %v", st, err)
	}
	f, err := tr.Open("big.files/blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
```

(`newTree` and `must` already exist in `workdir_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/workdir/`
Expected: FAIL, `tr.LoadAttachments undefined`.

- [ ] **Step 3: Implement**

`internal/workdir/attachments.go`:

```go
package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// AttEntry is one fetched attachment (attachments spec 3.3): the version and
// content last synced, and where its bytes are now.
type AttEntry struct {
	ResID, AttID, Version, SHA string
	Size, MTime                int64
	Path                       string
}

// Attachments is the set of fetched attachments, keyed by resource and attachment id.
type Attachments struct{ byKey map[string]AttEntry }

func NewAttachments() *Attachments { return &Attachments{byKey: map[string]AttEntry{}} }

func attKey(resID, attID string) string { return resID + "\x00" + attID }

func (a *Attachments) Get(resID, attID string) (AttEntry, bool) {
	e, ok := a.byKey[attKey(resID, attID)]
	return e, ok
}

func (a *Attachments) ByPath(p string) (AttEntry, bool) {
	for _, e := range a.byKey {
		if e.Path == p {
			return e, true
		}
	}
	return AttEntry{}, false
}

func (a *Attachments) ForResource(resID string) []AttEntry {
	var out []AttEntry
	for _, e := range a.All() {
		if e.ResID == resID {
			out = append(out, e)
		}
	}
	return out
}

func (a *Attachments) Put(e AttEntry)             { a.byKey[attKey(e.ResID, e.AttID)] = e }
func (a *Attachments) Delete(resID, attID string) { delete(a.byKey, attKey(resID, attID)) }

func (a *Attachments) All() []AttEntry {
	out := make([]AttEntry, 0, len(a.byKey))
	for _, e := range a.byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (t *Tree) LoadAttachments() (*Attachments, error) {
	a := NewAttachments()
	data, err := os.ReadFile(t.gfs("attachments"))
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 7)
		if len(f) != 7 {
			return nil, fmt.Errorf(".gfs/attachments line %d: want 7 tab-separated fields", n)
		}
		size, err1 := strconv.ParseInt(f[4], 10, 64)
		mtime, err2 := strconv.ParseInt(f[5], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf(".gfs/attachments line %d: size and mtime must be integers", n)
		}
		a.Put(AttEntry{ResID: f[0], AttID: f[1], Version: f[2], SHA: f[3], Size: size, MTime: mtime, Path: f[6]})
	}
	return a, sc.Err()
}

func (t *Tree) SaveAttachments(a *Attachments) error {
	var b bytes.Buffer
	for _, e := range a.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", e.ResID, e.AttID, e.Version, e.SHA, e.Size, e.MTime, e.Path)
	}
	return writeAtomic(t.gfs("attachments"), b.Bytes())
}
```

`internal/workdir/workdir.go`:

1. Add `"io"` to the imports.
2. Add the file helpers after `Exists`:

```go
func (t *Tree) Open(rel string) (*os.File, error)     { return os.Open(t.Abs(rel)) }
func (t *Tree) Stat(rel string) (os.FileInfo, error) { return os.Stat(t.Abs(rel)) }

// WriteStream writes rel atomically from fill, creating parent directories.
// Nothing is buffered in memory beyond what fill itself holds.
func (t *Tree) WriteStream(rel string, fill func(io.Writer) error) error {
	return writeAtomicFunc(t.Abs(rel), fill)
}

// ListSidecar returns the regular files and the subdirectories directly inside
// the tree-relative dir, as tree-relative paths, sorted. Temp files of an
// interrupted write are skipped; a missing dir is empty.
func (t *Tree) ListSidecar(dir string) (files, dirs []string, err error) {
	ents, err := os.ReadDir(t.Abs(dir))
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		p := dir + "/" + e.Name()
		switch {
		case e.IsDir():
			dirs = append(dirs, p)
		case e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".gfs-tmp-"):
			files = append(files, p)
		}
	}
	return files, dirs, nil
}

// Sidecars returns every directory named *.files outside .gfs/, sorted.
func (t *Tree) Sidecars() ([]string, error) {
	var out []string
	err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == t.Root {
			return nil
		}
		if p == filepath.Join(t.Root, Dir) {
			return filepath.SkipDir
		}
		if strings.HasSuffix(d.Name(), ".files") {
			r, _ := filepath.Rel(t.Root, p)
			out = append(out, filepath.ToSlash(r))
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
```

3. In `Scan`, skip sidecars. Replace the directory check with:

```go
		if d.IsDir() && (p == filepath.Join(t.Root, Dir) || (p != t.Root && strings.HasSuffix(d.Name(), ".files"))) {
			return filepath.SkipDir
		}
```

4. Replace `writeAtomic` with a streaming core:

```go
func writeAtomic(path string, data []byte) error {
	return writeAtomicFunc(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func writeAtomicFunc(path string, fill func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gfs-tmp-*")
	if err != nil {
		return err
	}
	if err := fill(f); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/workdir/ -v && go vet ./... && gofmt -l .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/workdir
git commit -m "workdir: .gfs/attachments, streaming writes, sidecar listing"
```

---

### Task 3: `attach` — sidecar paths, names, conflict copies, change detection

Pure functions shared by `changes` and `engine`. No adapter knowledge.

**Files:**
- Create: `internal/attach/attach.go`, `internal/attach/hash.go`
- Test: `internal/attach/attach_test.go`

**Interfaces:**
- Consumes: `schema.Elem` fields from Task 1, `workdir.Tree` and `workdir.AttEntry` from Task 2.
- Produces:
  - `const SidecarSuffix = ".files"`
  - `func SidecarDir(resPath string) string` (`a/X.xml` → `a/X.files`)
  - `func ResourceOf(p string) (string, bool)` (`a/X.files/f` → `a/X.xml`, true)
  - `func Sanitize(name string) string`
  - `func Derive(root *xmltree.Node, e *schema.Elem, resPath string) map[string]string` (attachment id → sidecar path)
  - `func Elements(root *xmltree.Node, e *schema.Elem) map[string]*xmltree.Node` (by id; nil root → empty)
  - `func Version(el *xmltree.Node, e *schema.Elem) string` (`"-"` when the kind or the element has no version)
  - `func ConflictCopy(p, version string) string`, `func ConflictOriginal(name string) (string, bool)`
  - `func Hash(t *workdir.Tree, rel string) (sha string, size, mtime int64, err error)`
  - `func Changed(t *workdir.Tree, rel string, line workdir.AttEntry) (bool, error)`
  - `func LineMTime(st os.FileInfo) int64` (0 for files younger than 2 s)
  - `func Entry(t *workdir.Tree, resID, attID, version, rel string) (workdir.AttEntry, error)`

- [ ] **Step 1: Write the failing tests** `internal/attach/attach_test.go`

```go
package attach

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var elem = &schema.Elem{Name: "attachment", Kind: schema.Attachment, ID: "id", NameAttr: "name", VersionAttr: "version"}

func TestPaths(t *testing.T) {
	if got := SidecarDir("eng/Home/Runbooks.xml"); got != "eng/Home/Runbooks.files" {
		t.Fatal(got)
	}
	if res, ok := ResourceOf("eng/Home/Runbooks.files/a b.png"); !ok || res != "eng/Home/Runbooks.xml" {
		t.Fatal(res, ok)
	}
	if _, ok := ResourceOf("eng/Home/Runbooks.xml"); ok {
		t.Fatal("a resource file is not inside a sidecar")
	}
	for in, want := range map[string]string{"a/b.png": "a-b.png", ".hidden": "_hidden", " x\ty ": "x y", "": "attachment", "..": "attachment"} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveDuplicatesAndPrefix(t *testing.T) {
	root, _ := xmltree.ParseString(`<mail><attachment id="10" name="scan.pdf"/><attachment id="3" name="scan.pdf"/><attachment id="4" name="SCAN.pdf"/><attachment id="5" name="a/b.txt"/><attachment name="new.txt"/></mail>`)
	got := Derive(root, elem, "inbox/Invoice.xml")
	want := map[string]string{
		"3":  "inbox/Invoice.files/scan.pdf",
		"4":  "inbox/Invoice.files/SCAN (2).pdf",
		"5":  "inbox/Invoice.files/a-b.txt",
		"10": "inbox/Invoice.files/scan (3).pdf",
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for id, p := range want {
		if got[id] != p {
			t.Errorf("%s: got %q want %q", id, got[id], p)
		}
	}
	pre := &schema.Elem{Name: "file", Kind: schema.Attachment, ID: "id", NameAttr: "name", PrefixAttr: "ts"}
	msg, _ := xmltree.ParseString(`<message ts="1758096060.000300"><file id="F1" name="graph.png"/></message>`)
	if p := Derive(msg, pre, "general/2026-09-17.xml")["F1"]; p != "general/2026-09-17.files/1758096060.000300-graph.png" {
		t.Fatal(p)
	}
	if len(Derive(nil, elem, "x.xml")) != 0 {
		t.Fatal("nil root")
	}
}

func TestElementsAndVersion(t *testing.T) {
	root, _ := xmltree.ParseString(`<p><attachment id="a" version="3"/><attachment id="b"/></p>`)
	els := Elements(root, elem)
	if len(els) != 2 || Version(els["a"], elem) != "3" || Version(els["b"], elem) != "-" {
		t.Fatalf("%v", els)
	}
	noVer := &schema.Elem{Name: "attachment", ID: "id"}
	if Version(els["a"], noVer) != "-" {
		t.Fatal("kind without versions")
	}
}

func TestConflictCopies(t *testing.T) {
	if got := ConflictCopy("eng/Home.files/logo.svg", "4"); got != "eng/Home.files/logo.remote-v4.svg" {
		t.Fatal(got)
	}
	if got := ConflictCopy("m.files/README", "-"); got != "m.files/README.remote" {
		t.Fatal(got)
	}
	for name, want := range map[string]string{"logo.remote-v4.svg": "logo.svg", "README.remote": "README", "a.b.remote-v12.tar": "a.b.tar"} {
		if got, ok := ConflictOriginal(name); !ok || got != want {
			t.Errorf("ConflictOriginal(%q) = %q %v, want %q", name, got, ok, want)
		}
	}
	if _, ok := ConflictOriginal("logo.svg"); ok {
		t.Fatal("not a conflict copy")
	}
}

func TestHashChangedEntry(t *testing.T) {
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("r.files/a.txt", []byte("abc"))
	line, err := Entry(tr, "1", "a1", "2", "r.files/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if line.SHA != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || line.Size != 3 || line.Version != "2" || line.Path != "r.files/a.txt" {
		t.Fatalf("%+v", line)
	}
	if line.MTime != 0 {
		t.Fatal("a file written just now must get mtime 0 (racy)")
	}
	if ch, err := Changed(tr, "r.files/a.txt", line); err != nil || ch {
		t.Fatalf("same bytes: %v %v", ch, err)
	}
	tr.WriteFile("r.files/a.txt", []byte("xyz"))
	if ch, _ := Changed(tr, "r.files/a.txt", line); !ch {
		t.Fatal("same size, different bytes must be changed")
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(tr.Abs("r.files/a.txt"), old, old)
	st, _ := tr.Stat("r.files/a.txt")
	if LineMTime(st) != old.UnixNano() {
		t.Fatal("old files keep their mtime")
	}
	fast := workdir.AttEntry{SHA: "wrong", Size: 3, MTime: old.UnixNano()}
	if ch, _ := Changed(tr, "r.files/a.txt", fast); ch {
		t.Fatal("equal size and mtime must skip hashing")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/attach/`
Expected: FAIL, `no non-test Go files` / `undefined: SidecarDir`.

- [ ] **Step 3: Implement**

`internal/attach/attach.go`:

```go
// Package attach holds the shared attachment rules (attachments spec 3, 5):
// sidecar paths, derived file names, conflict copies and change detection.
package attach

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// SidecarSuffix names the folder next to a resource file that holds its attachments.
const SidecarSuffix = ".files"

// SidecarDir returns the sidecar of a resource file: a/X.xml -> a/X.files.
func SidecarDir(resPath string) string { return strings.TrimSuffix(resPath, ".xml") + SidecarSuffix }

// ResourceOf returns the resource file owning a path directly inside a sidecar.
func ResourceOf(p string) (string, bool) {
	dir := path.Dir(p)
	if dir == "." || !strings.HasSuffix(dir, SidecarSuffix) {
		return "", false
	}
	return strings.TrimSuffix(dir, SidecarSuffix) + ".xml", true
}

// Sanitize makes a service file name safe as a single path segment.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" || s == "." || s == ".." {
		return "attachment"
	}
	if s[0] == '.' {
		s = "_" + s[1:] // no hidden files, no clash with .gfs-tmp-*
	}
	return s
}

// Elements returns the attachment elements of root by identity.
func Elements(root *xmltree.Node, e *schema.Elem) map[string]*xmltree.Node {
	out := map[string]*xmltree.Node{}
	if root == nil || e == nil {
		return out
	}
	for _, c := range root.ChildrenNamed(e.Name) {
		if id, ok := c.Attr(e.ID); ok {
			out[id] = c
		}
	}
	return out
}

// Version returns an attachment element's version, or "-" when there is none.
func Version(el *xmltree.Node, e *schema.Elem) string {
	if e.VersionAttr == "" {
		return "-"
	}
	if v, ok := el.Attr(e.VersionAttr); ok && v != "" {
		return v
	}
	return "-"
}

// Derive maps attachment id to sidecar path for every attachment element of
// root (attachments spec 3.2): sanitised name, optional prefix from the parent,
// de-duplicated in id order.
func Derive(root *xmltree.Node, e *schema.Elem, resPath string) map[string]string {
	out := map[string]string{}
	if root == nil || e == nil {
		return out
	}
	type item struct{ id, name string }
	var items []item
	for _, c := range root.ChildrenNamed(e.Name) {
		id, ok := c.Attr(e.ID)
		if !ok {
			continue
		}
		raw, _ := c.Attr(e.NameAttr)
		name := Sanitize(raw)
		if e.PrefixAttr != "" {
			if v, ok := root.Attr(e.PrefixAttr); ok && v != "" {
				name = v + "-" + name
			}
		}
		items = append(items, item{id, name})
	}
	sort.SliceStable(items, func(i, j int) bool { return idLess(items[i].id, items[j].id) })
	dir := SidecarDir(resPath)
	used := map[string]int{}
	for _, it := range items {
		key := strings.ToLower(it.name)
		used[key]++
		name := it.name
		if n := used[key]; n > 1 {
			name = numbered(name, n)
		}
		out[it.id] = dir + "/" + name
	}
	return out
}

func numbered(name string, n int) string {
	ext := path.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}

// idLess orders numeric ids numerically and anything else as strings.
func idLess(a, b string) bool {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// ConflictCopy names the remote copy written next to a conflicted attachment
// (attachments spec 5.3): stem.remote-v<N>.ext, or stem.remote.ext without versions.
func ConflictCopy(p, version string) string {
	ext := path.Ext(p)
	tag := ".remote"
	if version != "" && version != "-" {
		tag += "-v" + version
	}
	return strings.TrimSuffix(p, ext) + tag + ext
}

var copyRe = regexp.MustCompile(`^(.*)\.remote(?:-v[^.]*)?(\.[^.]*)?$`)

// ConflictOriginal returns the file name a conflict copy belongs to.
func ConflictOriginal(name string) (string, bool) {
	m := copyRe.FindStringSubmatch(name)
	if m == nil || m[1] == "" {
		return "", false
	}
	return m[1] + m[2], true
}
```

`internal/attach/hash.go`:

```go
package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// racyWindow: a file modified this recently may change again within the same
// mtime tick, so its tracking line does not trust mtime (attachments spec 3.3).
const racyWindow = 2 * time.Second

// Hash returns the sha256 (hex), size and mtime (Unix ns) of a working file.
func Hash(t *workdir.Tree, rel string) (string, int64, int64, error) {
	f, err := t.Open(rel)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), st.ModTime().UnixNano(), nil
}

// LineMTime is the mtime to record for a file: its own, or 0 while it is racy.
func LineMTime(st os.FileInfo) int64 {
	if time.Since(st.ModTime()) < racyWindow {
		return 0
	}
	return st.ModTime().UnixNano()
}

// Changed reports whether the file at rel differs from what line synced.
// Equal size and mtime mean unchanged without reading the file.
func Changed(t *workdir.Tree, rel string, line workdir.AttEntry) (bool, error) {
	st, err := t.Stat(rel)
	if err != nil {
		return false, err
	}
	if line.MTime != 0 && st.Size() == line.Size && st.ModTime().UnixNano() == line.MTime {
		return false, nil
	}
	sha, _, _, err := Hash(t, rel)
	if err != nil {
		return false, err
	}
	return sha != line.SHA, nil
}

// Entry builds the tracking line for the file at rel as it is now.
func Entry(t *workdir.Tree, resID, attID, version, rel string) (workdir.AttEntry, error) {
	sha, size, _, err := Hash(t, rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	st, err := t.Stat(rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	return workdir.AttEntry{ResID: resID, AttID: attID, Version: version, SHA: sha, Size: size, MTime: LineMTime(st), Path: rel}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/attach/ -v && go vet ./... && gofmt -l .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/attach
git commit -m "attach: sidecar paths, derived names, conflict copies, change detection"
```

---
### Task 4: Adapter contract and attachments in the fake remote

**Files:**
- Modify: `internal/adapter/adapter.go`, `internal/adapter/fake/fake.go` (replaced whole)
- Create: `internal/adapter/confluence/attachments.go` (temporary `Download` so the package compiles; Task 11 replaces the file)
- Test: `internal/adapter/attachment_test.go`, `internal/adapter/fake/attachment_test.go` (new)

**Interfaces:**
- Produces (package `adapter`):
  - `Action.File string`; `func (a Action) IsAttachment() bool`
  - `ApplyRequest.Open func(rel string) (io.ReadCloser, error)`; `ApplyRequest.Files []string`
  - `Result.Version string`
  - `type AttachmentInfo struct{ Version string; Size int64 }`
  - `Session.Download(ctx context.Context, resourceID, attachmentID string, w io.Writer) (AttachmentInfo, error)`
  - `func AttachmentTarget(elem, id string) string` → `elem[id=<id>]`; `func NewAttachmentTarget(elem, file string) string` → `elem[file=<file>]`; `func ParseAttachmentTarget(t string) (id, file string, ok bool)`
- Produces (package `fake`):
  - `Schema` gains `<attachment id name size version created>` (all read-only, ops create/update/delete, sort key `created`)
  - `Remote.Downloads int`; `FailVerb["download"]` fails `Download`
  - `Remote.PutAttachment(resID, attID, name string, data []byte)`, `EditAttachment(resID, attID string, data []byte)`, `RenameAttachment(resID, attID, name string)`, `DeleteAttachment(resID, attID string)`, `Attachment(resID, attID string) ([]byte, int, bool)`, `AttachmentNamed(resID, name string) (string, bool)`
  - `func (a *Adapter) ReadOnlyAttachments()`: this adapter's attachment kind allows nothing; the package `Schema` is untouched
  - Apply: attachment actions read `req.Open(a.File)`. Create names the attachment `path.Base(a.File)` with id `a<n>` at version 1; update bumps the version; delete removes it. Attachment actions never bump the resource version.

- [ ] **Step 1: Write the failing tests**

`internal/adapter/attachment_test.go`:

```go
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
```

`internal/adapter/fake/attachment_test.go`:

```go
package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestFakeAttachments(t *testing.T) {
	a := New()
	a.Remote.Put("1", "n.xml", `<note><title>T</title></note>`)
	a.Remote.PutAttachment("1", "a1", "x.png", []byte("abc"))
	ctx := context.Background()
	s, _ := a.Open(ctx, nil, nil)
	res, err := s.Fetch(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got := xmltree.Print(res.Root, 0); !strings.Contains(got, `<attachment id="a1" name="x.png" size="3" version="1" created="2026-01-01T00:00:00Z"/>`) {
		t.Fatalf("element missing:\n%s", got)
	}
	var buf bytes.Buffer
	info, err := s.Download(ctx, "1", "a1", &buf)
	if err != nil || buf.String() != "abc" || info.Version != "1" || info.Size != 3 || a.Remote.Downloads != 1 {
		t.Fatalf("%+v %v %q", info, err, buf.String())
	}
	files := map[string]string{"n.files/x.png": "abcd", "n.files/new.csv": "a,b"}
	open := func(rel string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(files[rel])), nil }
	out := s.Apply(ctx, adapter.ApplyRequest{Local: res, Base: res, Lock: "1", Open: open, Actions: []adapter.Action{
		{Verb: "update", Target: "attachment[id=a1]", File: "n.files/x.png"},
		{Verb: "create", Target: "attachment[file=new.csv]", File: "n.files/new.csv"},
	}})
	if len(out) != 2 || out[0].Err != nil || out[0].Version != "2" || out[1].Err != nil || out[1].ID == "" || out[1].Version != "1" {
		t.Fatalf("%+v", out)
	}
	if data, v, _ := a.Remote.Attachment("1", "a1"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
	if id, ok := a.Remote.AttachmentNamed("1", "new.csv"); !ok || id != out[1].ID {
		t.Fatal(id, ok)
	}
	after, _ := s.Fetch(ctx, "1")
	if after.Version != "1" {
		t.Fatal("attachment changes must not bump the resource version")
	}
	out = s.Apply(ctx, adapter.ApplyRequest{Local: after, Base: after, Lock: "1",
		Actions: []adapter.Action{{Verb: "delete", Target: "attachment[id=a1]", File: "n.files/x.png"}}})
	if out[0].Err != nil {
		t.Fatalf("%+v", out)
	}
	if _, _, ok := a.Remote.Attachment("1", "a1"); ok {
		t.Fatal("not deleted")
	}
	if _, err := s.Download(ctx, "1", "a1", &buf); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	a.ReadOnlyAttachments()
	if a.Schema().Attachment().Allows("create") || !Schema.Attachment().Allows("create") {
		t.Fatal("ReadOnlyAttachments must change this adapter only")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/...`
Expected: FAIL, `undefined: AttachmentTarget`.

- [ ] **Step 3: Implement**

`internal/adapter/adapter.go`: add `"io"` and `"regexp"` to the imports, then make these changes.

In `Action`, add the field after `Params`:

```go
	File     string // attachment actions: sidecar path of the bytes; "" for resource actions
```

After the `Action` type:

```go
// IsAttachment reports whether a acts on an attachment (attachments spec 4.4).
func (a Action) IsAttachment() bool { return a.File != "" }

var attTargetRe = regexp.MustCompile(`^[\w:.-]+\[(?:id=([^\]]+)|file=(.+))\]$`)

// AttachmentTarget names an existing attachment: attachment[id=att9].
func AttachmentTarget(elem, id string) string { return elem + "[id=" + id + "]" }

// NewAttachmentTarget names a new attachment by its file: attachment[file=a.png].
func NewAttachmentTarget(elem, file string) string { return elem + "[file=" + file + "]" }

// ParseAttachmentTarget returns the attachment id, or the file name of a new one.
func ParseAttachmentTarget(t string) (id, file string, ok bool) {
	m := attTargetRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
```

In `ApplyRequest`, add after `IDByPath`:

```go
	Open     func(rel string) (io.ReadCloser, error) // reads the sidecar file named by Action.File
	Files    []string                                // explicit verbs: the resource's sidecar files
```

In `Result`, add after `Detail`:

```go
	Version string // attachment create/update: the attachment's new version
```

After `Result`:

```go
// AttachmentInfo describes downloaded attachment bytes.
type AttachmentInfo struct {
	Version string // "-" when the service has no versions
	Size    int64
}
```

In `Session`, add after `Check`:

```go
	Download(ctx context.Context, resourceID, attachmentID string, w io.Writer) (AttachmentInfo, error)
```

`internal/adapter/confluence/attachments.go` (temporary; Task 11 replaces this file):

```go
package confluence

import (
	"context"
	"errors"
	"io"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// Download is replaced by the real implementation in Task 11 of the attachments plan.
func (s *session) Download(context.Context, string, string, io.Writer) (adapter.AttachmentInfo, error) {
	return adapter.AttachmentInfo{}, errors.New("confluence: attachments are not supported yet")
}
```

`internal/adapter/fake/fake.go` (whole file):

```go
// Package fake is an in-memory adapter for engine tests.
package fake

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var Schema = &schema.Schema{
	Root: "note", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "tags", Kind: schema.List, Item: "tag", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type"}}, BodyTypes: []string{"text/plain"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Ops: []string{"create", "update", "delete"},
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "size", ReadOnly: true},
				{Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

type attachment struct {
	id, name, created string
	data              []byte
	version           int
}

type record struct {
	path    string
	root    *xmltree.Node // never holds attachment elements; resource() adds them
	version int
	by      string
	atts    []*attachment
}

type Remote struct {
	FailVerb     map[string]error // keyed by verb, or verb+" "+target; "download" fails Download
	LockFailures int              // Apply returns ErrLock this many times
	Calls        []string         // "verb path" per executed action ("verb file" for attachments)
	Published    []string         // "path channel"
	Downloads    int              // successful Download calls
	recs         map[string]*record
	seq          int
}

// next returns a fresh id, skipping ids already taken by Put.
func (r *Remote) next() string {
	for {
		r.seq++
		id := strconv.Itoa(r.seq)
		if _, taken := r.recs[id]; !taken {
			return id
		}
	}
}

func parse(x string) *xmltree.Node {
	n, err := xmltree.ParseString(x)
	if err != nil {
		panic(err)
	}
	return n
}

// Put stores a resource at version 1 (xml is the <note> root, without id/version).
func (r *Remote) Put(id, path, x string) {
	r.recs[id] = &record{path: path, root: parse(x), version: 1, by: "alice"}
}

func (r *Remote) Edit(id string, f func(root *xmltree.Node)) {
	rec := r.recs[id]
	f(rec.root)
	rec.version++
	rec.by = "bob"
}

func (r *Remote) Delete(id string)     { delete(r.recs, id) }
func (r *Remote) Move(id, path string) { r.recs[id].path = path; r.recs[id].version++ }

// PutAttachment adds an attachment to resource resID at version 1.
func (r *Remote) PutAttachment(resID, attID, name string, data []byte) {
	rec := r.recs[resID]
	rec.atts = append(rec.atts, &attachment{id: attID, name: name, created: "2026-01-01T00:00:00Z", data: data, version: 1})
}

// EditAttachment replaces an attachment's bytes on the remote and bumps its version.
func (r *Remote) EditAttachment(resID, attID string, data []byte) {
	a := findAtt(r.recs[resID], attID)
	a.data, a.version = data, a.version+1
}

func (r *Remote) RenameAttachment(resID, attID, name string) { findAtt(r.recs[resID], attID).name = name }

func (r *Remote) DeleteAttachment(resID, attID string) {
	rec := r.recs[resID]
	rec.atts = slices.DeleteFunc(rec.atts, func(a *attachment) bool { return a.id == attID })
}

// Attachment returns an attachment's bytes and version.
func (r *Remote) Attachment(resID, attID string) ([]byte, int, bool) {
	a := findAtt(r.recs[resID], attID)
	if a == nil {
		return nil, 0, false
	}
	return a.data, a.version, true
}

// AttachmentNamed returns the id of resID's attachment called name.
func (r *Remote) AttachmentNamed(resID, name string) (string, bool) {
	if rec := r.recs[resID]; rec != nil {
		for _, a := range rec.atts {
			if a.name == name {
				return a.id, true
			}
		}
	}
	return "", false
}

func findAtt(rec *record, id string) *attachment {
	if rec == nil {
		return nil
	}
	for _, a := range rec.atts {
		if a.id == id {
			return a
		}
	}
	return nil
}

func (r *Remote) Get(id string) (*adapter.Resource, bool) {
	rec, ok := r.recs[id]
	if !ok {
		return nil, false
	}
	return r.resource(id, rec), true
}

func (r *Remote) resource(id string, rec *record) *adapter.Resource {
	root := rec.root.Clone()
	root.SetAttr("id", id)
	root.SetAttr("version", strconv.Itoa(rec.version))
	for _, a := range rec.atts {
		root.Children = append(root.Children, &xmltree.Node{Kind: xmltree.Element, Name: "attachment", Attrs: []xmltree.Attr{
			{Name: "id", Value: a.id}, {Name: "name", Value: a.name}, {Name: "size", Value: strconv.Itoa(len(a.data))},
			{Name: "version", Value: strconv.Itoa(a.version)}, {Name: "created", Value: a.created}}})
	}
	return &adapter.Resource{ID: id, Version: strconv.Itoa(rec.version), Path: rec.path,
		By: rec.by, At: "2026-09-23T12:00:00Z", Root: root}
}

type Adapter struct {
	Remote *Remote
	sch    *schema.Schema
}

func New() *Adapter { return &Adapter{Remote: &Remote{recs: map[string]*record{}}, sch: Schema} }

// ReadOnlyAttachments makes this adapter's attachment kind allow no operation.
func (a *Adapter) ReadOnlyAttachments() {
	s := *Schema
	s.Elems = slices.Clone(Schema.Elems)
	for i := range s.Elems {
		if s.Elems[i].Kind == schema.Attachment {
			s.Elems[i].Ops = nil
		}
	}
	a.sch = &s
}

// Schema is the adapter's schema: the package Schema unless ReadOnlyAttachments changed it.
func (a *Adapter) Schema() *schema.Schema {
	if a.sch == nil {
		return Schema
	}
	return a.sch
}

func (*Adapter) Name() string                 { return "fake" }
func (*Adapter) Schemes() []string            { return []string{"fake"} }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Tree }
func (*Adapter) DefaultDir(*url.URL) string   { return "fake" }
func (*Adapter) Verbs() []adapter.Verb {
	return []adapter.Verb{{Name: "publish", Class: "publish", Help: "publish the note to a channel", Params: []string{"channel"}}}
}

func (*Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	a.Class = a.Verb
	switch {
	case a.Target != "":
		a.Detail = a.Verb + " " + a.Target
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	case a.Verb == "move":
		a.Detail = "move"
	default:
		a.Detail = a.Verb + " note"
	}
}

func (a *Adapter) Open(context.Context, *url.URL, map[string]string) (adapter.Session, error) {
	return session{a.Remote}, nil
}

type session struct{ r *Remote }

func (s session) Close() error { return nil }

func (s session) List(context.Context, string) (adapter.Listing, error) {
	l := adapter.Listing{Full: true}
	for id, rec := range s.r.recs {
		l.Resources = append(l.Resources, *s.r.resource(id, rec))
	}
	return l, nil
}

func (s session) Fetch(_ context.Context, id string) (*adapter.Resource, error) {
	res, ok := s.r.Get(id)
	if !ok {
		return nil, adapter.ErrNotFound
	}
	return res, nil
}

func (s session) Download(_ context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	if err := s.r.FailVerb["download"]; err != nil {
		return adapter.AttachmentInfo{}, err
	}
	a := findAtt(s.r.recs[resID], attID)
	if a == nil {
		return adapter.AttachmentInfo{}, adapter.ErrNotFound
	}
	n, err := w.Write(a.data)
	if err == nil {
		s.r.Downloads++
	}
	return adapter.AttachmentInfo{Version: strconv.Itoa(a.version), Size: int64(n)}, err
}

func (s session) Check(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var out []adapter.Result
	for _, a := range req.Actions {
		out = append(out, adapter.Result{Action: a, Err: s.fault(a)})
	}
	return out
}

func (s session) fault(a adapter.Action) error {
	if err := s.r.FailVerb[a.Verb+" "+a.Target]; err != nil {
		return err
	}
	return s.r.FailVerb[a.Verb]
}

var targetRe = regexp.MustCompile(`^(\w+)\[(?:id=([^\]]+)|(\d+))\]$`)

func (s session) Apply(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var id string
	var rec *record
	if req.Base != nil {
		id = req.Base.ID
		rec = s.r.recs[id]
		if rec == nil {
			return []adapter.Result{{Action: req.Actions[0], Err: adapter.ErrNotFound, Code: "404"}}
		}
		if s.r.LockFailures > 0 || (req.Lock != "" && req.Lock != strconv.Itoa(rec.version)) {
			if s.r.LockFailures > 0 {
				s.r.LockFailures--
			}
			return []adapter.Result{{Action: req.Actions[0], Err: fmt.Errorf("fake: %w", adapter.ErrLock), Code: "409"}}
		}
	}
	var out []adapter.Result
	changed := false
	for _, a := range req.Actions {
		res := adapter.Result{Action: a}
		if err := s.fault(a); err != nil {
			res.Err, res.Code = err, "500"
			out = append(out, res)
			continue
		}
		if a.IsAttachment() {
			s.r.Calls = append(s.r.Calls, a.Verb+" "+a.File)
			out = append(out, s.applyAttachment(rec, req, a))
			continue // attachment changes do not bump the resource version
		}
		s.r.Calls = append(s.r.Calls, a.Verb+" "+req.Local.Path)
		switch {
		case a.Verb == "create" && a.Target == "":
			id = s.r.next()
			root := req.Local.Root.Clone()
			for _, c := range root.ChildrenNamed("comment") {
				c.SetAttr("id", "c"+s.r.next())
				c.SetAttr("created", "2026-09-23T12:00:00Z")
			}
			root.DelAttr("id")
			root.DelAttr("version")
			root.Children = slices.DeleteFunc(root.Children, func(c *xmltree.Node) bool {
				return c.Kind == xmltree.Element && c.Name == "attachment"
			})
			rec = &record{path: req.Local.Path, root: root, version: 0, by: "me"}
			s.r.recs[id] = rec
			res.ID = id
		case a.Verb == "delete" && a.Target == "":
			delete(s.r.recs, id)
			out = append(out, res)
			return out
		case a.Verb == "move":
			rec.path = a.To
		case a.Verb == "update" && a.Target == "":
			replaceGroup(rec.root, req.Local.Root, a.Group)
		case a.Target != "":
			s.applySub(rec.root, req.Local.Root, a)
		case a.Verb == "publish":
			s.r.Published = append(s.r.Published, req.Local.Path+" "+a.Params["channel"])
		}
		changed = true
		out = append(out, res)
	}
	if changed && rec != nil {
		rec.version++
		rec.by = "me"
	}
	return out
}

func (s session) applyAttachment(rec *record, req adapter.ApplyRequest, a adapter.Action) adapter.Result {
	res := adapter.Result{Action: a}
	if rec == nil {
		res.Err, res.Code = adapter.ErrNotFound, "404"
		return res
	}
	read := func() ([]byte, error) {
		rc, err := req.Open(a.File)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	id, _, _ := adapter.ParseAttachmentTarget(a.Target)
	switch a.Verb {
	case "create":
		data, err := read()
		if err != nil {
			res.Err = err
			return res
		}
		n := &attachment{id: "a" + s.r.next(), name: path.Base(a.File), created: "2026-09-23T12:00:00Z", data: data, version: 1}
		rec.atts = append(rec.atts, n)
		res.ID, res.Version = n.id, "1"
	case "update":
		x := findAtt(rec, id)
		if x == nil {
			res.Err, res.Code = adapter.ErrNotFound, "404"
			return res
		}
		data, err := read()
		if err != nil {
			res.Err = err
			return res
		}
		x.data, x.version = data, x.version+1
		res.ID, res.Version = x.id, strconv.Itoa(x.version)
	case "delete":
		if findAtt(rec, id) == nil {
			res.Err, res.Code = adapter.ErrNotFound, "404"
			return res
		}
		rec.atts = slices.DeleteFunc(rec.atts, func(x *attachment) bool { return x.id == id })
	default:
		res.Err = fmt.Errorf("fake: no attachment action %q", a.Verb)
	}
	return res
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}

func (s session) applySub(dst, src *xmltree.Node, a adapter.Action) {
	m := targetRe.FindStringSubmatch(a.Target)
	if m == nil {
		return
	}
	name, id, nth := m[1], m[2], m[3]
	switch a.Verb {
	case "create":
		n, _ := strconv.Atoi(nth)
		i := 0
		for _, c := range src.ChildrenNamed(name) {
			if _, has := c.Attr("id"); has {
				continue
			}
			if i++; i == n {
				nc := c.Clone()
				nc.SetAttr("id", "c"+s.r.next())
				nc.SetAttr("created", "2026-09-23T12:00:00Z")
				dst.Children = append(dst.Children, nc)
			}
		}
	case "update":
		if old, nw := validate.FindSub(dst, name, "id", id), validate.FindSub(src, name, "id", id); old != nil && nw != nil {
			*old = *nw.Clone()
		}
	case "delete":
		old := validate.FindSub(dst, name, "id", id)
		var kept []*xmltree.Node
		for _, c := range dst.Children {
			if c != old {
				kept = append(kept, c)
			}
		}
		dst.Children = kept
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS, including every existing engine and CLI test (the fake schema's new element changes nothing for notes without attachments).

- [ ] **Step 5: Commit**

```bash
git add internal/adapter
git commit -m "adapter: attachment actions, Download; fake remote stores attachments"
```

---

### Task 5: `changes` — attachment status and actions

Implements the state table of spec 3.4 inside `changes.Compute`.

**Files:**
- Create: `internal/changes/attachments.go`
- Modify: `internal/changes/changes.go` (replaced whole)
- Test: `internal/changes/attachments_test.go` (new)

**Interfaces:**
- Consumes: Tasks 1–4 (`schema.Schema.Attachment`, `workdir.Tree.LoadAttachments/ListSidecar/Sidecars`, `attach.*`, `adapter.Action.File`, attachment target helpers).
- Produces:
  - `type AttChange struct{ Status byte; Path, AttID string; Action *adapter.Action; Note string }`
  - `FileChange.Attachments []AttChange`, `FileChange.Quiet bool`
  - `FileChange.Actions` = resource actions, then attachment creates/updates, then attachment deletes
  - A `Quiet` change has `Status 'M'` (resource unchanged) or `Status 'C'` (tracked files of a resource that is gone). An attachment folder without a resource file is a `FileChange{Status: '!', Path: <dir>, Err: ...}`.
  - `Compute`'s filter: an attachment path keeps a `Status 'M'` resource as `Quiet` with only the matching attachments.

- [ ] **Step 1: Write the failing tests** `internal/changes/attachments_test.go`

```go
package changes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const noteA = `<note id="1" version="2"><title>T</title><attachment id="a1" name="x.png" size="3" version="1" created="2026-01-01"/></note>`
const noteNoAtt = `<note id="1" version="2"><title>T</title></note>`
const xPath = "a/n.files/x.png"

var noteV2 = strings.Replace(noteA, `size="3" version="1"`, `size="3" version="2"`, 1)

// attTree is setup with chosen working and base contents for a/n.xml.
func attTree(t *testing.T, working, base string) (*workdir.Tree, *workdir.Index) {
	t.Helper()
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("a/n.xml", file(t, working))
	tr.WriteBase("a/n.xml", file(t, base))
	ix, _ := tr.LoadIndex()
	ix.Put(workdir.Entry{ID: "1", Version: "2", Path: "a/n.xml"})
	return tr, ix
}

// fetch writes data as the bytes of attachment a1 and tracks them at version.
func fetch(t *testing.T, tr *workdir.Tree, rel, data, version string) {
	t.Helper()
	if err := tr.WriteFile(rel, []byte(data)); err != nil {
		t.Fatal(err)
	}
	line, err := attach.Entry(tr, "1", "a1", version, rel)
	if err != nil {
		t.Fatal(err)
	}
	atts, _ := tr.LoadAttachments()
	atts.Put(line)
	if err := tr.SaveAttachments(atts); err != nil {
		t.Fatal(err)
	}
}

func attSummary(cs []FileChange) []string {
	var out []string
	for _, c := range cs {
		if !c.Quiet {
			out = append(out, fmt.Sprintf("%c %s", c.Status, c.Path))
		}
		for _, a := range c.Attachments {
			s := fmt.Sprintf("%c %s", a.Status, a.Path)
			if a.Action != nil {
				s += " " + a.Action.Verb + ":" + a.Action.Target
			}
			out = append(out, s)
		}
	}
	return out
}

func TestAttachmentStates(t *testing.T) {
	put := func(p, data string) func(*testing.T, *workdir.Tree) {
		return func(t *testing.T, tr *workdir.Tree) { tr.WriteFile(p, []byte(data)) }
	}
	fetched := func(data string) func(*testing.T, *workdir.Tree) {
		return func(t *testing.T, tr *workdir.Tree) { fetch(t, tr, xPath, "abc", "1"); tr.WriteFile(xPath, []byte(data)) }
	}
	cases := []struct {
		name          string
		working, base string
		setup         []func(*testing.T, *workdir.Tree)
		want          []string
	}{
		{"not fetched", noteA, noteA, nil, nil},
		{"fetched clean", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"changed locally", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abd")},
			[]string{"M a/n.files/x.png update:attachment[id=a1]"}},
		{"changed on remote only", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"both changed", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abd")}, []string{"C a/n.files/x.png"}},
		{"conflict copy ignored", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abd"), put("a/n.files/x.remote-v2.png", "rem")},
			[]string{"C a/n.files/x.png"}},
		{"evicted", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc"),
			func(t *testing.T, tr *workdir.Tree) { os.Remove(tr.Abs(xPath)) }}, nil},
		{"element removed, fetched", noteNoAtt, noteA, []func(*testing.T, *workdir.Tree){fetched("abc")},
			[]string{"M a/n.xml", "D a/n.files/x.png delete:attachment[id=a1]"}},
		{"element removed, not fetched", noteNoAtt, noteA, nil,
			[]string{"M a/n.xml", "D a/n.files/x.png delete:attachment[id=a1]"}},
		{"new file", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/n.files/new.csv", "a,b")},
			[]string{"A a/n.files/new.csv create:attachment[file=new.csv]"}},
		{"untracked collision", noteA, noteA, []func(*testing.T, *workdir.Tree){put(xPath, "abc")}, []string{"! a/n.files/x.png"}},
		{"subdirectory", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/n.files/sub/y", "y")}, []string{"! a/n.files/sub"}},
		{"deleted on remote, changed locally", noteNoAtt, noteNoAtt, []func(*testing.T, *workdir.Tree){fetched("abd")},
			[]string{"C a/n.files/x.png"}},
		{"deleted on remote, unchanged", noteNoAtt, noteNoAtt, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"rename", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc"), func(t *testing.T, tr *workdir.Tree) {
			os.Rename(tr.Abs(xPath), tr.Abs("a/n.files/y.png"))
		}}, []string{"! a/n.files/y.png"}},
		{"orphan folder", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/gone.files/z.bin", "z")}, []string{"! a/gone.files"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, ix := attTree(t, c.working, c.base)
			for _, f := range c.setup {
				f(t, tr)
			}
			got := attSummary(compute(t, tr, ix))
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestAttachmentQuietAndNotes(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	fetch(t, tr, xPath, "abc", "1")
	tr.WriteFile(xPath, []byte("abd"))
	c := one(t, compute(t, tr, ix))
	if c.Status != 'M' || !c.Quiet || len(c.Actions) != 1 || c.Actions[0].File != xPath || !c.Actions[0].IsAttachment() {
		t.Fatalf("%+v", c)
	}
	tr2, ix2 := attTree(t, noteNoAtt, noteA)
	c = one(t, compute(t, tr2, ix2))
	if len(c.Attachments) != 1 || !strings.HasSuffix(c.Attachments[0].Action.Detail, "(not fetched)") {
		t.Fatalf("%+v", c.Attachments)
	}
}

func TestAttachmentReadOnlyKind(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	tr.WriteFile("a/n.files/new.csv", []byte("a,b"))
	ad := fake.New()
	ad.ReadOnlyAttachments()
	cs, err := Compute(tr, ix, ad, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := one(t, cs)
	if len(c.Actions) != 0 || c.Attachments[0].Status != '!' || c.Attachments[0].Note != "attachments of this resource are read-only" {
		t.Fatalf("%+v", c)
	}
}

func TestAttachmentFilterSelectsOneFile(t *testing.T) {
	working := strings.Replace(noteA, "<title>T</title>", "<title>Changed</title>", 1)
	tr, ix := attTree(t, working, noteA)
	fetch(t, tr, xPath, "abc", "1")
	tr.WriteFile(xPath, []byte("abd"))
	filter, _ := PathFilter(tr, []string{tr.Abs(xPath)})
	cs, err := Compute(tr, ix, fake.New(), filter)
	if err != nil {
		t.Fatal(err)
	}
	c := one(t, cs)
	if !c.Quiet || len(c.Actions) != 1 || c.Actions[0].Target != "attachment[id=a1]" {
		t.Fatalf("only the attachment must be selected: %+v", c)
	}
}

func TestAttachmentsOfNewResource(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	tr.WriteFile("a/new.xml", []byte("<note><title>N</title></note>"))
	tr.WriteFile("a/new.files/f.txt", []byte("f"))
	var c FileChange
	for _, x := range compute(t, tr, ix) {
		if x.Path == "a/new.xml" {
			c = x
		}
	}
	if !eq(verbs(c), []string{"create", "create:attachment[file=f.txt]"}) {
		t.Fatalf("%v", verbs(c))
	}
}

func TestSidecarMovedWithResource(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	fetch(t, tr, xPath, "abc", "1")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(tr.Abs("b"), 0o755))
	must(os.Rename(tr.Abs("a/n.xml"), tr.Abs("b/n.xml")))
	must(os.Rename(tr.Abs("a/n.files"), tr.Abs("b/n.files")))
	c := one(t, compute(t, tr, ix))
	if c.Status != 'R' || len(c.Attachments) != 0 || !eq(verbs(c), []string{"move"}) {
		t.Fatalf("%c %v %+v", c.Status, verbs(c), c.Attachments)
	}
}

func TestExplicitVerbIncludesFiles(t *testing.T) {
	env := `<gfs action="publish" channel="news"><content>` + noteA + `</content></gfs>`
	tr, ix := attTree(t, env, noteA)
	tr.WriteFile("a/n.files/new.csv", []byte("a,b"))
	c := one(t, compute(t, tr, ix))
	if !eq(verbs(c), []string{"publish"}) || len(c.Attachments) != 1 || c.Attachments[0].Action != nil || c.Attachments[0].Note != "included in publish" {
		t.Fatalf("%v %+v", verbs(c), c.Attachments)
	}
}
```

(`file`, `compute`, `one`, `verbs` and `eq` already exist in `changes_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/changes/`
Expected: FAIL, `c.Attachments undefined (type FileChange has no field or method Attachments)`.

- [ ] **Step 3: Implement**

`internal/changes/attachments.go`:

```go
package changes

import (
	"fmt"
	"path"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// AttChange is the status of one attachment of a resource (attachments spec 3.4).
type AttChange struct {
	Status byte            // 'A', 'M', 'D', 'C' or '!'
	Path   string          // sidecar file path (the derived path when not fetched)
	AttID  string          // "" for a new file
	Action *adapter.Action // what commit sends; nil for C and !
	Note   string          // reason for C and !, or how an explicit verb uses the file
}

type attCtx struct {
	t       *workdir.Tree
	atts    *workdir.Attachments
	ad      adapter.Adapter
	e       *schema.Elem    // nil: the adapter has no attachments
	claimed map[string]bool // sidecar dirs accounted for by some resource
}

func (c *attCtx) claim(resPath, resID string) {
	c.claimed[attach.SidecarDir(resPath)] = true
	if resID != "" {
		for _, l := range c.atts.ForResource(resID) {
			c.claimed[path.Dir(l.Path)] = true
		}
	}
}

// changes computes the attachment changes of the resource at resPath.
// resID is "" for a new resource; explicit is the envelope action, if any.
func (c *attCtx) changes(resID, resPath string, local, base *xmltree.Node, explicit string) ([]AttChange, error) {
	if c.e == nil {
		return nil, nil
	}
	c.claim(resPath, resID)
	dir := attach.SidecarDir(resPath)
	files, dirs, err := c.t.ListSidecar(dir)
	if err != nil {
		return nil, err
	}
	var out []AttChange
	for _, d := range dirs {
		out = append(out, AttChange{Status: '!', Path: d, Note: "subdirectories are not supported in " + dir + "/"})
	}
	localEls, baseEls := attach.Elements(local, c.e), attach.Elements(base, c.e)
	var lines []workdir.AttEntry
	if resID != "" {
		lines = c.atts.ForResource(resID)
	}
	taken := map[string]bool{} // sidecar files explained by a tracking line
	var missing []workdir.AttEntry
	for _, l := range lines {
		cur := c.current(l, dir)
		taken[cur] = true
		exists := c.t.Exists(cur)
		el := localEls[l.AttID]
		if el == nil {
			if baseEls[l.AttID] != nil {
				out = append(out, c.change('D', cur, l.AttID, "delete", resPath, local))
				continue
			}
			if exists {
				changed, err := attach.Changed(c.t, cur, l)
				if err != nil {
					return nil, err
				}
				if changed {
					out = append(out, AttChange{Status: 'C', Path: cur, AttID: l.AttID, Note: "deleted on remote; kept because it changed locally"})
				}
			}
			continue
		}
		if !exists {
			missing = append(missing, l) // evicted: silent
			continue
		}
		changed, err := attach.Changed(c.t, cur, l)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if v := attach.Version(el, c.e); v != l.Version {
			out = append(out, AttChange{Status: 'C', Path: cur, AttID: l.AttID,
				Note: fmt.Sprintf("changed locally and on remote v%s; remote copy: %s", v, path.Base(attach.ConflictCopy(cur, v)))})
			continue
		}
		out = append(out, c.change('M', cur, l.AttID, "update", resPath, local))
	}
	baseDerived := attach.Derive(base, c.e, resPath)
	for _, id := range sortedKeys(baseEls) {
		if localEls[id] != nil {
			continue
		}
		if _, fetched := c.atts.Get(resID, id); fetched {
			continue
		}
		ch := c.change('D', baseDerived[id], id, "delete", resPath, local)
		if ch.Action != nil {
			ch.Action.Detail += " (not fetched)"
		}
		out = append(out, ch)
	}
	unfetched := map[string]bool{}
	for id, p := range attach.Derive(local, c.e, resPath) {
		if _, ok := c.atts.Get(resID, id); !ok {
			unfetched[p] = true
		}
	}
	for _, f := range files {
		if taken[f] {
			continue
		}
		if orig, ok := attach.ConflictOriginal(path.Base(f)); ok && taken[dir+"/"+orig] {
			continue
		}
		switch {
		case unfetched[f]:
			out = append(out, AttChange{Status: '!', Path: f, Note: "file exists, not fetched by gfs; move it away or delete it"})
		case c.renamed(f, missing):
			out = append(out, AttChange{Status: '!', Path: f, Note: "rename of attachments is not supported; rename it on the remote, or delete it and add it again"})
		default:
			out = append(out, c.change('A', f, "", "create", resPath, local))
		}
	}
	if explicit != "" {
		for i := range out {
			if out[i].Action != nil {
				out[i].Action, out[i].Note = nil, "included in "+explicit
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// current is where a tracked file is now: its recorded path, or the same name
// in the resource's sidecar when the sidecar moved along with the resource.
func (c *attCtx) current(l workdir.AttEntry, dir string) string {
	if c.t.Exists(l.Path) {
		return l.Path
	}
	alt := dir + "/" + path.Base(l.Path)
	if _, tracked := c.atts.ByPath(alt); alt != l.Path && !tracked && c.t.Exists(alt) {
		return alt
	}
	return l.Path
}

// renamed reports whether f has the content of a tracked file that is missing
// from the same folder (a local rename, attachments spec 3.5).
func (c *attCtx) renamed(f string, missing []workdir.AttEntry) bool {
	st, err := c.t.Stat(f)
	if err != nil {
		return false
	}
	for _, l := range missing {
		if path.Dir(l.Path) != path.Dir(f) || st.Size() != l.Size {
			continue
		}
		if sha, _, _, err := attach.Hash(c.t, f); err == nil && sha == l.SHA {
			return true
		}
	}
	return false
}

// change builds an attachment change with its action, or a refusal when the
// adapter does not allow verb or the file is too large.
func (c *attCtx) change(status byte, p, attID, verb, resPath string, local *xmltree.Node) AttChange {
	ch := AttChange{Status: status, Path: p, AttID: attID}
	if !c.e.Allows(verb) {
		ch.Status = '!'
		if len(c.e.Ops) == 0 {
			ch.Note = "attachments of this resource are read-only"
		} else {
			ch.Note = verb + " of attachments is not supported"
		}
		return ch
	}
	if verb != "delete" && c.e.MaxSize > 0 {
		if st, err := c.t.Stat(p); err == nil && st.Size() > c.e.MaxSize {
			ch.Status = '!'
			ch.Note = fmt.Sprintf("%d bytes is over the upload limit of %d", st.Size(), c.e.MaxSize)
			return ch
		}
	}
	a := adapter.Action{Verb: verb, Target: adapter.AttachmentTarget(c.e.Name, attID), File: p}
	if verb == "create" {
		a.Target = adapter.NewAttachmentTarget(c.e.Name, path.Base(p))
	}
	c.ad.Describe(&a, &adapter.Resource{Path: resPath, Root: local})
	ch.Action = &a
	return ch
}

// orphans reports tracked files whose resource is gone and changed locally
// (kept as C), and sidecar folders that no resource accounts for.
func (c *attCtx) orphans(seen map[string]string) ([]FileChange, error) {
	if c.e == nil {
		return nil, nil
	}
	gone := map[string][]AttChange{}
	var goneKeys []string
	for _, l := range c.atts.All() {
		if _, ok := seen[l.ResID]; ok {
			continue
		}
		c.claimed[path.Dir(l.Path)] = true
		if !c.t.Exists(l.Path) {
			continue
		}
		changed, err := attach.Changed(c.t, l.Path, l)
		if err != nil {
			return nil, err
		}
		if changed {
			res, _ := attach.ResourceOf(l.Path)
			if _, ok := gone[res]; !ok {
				goneKeys = append(goneKeys, res)
			}
			gone[res] = append(gone[res], AttChange{Status: 'C', Path: l.Path, AttID: l.AttID,
				Note: "its resource file is gone; kept because it changed locally"})
		}
	}
	var out []FileChange
	for _, res := range goneKeys {
		out = append(out, FileChange{Status: 'C', Path: res, Quiet: true, Attachments: gone[res]})
	}
	dirs, err := c.t.Sidecars()
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if c.claimed[d] {
			continue
		}
		res, _ := attach.ResourceOf(d + "/x")
		out = append(out, FileChange{Status: '!', Path: d, Err: fmt.Errorf("attachment folder without its resource file %s", res)})
	}
	return out, nil
}

// attachmentActions orders attachment actions: creates and updates, then deletes.
func attachmentActions(acs []AttChange) []adapter.Action {
	var first, last []adapter.Action
	for _, a := range acs {
		switch {
		case a.Action == nil:
		case a.Action.Verb == "delete":
			last = append(last, *a.Action)
		default:
			first = append(first, *a.Action)
		}
	}
	return append(first, last...)
}

func sortedKeys(m map[string]*xmltree.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

`internal/changes/changes.go` (whole file):

```go
// Package changes computes gfs status: what changed and which remote actions follow.
package changes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type FileChange struct {
	Status        byte
	Path, OldPath string
	ID            string
	Entry         workdir.Entry
	Local, Base   *envelope.Doc
	Actions       []adapter.Action // resource actions, then attachment actions
	Note          string
	Err           error
	Attachments   []AttChange // attachment status of this resource (attachments spec 3.4)
	Quiet         bool        // the resource file itself is unchanged; only attachments changed
}

func Compute(t *workdir.Tree, ix *workdir.Index, ad adapter.Adapter, filter func(string) bool) ([]FileChange, error) {
	s := ad.Schema()
	files, err := t.Scan()
	if err != nil {
		return nil, err
	}
	atts, err := t.LoadAttachments()
	if err != nil {
		return nil, err
	}
	ac := &attCtx{t: t, atts: atts, ad: ad, e: s.Attachment(), claimed: map[string]bool{}}
	var out []FileChange
	seen := map[string]string{} // id -> path
	for _, p := range files {
		data, err := t.ReadFile(p)
		if err != nil {
			return nil, err
		}
		fc := FileChange{Path: p}
		byPath, inIndex := ix.ByPath(p)
		if envelope.HasMarkers(data) || envelope.HasConflictElement(data) {
			ac.claim(p, byPath.ID)
			fc.Status, fc.ID, fc.Entry = 'C', byPath.ID, byPath
			if inIndex {
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			ac.claim(p, byPath.ID)
			fc.Status, fc.Err = 'A', err
			if inIndex {
				fc.Status, fc.ID, fc.Entry = 'M', byPath.ID, byPath
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		fc.Local = doc
		id, _ := doc.Content.Attr(s.ID)
		entry, known := ix.ByID(id)
		if id != "" && known {
			if other, dup := seen[id]; dup {
				ac.claim(p, "")
				fc.Status, fc.Err = 'A', fmt.Errorf("duplicate identity %s=%q, also in %s", s.ID, id, other)
				out = append(out, fc)
				continue
			}
			seen[id] = p
			fc.ID, fc.Entry = id, entry
			bdata, err := t.ReadBase(entry.Path)
			if err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
			if fc.Base, err = envelope.Parse(bdata); err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
		}
		moved := fc.Base != nil && entry.Path != p
		var baseContent *xmltree.Node
		basePath := ""
		if fc.Base != nil {
			baseContent, basePath = fc.Base.Content, entry.Path
		}
		if fc.Attachments, err = ac.changes(fc.ID, p, doc.Content, baseContent, doc.Action); err != nil {
			return nil, err
		}
		if fc.Base != nil && !moved && doc.Action == "" && len(doc.Errors) == 0 &&
			CanonContent(doc.Content, s) == CanonContent(fc.Base.Content, s) {
			if len(fc.Attachments) == 0 {
				continue // unchanged
			}
			fc.Status, fc.Quiet, fc.Actions = 'M', true, attachmentActions(fc.Attachments)
			out = append(out, fc)
			continue
		}
		switch {
		case len(doc.Errors) > 0:
			fc.Status = '!'
		case fc.Base == nil:
			fc.Status = 'A'
		case moved:
			fc.Status = 'R'
		default:
			fc.Status = 'M'
		}
		if moved {
			fc.OldPath = entry.Path
			if !adapter.IsMove(ad.PathModel(), entry.Path, p) {
				fc.Note = "rename ignored: name is derived"
			}
		}
		fc.Err = validate.Resource(doc.Content, baseContent, s)
		fc.Actions = append(ResolveActions(ad, baseContent, doc, basePath, p), attachmentActions(fc.Attachments)...)
		out = append(out, fc)
	}
	for _, e := range ix.All() {
		if _, ok := seen[e.ID]; ok {
			continue
		}
		fc := FileChange{Status: 'D', Path: e.Path, ID: e.ID, Entry: e, Actions: []adapter.Action{{Verb: "delete"}}}
		if bdata, err := t.ReadBase(e.Path); err == nil {
			fc.Base, _ = envelope.Parse(bdata)
		}
		ad.Describe(&fc.Actions[0], nil)
		out = append(out, fc)
	}
	orphans, err := ac.orphans(seen)
	if err != nil {
		return nil, err
	}
	out = append(out, orphans...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if filter == nil {
		return out, nil
	}
	var kept []FileChange
	for _, c := range out {
		if filter(c.Path) || (c.OldPath != "" && filter(c.OldPath)) {
			kept = append(kept, c)
			continue
		}
		if c.Status != 'M' {
			continue // attachments alone can be selected only from a modified resource
		}
		var acs []AttChange
		for _, a := range c.Attachments {
			if filter(a.Path) {
				acs = append(acs, a)
			}
		}
		if len(acs) == 0 {
			continue
		}
		c.Attachments, c.Quiet, c.Actions = acs, true, attachmentActions(acs)
		kept = append(kept, c)
	}
	return kept, nil
}

func PathFilter(t *workdir.Tree, args []string) (func(string) bool, error) {
	if len(args) == 0 {
		return nil, nil
	}
	var rels []string
	for _, a := range args {
		r, err := t.Rel(a)
		if err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return func(p string) bool {
		for _, r := range rels {
			if r == "" || p == r || strings.HasPrefix(p, r+"/") {
				return true
			}
		}
		return false
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS, the whole suite included (existing status, commit and pull tests have no attachments and see no change).

- [ ] **Step 5: Commit**

```bash
git add internal/changes
git commit -m "changes: attachment status, actions and orphan folders in Compute"
```

---
### Task 6: `engine` — tracking in `Env`, placement helpers, `gfs get`

**Files:**
- Create: `internal/engine/attach.go`, `internal/engine/get.go`
- Modify: `internal/engine/engine.go` (`Env.Atts`, `Forget`), `internal/engine/clone.go`, `internal/cli/env.go`
- Test: `internal/engine/get_test.go` (new)

**Interfaces:**
- Consumes: Tasks 2–5.
- Produces:
  - `Env.Atts *workdir.Attachments`: set by `Clone` and by the CLI's `openEnv`; engine code saves it after every change
  - `func (e *Env) download(ctx, resID, attID, rel string) (workdir.AttEntry, error)`: streams bytes into `rel` atomically and returns the line (not stored)
  - `func (e *Env) baseRoot(en workdir.Entry) (*xmltree.Node, error)`
  - `func (e *Env) placeAttachments(id, resPath string, root *xmltree.Node) ([][2]string, error)`: moves tracked files to their derived paths (repeating until no move is possible), follows sidecars the user moved, drops lines of evicted files, saves; returns `{old, new}` moves
  - `func (e *Env) forgetAttachment(resID, attID, rel string) error`, `func (e *Env) dropAttachments(id string) error` (used by `Forget`: unchanged tracked files are removed, changed ones and their lines are kept)
  - `func (e *Env) removeConflictCopies(p string)`, `func (e *Env) open(rel string) (io.ReadCloser, error)`, `func humanSize(n int64) string`
  - `type GetReport struct{ Fetched, UpToDate, Refused, Failed int; Bytes int64 }`, `func (r GetReport) ExitCode() int`
  - `func Get(ctx context.Context, e *Env, targets []string) (GetReport, error)`: targets are tree-relative (`""` = whole tree)

- [ ] **Step 1: Write the failing test** `internal/engine/get_test.go`

```go
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
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out)
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run Get`
Expected: FAIL, `undefined: Get`.

- [ ] **Step 3: Implement**

`internal/engine/engine.go`:

1. Add the field to `Env`, after `Index`:

```go
	Atts    *workdir.Attachments // fetched attachments; saved after every change
```

2. Make `Forget` drop the resource's attachments first:

```go
func (e *Env) Forget(id, path string) error {
	if err := e.dropAttachments(id); err != nil {
		return err
	}
	if e.Tree.Exists(path) {
		if err := e.Tree.Remove(path); err != nil {
			return err
		}
	}
	if err := e.Tree.RemoveBase(path); err != nil {
		return err
	}
	e.Index.Delete(id)
	return e.Tree.SaveIndex(e.Index)
}
```

`internal/engine/clone.go`: build the `Env` with an empty attachment set:

```go
	env := &Env{Tree: t, Index: ix, Atts: workdir.NewAttachments(), Adapter: ad, Session: sess, Out: out, Now: time.Now}
```

`internal/cli/env.go`, in `openEnv` after loading the index:

```go
	atts, err := t.LoadAttachments()
	if err != nil {
		return nil, "", nil, err
	}
```

and set `Atts: atts` in the `engine.Env` literal.

`internal/engine/attach.go`:

```go
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func (e *Env) attachmentElem() *schema.Elem { return e.Adapter.Schema().Attachment() }

func (e *Env) saveAtts() error { return e.Tree.SaveAttachments(e.Atts) }

func (e *Env) open(rel string) (io.ReadCloser, error) { return e.Tree.Open(rel) }

// download streams one attachment's current bytes into rel, atomically, and
// returns its tracking line. The caller decides whether to store the line.
func (e *Env) download(ctx context.Context, resID, attID, rel string) (workdir.AttEntry, error) {
	var info adapter.AttachmentInfo
	h := sha256.New()
	err := e.Tree.WriteStream(rel, func(w io.Writer) error {
		var err error
		info, err = e.Session.Download(ctx, resID, attID, io.MultiWriter(w, h))
		return err
	})
	if err != nil {
		return workdir.AttEntry{}, err
	}
	st, err := e.Tree.Stat(rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	return workdir.AttEntry{ResID: resID, AttID: attID, Version: info.Version, SHA: hex.EncodeToString(h.Sum(nil)),
		Size: st.Size(), MTime: attach.LineMTime(st), Path: rel}, nil
}

// baseRoot returns the base content of an indexed resource.
func (e *Env) baseRoot(en workdir.Entry) (*xmltree.Node, error) {
	data, err := e.Tree.ReadBase(en.Path)
	if err != nil {
		return nil, err
	}
	doc, err := envelope.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("base %s: %w", en.Path, err)
	}
	return doc.Content, nil
}

// placeAttachments moves the tracked files of resource id to the paths derived
// from root at resPath (resource moved, attachment renamed, a de-duplication
// number shifted), follows sidecars the user moved along with the resource, and
// drops the lines of evicted files (attachments spec 3.2, 3.5). A move whose
// target is taken waits for a later pass; passes repeat while moves happen.
func (e *Env) placeAttachments(id, resPath string, root *xmltree.Node) ([][2]string, error) {
	el := e.attachmentElem()
	if el == nil {
		return nil, nil
	}
	derived := attach.Derive(root, el, resPath)
	dir := attach.SidecarDir(resPath)
	var moves [][2]string
	for pass, moved := 0, true; moved && pass < 8; pass++ {
		moved = false
		for _, l := range e.Atts.ForResource(id) {
			cur := l.Path
			if !e.Tree.Exists(cur) {
				alt := dir + "/" + path.Base(cur)
				if _, tracked := e.Atts.ByPath(alt); alt == cur || tracked || !e.Tree.Exists(alt) {
					e.Atts.Delete(l.ResID, l.AttID) // evicted
					continue
				}
				cur = alt
			}
			if want, ok := derived[l.AttID]; ok && want != cur && !e.Tree.Exists(want) {
				if err := e.Tree.Rename(cur, want); err != nil {
					return moves, err
				}
				moves = append(moves, [2]string{cur, want})
				cur, moved = want, true
			}
			if cur != l.Path {
				l.Path = cur
				e.Atts.Put(l)
			}
		}
	}
	return moves, e.saveAtts()
}

// forgetAttachment removes one tracked attachment's file, if present, and its line.
func (e *Env) forgetAttachment(resID, attID, rel string) error {
	if e.Tree.Exists(rel) {
		if err := e.Tree.Remove(rel); err != nil {
			return err
		}
	}
	e.Atts.Delete(resID, attID)
	return e.saveAtts()
}

// dropAttachments removes the unchanged tracked files of a resource that is
// gone; changed ones keep their line, so status shows them as C (spec 5.4).
func (e *Env) dropAttachments(id string) error {
	for _, l := range e.Atts.ForResource(id) {
		if e.Tree.Exists(l.Path) {
			changed, err := attach.Changed(e.Tree, l.Path, l)
			if err != nil {
				return err
			}
			if changed {
				continue
			}
			if err := e.Tree.Remove(l.Path); err != nil {
				return err
			}
		}
		e.Atts.Delete(l.ResID, l.AttID)
	}
	return e.saveAtts()
}

// removeConflictCopies deletes the remote copies written next to p.
func (e *Env) removeConflictCopies(p string) {
	files, _, _ := e.Tree.ListSidecar(path.Dir(p))
	for _, f := range files {
		if orig, ok := attach.ConflictOriginal(path.Base(f)); ok && orig == path.Base(p) {
			_ = e.Tree.Remove(f)
		}
	}
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func sortedIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

`internal/engine/get.go`:

```go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type GetReport struct {
	Fetched, UpToDate, Refused, Failed int
	Bytes                              int64
}

func (r GetReport) ExitCode() int {
	if r.Refused+r.Failed > 0 {
		return 1
	}
	return 0
}

type getItem struct{ resID, attID, path, version string }

// Get downloads attachment bytes on demand (attachments spec 4.1). A target is
// a resource file, a folder (every resource under it, "" for the whole tree),
// a sidecar folder, or one sidecar file.
func Get(ctx context.Context, e *Env, targets []string) (GetReport, error) {
	var r GetReport
	el := e.attachmentElem()
	if el == nil {
		return r, fmt.Errorf("%s resources have no attachments", e.Adapter.Name())
	}
	items, unmatched, err := e.getItems(targets, el)
	if err != nil {
		return r, err
	}
	for _, t := range unmatched {
		if t == "" {
			t = "."
		}
		r.Refused++
		fmt.Fprintf(e.Out, "  !  %s   not a resource, folder or attachment of this tree\n", t)
	}
	for _, it := range items {
		if err := e.getOne(ctx, it, &r); err != nil {
			return r, err
		}
	}
	fmt.Fprintf(e.Out, "%d fetched, %d up to date, %d refused, %d failed (%s)\n", r.Fetched, r.UpToDate, r.Refused, r.Failed, humanSize(r.Bytes))
	return r, e.saveAtts()
}

func (e *Env) getItems(targets []string, el *schema.Elem) ([]getItem, []string, error) {
	working, err := e.workingPaths()
	if err != nil {
		return nil, nil, err
	}
	matched := map[string]bool{}
	seen := map[string]bool{}
	var items []getItem
	for _, en := range e.Index.All() {
		wp := working[en.ID]
		if wp == "" {
			wp = en.Path
		}
		root, err := e.contentOf(wp, en)
		if err != nil {
			return nil, nil, err
		}
		derived := attach.Derive(root, el, wp)
		els := attach.Elements(root, el)
		sd := attach.SidecarDir(wp)
		whole := func(t string) bool { return t == "" || t == wp || t == sd || strings.HasPrefix(wp, t+"/") }
		for _, t := range targets {
			if whole(t) {
				matched[t] = true
			}
		}
		for _, id := range sortedIDs(derived) {
			p := derived[id]
			tracked := p
			if l, ok := e.Atts.Get(en.ID, id); ok {
				tracked = l.Path
			}
			for _, t := range targets {
				if !whole(t) && t != p && t != tracked {
					continue
				}
				matched[t] = true
				if key := en.ID + "\x00" + id; !seen[key] {
					seen[key] = true
					items = append(items, getItem{resID: en.ID, attID: id, path: p, version: attach.Version(els[id], el)})
				}
			}
		}
	}
	var unmatched []string
	for _, t := range targets {
		if !matched[t] {
			unmatched = append(unmatched, t)
		}
	}
	return items, unmatched, nil
}

// workingPaths maps resource id to its working path (resources may be moved locally).
func (e *Env) workingPaths() (map[string]string, error) {
	files, err := e.Tree.Scan()
	if err != nil {
		return nil, err
	}
	idAttr := e.Adapter.Schema().ID
	out := map[string]string{}
	for _, p := range files {
		data, err := e.Tree.ReadFile(p)
		if err != nil {
			return nil, err
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			continue // conflicted or invalid: the base is used
		}
		if id, ok := doc.Content.Attr(idAttr); ok {
			if _, dup := out[id]; !dup {
				out[id] = p
			}
		}
	}
	return out, nil
}

// contentOf returns the working content of a resource, or its base when the
// working file is missing or does not parse.
func (e *Env) contentOf(wp string, en workdir.Entry) (*xmltree.Node, error) {
	if data, err := e.Tree.ReadFile(wp); err == nil {
		if doc, err := envelope.Parse(data); err == nil {
			return doc.Content, nil
		}
	}
	return e.baseRoot(en)
}

func (e *Env) getOne(ctx context.Context, it getItem, r *GetReport) error {
	if l, ok := e.Atts.Get(it.resID, it.attID); ok && e.Tree.Exists(l.Path) {
		changed, err := attach.Changed(e.Tree, l.Path, l)
		if err != nil {
			return err
		}
		if changed {
			r.Refused++
			fmt.Fprintf(e.Out, "  !  %s   changed locally; commit or resolve first\n", l.Path)
			return nil
		}
		if l.Version == it.version {
			r.UpToDate++
			fmt.Fprintf(e.Out, "  =  %s   up to date\n", l.Path)
			return nil
		}
		it.path = l.Path
	} else if e.Tree.Exists(it.path) {
		r.Refused++
		fmt.Fprintf(e.Out, "  !  %s   file exists, not fetched by gfs; move it away or delete it\n", it.path)
		return nil
	}
	line, err := e.download(ctx, it.resID, it.attID, it.path)
	if err != nil {
		r.Failed++
		fmt.Fprintf(e.Out, "  !  %s   FAIL %s\n", it.path, oneLine(err))
		e.Log("get", it.path, "", "FAIL", oneLine(err))
		return nil
	}
	e.Atts.Put(line)
	r.Fetched++
	r.Bytes += line.Size
	ver := ""
	if line.Version != "-" {
		ver = "   v" + line.Version
	}
	note := ""
	if it.version != "-" && line.Version != it.version {
		note = fmt.Sprintf("   (remote has v%s; run gfs pull)", line.Version)
	}
	fmt.Fprintf(e.Out, "  +  %s   %s%s%s\n", it.path, humanSize(line.Size), ver, note)
	e.Log("get", it.path, "", "ok", strings.TrimSpace(ver))
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine internal/cli/env.go
git commit -m "engine: attachment tracking in Env, placement helpers, Get"
```

---

### Task 7: `engine.Commit` with attachments

Spec 4.4. Attachment actions ride the existing per-file algorithm. What's new: reporting paths, the remote-version check before uploading, the `Quiet` fast-forward, and tracking updates on write-back.

**Files:**
- Modify: `internal/engine/commit.go` (replaced whole), `internal/engine/attach.go` (add `syncCommitted`, `sidecarFiles`)
- Test: `internal/engine/commit_attach_test.go` (new)

**Interfaces:**
- Consumes: Task 5 `FileChange.Attachments/Quiet`, `Action.File`; Task 6 helpers.
- Produces (behaviour):
  - Report and log lines for attachment actions use the sidecar path. A successful attachment create shows `id=<id>`.
  - `C` and `!` attachment changes are reported and counted as conflicts / failures, without blocking the resource's other actions.
  - `func (e *Env) syncCommitted(resID, resPath string, root *xmltree.Node, results []adapter.Result) error`
  - `func (e *Env) sidecarFiles(fc changes.FileChange) []string` (explicit verbs only)

- [ ] **Step 1: Write the failing tests** `internal/engine/commit_attach_test.go`

```go
package engine

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

// dropAttachment removes the <attachment id=...> line from a working file.
func dropAttachment(t *testing.T, env *Env, p, id string) {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*<attachment id="` + regexp.QuoteMeta(id) + `"[^\n]*\n`)
	s := read(t, env, p)
	if !re.MatchString(s) {
		t.Fatalf("no attachment %s in %s:\n%s", id, p, s)
	}
	write(t, env, p, re.ReplaceAllString(s, ""))
}

func TestCommitAttachmentUpdate(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x.png", "abcd")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || r.Actions != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, v, _ := ad.Remote.Attachment("1", "10"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
	if !strings.Contains(out.String(), "update  a/one.files/x.png   ok") {
		t.Fatal(out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `<attachment id="10" name="x.png" size="4" version="2"`) {
		t.Fatalf("write-back must show the new version:\n%s", read(t, env, "a/one.xml"))
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "2" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
	log, _ := env.Tree.ReadLog()
	if last := log[len(log)-1]; last.Verb != "update" || last.Path != "a/one.files/x.png" || last.Outcome != "ok" {
		t.Fatalf("%+v", last)
	}
}

func TestCommitAttachmentCreate(t *testing.T) {
	env, ad, out := attached(t)
	write(t, env, "a/one.files/new.csv", "a,b")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	id, ok := ad.Remote.AttachmentNamed("1", "new.csv")
	if !ok || !strings.Contains(out.String(), "create  a/one.files/new.csv   ok  id="+id) {
		t.Fatalf("%v\n%s", ok, out)
	}
	if l, ok := env.Atts.Get("1", id); !ok || l.Path != "a/one.files/new.csv" || l.Version != "1" {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `name="new.csv"`) {
		t.Fatal("element not written back")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentDelete(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	dropAttachment(t, env, "a/one.xml", "10")
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 || r.ExitCode() != 1 {
		t.Fatalf("delete is ask; non-TTY denies: %+v\n%s", r, out)
	}
	if _, _, ok := ad.Remote.Attachment("1", "10"); !ok {
		t.Fatal("denied delete must not run")
	}
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"delete": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	// attachment 11 is now the only x.png, so its file takes over the plain name
	if _, _, ok := ad.Remote.Attachment("1", "10"); ok || read(t, env, "a/one.files/x.png") != "second" || env.Tree.Exists("a/one.files/x (2).png") {
		t.Fatal("remote attachment and its local file must be gone")
	}
	if _, ok := env.Atts.Get("1", "10"); ok {
		t.Fatal("line must be gone")
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitDeleteOfUnfetchedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	dropAttachment(t, env, "b/two.xml", "20")
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"delete": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if _, _, ok := ad.Remote.Attachment("2", "20"); ok {
		t.Fatal("not deleted")
	}
}

func TestCommitNewResourceWithAttachment(t *testing.T) {
	env, ad, out := attached(t)
	write(t, env, "c/new.xml", "<note><title>N</title></note>")
	write(t, env, "c/new.files/f.txt", "f")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || r.Actions != 2 {
		t.Fatalf("%+v\n%s", r, out)
	}
	en, ok := env.Index.ByPath("c/new.xml")
	if !ok {
		t.Fatal("new resource not indexed")
	}
	if _, ok := ad.Remote.AttachmentNamed(en.ID, "f.txt"); !ok {
		t.Fatal("attachment of the new resource not uploaded")
	}
	if ls := env.Atts.ForResource(en.ID); len(ls) != 1 || ls[0].Path != "c/new.files/f.txt" {
		t.Fatalf("%+v", ls)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentConflict(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.EditAttachment("1", "10", []byte("remote"))
	write(t, env, "a/one.files/x.png", "mine")
	r := commit(t, env, CommitOpts{})
	if r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "remote" {
		t.Fatal("a conflicting upload must not run")
	}
	if read(t, env, "a/one.files/x.remote-v2.png") != "remote" || read(t, env, "a/one.files/x.png") != "mine" {
		t.Fatal("both copies must be kept")
	}
	cs := status(t, env)
	if len(cs) != 1 || len(cs[0].Attachments) != 1 || cs[0].Attachments[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitAttachmentPartialFailure(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	write(t, env, "a/one.files/x.png", "abcd")
	ad.Remote.FailVerb = map[string]error{"update attachment[id=10]": errors.New("413 too large")}
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if got := read(t, env, "a/one.xml"); !strings.Contains(got, `target="attachment[id=10]"`) || !strings.Contains(got, "413 too large") {
		t.Fatalf("<errors> must name the attachment:\n%s", got)
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "1" || read(t, env, "a/one.files/x.png") != "abcd" {
		t.Fatal("a failed upload leaves the file and its line alone")
	}
}

func TestCommitMoveTakesSidecarAlong(t *testing.T) {
	env, _, out := attached(t)
	fetchAll(t, env, out)
	if err := env.Tree.Rename("a/one.xml", "a/uno.xml"); err != nil {
		t.Fatal(err)
	}
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if read(t, env, "a/uno.files/x.png") != "abc" || read(t, env, "a/uno.files/x (2).png") != "second" || env.Tree.Exists("a/one.files") {
		t.Fatal("sidecar must follow the resource")
	}
	if l, _ := env.Atts.Get("1", "10"); l.Path != "a/uno.files/x.png" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestCommitReadOnlyAttachments(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.ReadOnlyAttachments()
	write(t, env, "a/one.files/x.png", "abcd")
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || !strings.Contains(out.String(), "read-only") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "abc" {
		t.Fatal("nothing may be uploaded")
	}
}

func TestCommitAttachmentDryRunAndFilter(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	write(t, env, "a/one.files/x.png", "abcd")
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "update  a/one.files/x.png   would run") {
		t.Fatal(out)
	}
	filter, _ := changes.PathFilter(env.Tree, []string{env.Tree.Abs("a/one.files/x.png")})
	if r := commit(t, env, CommitOpts{Filter: filter}); r.ExitCode() != 0 || r.Actions != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if data, _, _ := ad.Remote.Attachment("1", "10"); string(data) != "abcd" || title(t, env, "1") != "One" {
		t.Fatal("only the selected attachment may be committed")
	}
	if !strings.Contains(read(t, env, "a/one.xml"), "<title>Uno</title>") {
		t.Fatal("write-back must keep the unselected local edit")
	}
}
```

(`attached`, `fetchAll` come from Task 6; `commit`, `title` from `commit_test.go`; `read`, `write`, `edit`, `status` from `engine_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run 'CommitAttachment|CommitNewResource|CommitMove|CommitReadOnly|CommitDeleteOf'`
Expected: FAIL. For example `TestCommitAttachmentUpdate` reports `update  a/one.files/x.png   ok` missing (the report still uses the resource path), and the create test fails because `finish` takes the attachment id as the resource id.

- [ ] **Step 3: Implement**

`internal/engine/attach.go`: add `"slices"`, `"strings"` and `"github.com/KrzysztofBogdan/gitfs/internal/changes"` to the imports and append:

```go
// syncCommitted updates tracking after a commit's write-back (attachments spec
// 4.4): uploaded files are tracked at their new version without re-downloading,
// deleted ones are forgotten, and every file moves to its derived path.
func (e *Env) syncCommitted(resID, resPath string, root *xmltree.Node, results []adapter.Result) error {
	for _, res := range results {
		a := res.Action
		if !a.IsAttachment() || res.Err != nil {
			continue
		}
		id, _, _ := adapter.ParseAttachmentTarget(a.Target)
		switch a.Verb {
		case "create", "update":
			if res.ID != "" {
				id = res.ID
			}
			line, err := attach.Entry(e.Tree, resID, id, res.Version, a.File)
			if err != nil {
				return err
			}
			e.Atts.Put(line)
		case "delete":
			if l, ok := e.Atts.Get(resID, id); ok && e.Tree.Exists(l.Path) {
				if err := e.Tree.Remove(l.Path); err != nil {
					return err
				}
			}
			e.Atts.Delete(resID, id)
		}
	}
	_, err := e.placeAttachments(resID, resPath, root)
	return err
}

// sidecarFiles lists the files an explicit verb sends along (attachments spec 4.4).
func (e *Env) sidecarFiles(fc changes.FileChange) []string {
	if fc.Local == nil || fc.Local.Action == "" {
		return nil
	}
	files, _, _ := e.Tree.ListSidecar(attach.SidecarDir(fc.Path))
	var out []string
	for _, f := range files {
		if _, isCopy := attach.ConflictOriginal(path.Base(f)); !isCopy && !strings.HasPrefix(path.Base(f), ".") {
			out = append(out, f)
		}
	}
	return out
}

// checkAttachments is commit step 3 for attachment actions (spec 4.4). An
// update whose remote version moved since the line becomes a conflict with a
// remote copy; a delete of an attachment already gone succeeds without a call.
func (e *Env) checkAttachments(ctx context.Context, fc changes.FileChange, remote *adapter.Resource, acts []adapter.Action, r *Report) ([]adapter.Action, error) {
	el := e.attachmentElem()
	if el == nil {
		return acts, nil
	}
	remoteEls := attach.Elements(remote.Root, el)
	var kept []adapter.Action
	for _, a := range acts {
		if !a.IsAttachment() || a.Verb == "create" {
			kept = append(kept, a)
			continue
		}
		id, _, _ := adapter.ParseAttachmentTarget(a.Target)
		rel := remoteEls[id]
		switch {
		case a.Verb == "delete" && rel == nil:
			r.Actions++
			e.line("delete", a.File, "", "ok", "already deleted on remote")
			e.Log("delete", a.File, "", "ok", "already deleted on remote")
			if l, ok := e.Atts.Get(fc.ID, id); ok {
				if err := e.forgetAttachment(fc.ID, id, l.Path); err != nil {
					return nil, err
				}
			}
		case a.Verb == "update" && rel == nil:
			r.Conflicts++
			fmt.Fprintf(e.Out, "  C  %s   deleted on remote; kept because it changed locally\n", a.File)
			e.Log("update", a.File, "", "FAIL", "deleted on remote")
		case a.Verb == "update":
			line, _ := e.Atts.Get(fc.ID, id)
			v := attach.Version(rel, el)
			if v == line.Version {
				kept = append(kept, a)
				continue
			}
			cp := attach.ConflictCopy(a.File, v)
			if !e.Tree.Exists(cp) {
				if _, err := e.download(ctx, fc.ID, id, cp); err != nil {
					r.Failed++
					e.line("update", a.File, "", "FAIL", "remote moved to v"+v+"; download failed: "+oneLine(err))
					continue
				}
			}
			r.Conflicts++
			fmt.Fprintf(e.Out, "  C  %s   changed locally and on remote v%s; remote copy: %s\n", a.File, v, path.Base(cp))
			e.Log("update", a.File, "", "FAIL", "conflict with remote v"+v)
		default:
			kept = append(kept, a)
		}
	}
	return kept, nil
}

// withRemoteAttachments is the write-back content of a Quiet file: its own (or
// merged) content with the remote's service-owned root attributes and
// attachment elements, so local edits that were not committed survive.
func (e *Env) withRemoteAttachments(content, remote *xmltree.Node) *xmltree.Node {
	el := e.attachmentElem()
	out := content.Clone()
	out.Attrs = append([]xmltree.Attr(nil), remote.Attrs...)
	out.Children = slices.DeleteFunc(out.Children, func(c *xmltree.Node) bool {
		return c.Kind == xmltree.Element && c.Name == el.Name
	})
	for _, c := range remote.ChildrenNamed(el.Name) {
		out.Children = append(out.Children, c.Clone())
	}
	return out
}

// fastForward writes back a Quiet file none of whose actions is left to run,
// so its attachment elements show the remote versions (plan design decision 4).
func (e *Env) fastForward(fc changes.FileChange, remote *adapter.Resource, content *xmltree.Node) error {
	root := e.withRemoteAttachments(content, remote.Root)
	if err := e.Tree.WriteFile(fc.Path, e.Canonical(root)); err != nil {
		return err
	}
	if err := e.StoreBase(remote, fc.Entry.Path); err != nil {
		return err
	}
	_, err := e.placeAttachments(fc.ID, fc.Path, root)
	return err
}
```

`internal/engine/commit.go` (whole file):

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type CommitOpts struct {
	DryRun  bool
	Allow   map[string]bool
	NoMerge bool
	Filter  func(string) bool
}

type Report struct{ Actions, Failed, Denied, Conflicts int }

func (r Report) ExitCode() int {
	if r.Failed+r.Denied+r.Conflicts > 0 {
		return 1
	}
	return 0
}

func category(fc changes.FileChange) int {
	switch fc.Status {
	case 'A':
		return 0
	case 'R':
		return 2
	case 'D':
		return 3
	}
	return 1
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func oneLine(err error) string { return strings.ReplaceAll(err.Error(), "\n", "; ") }

// actPath is the path an action is reported under: its attachment file, or the resource.
func actPath(fc changes.FileChange, a adapter.Action) string {
	if a.IsAttachment() {
		return a.File
	}
	return fc.Path
}

func (e *Env) line(verb, path, newPath, outcome, detail string) {
	p := path
	if newPath != "" && newPath != path {
		p += " -> " + newPath
	}
	s := fmt.Sprintf("%-7s %s   %s", verb, p, outcome)
	if detail != "" {
		s += "  " + detail
	}
	fmt.Fprintln(e.Out, s)
}

func (e *Env) policy() (*policy.Policy, error) {
	cfg, err := e.Tree.LoadConfig()
	if err != nil {
		return nil, err
	}
	return policy.FromConfig(cfg.Section("policy"))
}

func Commit(ctx context.Context, e *Env, o CommitOpts) (Report, error) {
	pol, err := e.policy()
	if err != nil {
		return Report{}, err
	}
	cs, err := changes.Compute(e.Tree, e.Index, e.Adapter, o.Filter)
	if err != nil {
		return Report{}, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return category(cs[i]) < category(cs[j]) })
	d := &policy.Decider{Policy: pol, Allowed: o.Allow, Prompt: e.Prompt}
	var r Report
	for _, fc := range cs {
		var err error
		if o.DryRun {
			e.dryRunFile(ctx, fc, d, &r)
		} else {
			err = e.commitFile(ctx, fc, d, o, &r)
		}
		if err != nil {
			return r, err
		}
	}
	footer := fmt.Sprintf("%s, %d failed, %d denied", plural(r.Actions, "action"), r.Failed, r.Denied)
	if r.Conflicts > 0 {
		footer += ", " + plural(r.Conflicts, "conflict")
	}
	if o.DryRun {
		footer = "dry run: " + footer
	}
	fmt.Fprintln(e.Out, footer)
	return r, nil
}

// precheck reports attachment conflicts and refusals, then handles C, invalid
// and no-op files. It returns false when the file is done.
func (e *Env) precheck(fc changes.FileChange, r *Report) bool {
	for _, a := range fc.Attachments {
		switch a.Status {
		case 'C':
			r.Conflicts++
			fmt.Fprintf(e.Out, "  C  %s   %s\n", a.Path, a.Note)
		case '!':
			r.Failed++
			e.line("invalid", a.Path, "", "FAIL", a.Note)
			e.Log("invalid", a.Path, "", "FAIL", a.Note)
		}
	}
	if fc.Quiet && fc.Status == 'C' {
		return false // tracked files of a resource that is gone, reported above
	}
	switch {
	case fc.Status == 'C':
		r.Conflicts++
		fmt.Fprintf(e.Out, "  C  %s   unresolved conflict: edit, remove <conflict/>, commit (or gfs resolve)\n", fc.Path)
		return false
	case fc.Err != nil:
		r.Failed++
		e.line("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		e.Log("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		return false
	case len(fc.Actions) == 0:
		if fc.Note != "" {
			fmt.Fprintf(e.Out, "warning: %s: %s\n", fc.Path, fc.Note)
		}
		return false
	}
	return true
}

func (e *Env) dryRunFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, r *Report) {
	if !e.precheck(fc, r) {
		return
	}
	var checks []adapter.Result
	if fc.Local != nil {
		checks = e.Session.Check(ctx, adapter.ApplyRequest{
			Local: &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: fc.Local.Content},
			Base:  baseResource(fc), Actions: fc.Actions, IDByPath: e.IDByPath, Open: e.open,
		})
	}
	for i, a := range fc.Actions {
		level := d.Policy.Level(a.Class)
		mark := ""
		switch {
		case level == policy.Deny:
			mark = "[deny]"
			r.Denied++
		case level == policy.Ask && !d.Allowed[a.Class]:
			mark = "[ask]"
			if d.Prompt == nil {
				r.Denied++
			} else {
				r.Actions++
			}
		default:
			r.Actions++
		}
		if i < len(checks) && checks[i].Err != nil {
			r.Failed++
			e.line(a.Verb, actPath(fc, a), a.To, "FAIL", oneLine(checks[i].Err))
			continue
		}
		e.line(a.Verb, actPath(fc, a), a.To, "would run", strings.TrimSpace(a.Detail+"  "+mark))
	}
}

func baseResource(fc changes.FileChange) *adapter.Resource {
	if fc.Base == nil {
		return nil
	}
	return &adapter.Resource{ID: fc.ID, Version: fc.Entry.Version, Path: fc.Entry.Path, Root: fc.Base.Content}
}

func (e *Env) commitFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, o CommitOpts, r *Report) error {
	if !e.precheck(fc, r) {
		return nil
	}
	for _, a := range fc.Actions {
		p := actPath(fc, a)
		if run, reason := d.Decide(a.Class, fmt.Sprintf("%s %s ?", a.Verb, p)); !run {
			r.Denied++
			e.line(a.Verb, p, "", "denied", reason)
			e.Log(a.Verb, p, "", "denied", reason)
			return nil
		}
	}
	s := e.Adapter.Schema()
	acts := fc.Actions
	for attempt := 0; ; attempt++ {
		var remote *adapter.Resource
		content := (*xmltree.Node)(nil)
		if fc.Local != nil {
			content = fc.Local.Content
		}
		merged := false
		if fc.Base != nil {
			var err error
			remote, err = e.Session.Fetch(ctx, fc.ID)
			if errors.Is(err, adapter.ErrNotFound) {
				if fc.Status == 'D' {
					r.Actions++
					e.line("delete", fc.Path, "", "ok", "already deleted on remote")
					e.Log("delete", fc.Path, "", "ok", "already deleted on remote")
					return e.Forget(fc.ID, fc.Path)
				}
				return e.failAll(fc, acts, errors.New("deleted on remote; run gfs pull"), r)
			}
			if err != nil {
				return e.failAll(fc, acts, err, r)
			}
			if changes.CanonContent(remote.Root, s) != changes.CanonContent(fc.Base.Content, s) {
				if fc.Status == 'D' {
					r.Conflicts++
					fmt.Fprintf(e.Out, "  C  %s   changed on remote since base; run gfs pull\n", fc.Path)
					return nil
				}
				if o.NoMerge {
					return e.refuse(fc, remote, r)
				}
				res, err := merge.Merge(fc.Base.Content, fc.Local.Content, remote.Root, s, "remote v"+remote.Version)
				if err != nil {
					return e.failAll(fc, acts, fmt.Errorf("merge: %w", err), r)
				}
				if res.Conflicted() {
					return e.writeConflict(fc, remote, res, r)
				}
				content, merged = res.Root, true
			}
			if fc.Status != 'D' {
				if acts, err = e.checkAttachments(ctx, fc, remote, acts, r); err != nil {
					return err
				}
				if len(acts) == 0 {
					if fc.Quiet {
						return e.fastForward(fc, remote, content)
					}
					return nil
				}
			}
		}
		local := &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: content}
		if fc.Status == 'D' {
			local = remote
		}
		req := adapter.ApplyRequest{Local: local, Base: remote, Actions: acts, IDByPath: e.IDByPath,
			Open: e.open, Files: e.sidecarFiles(fc)}
		if remote != nil {
			req.Lock = remote.Version
		}
		results := e.Session.Apply(ctx, req)
		if len(results) > 0 && errors.Is(results[0].Err, adapter.ErrLock) && attempt == 0 {
			continue
		}
		return e.finish(ctx, fc, content, results, merged, r)
	}
}

func (e *Env) errorsFor(failed []adapter.Result) []envelope.Error {
	var out []envelope.Error
	for _, f := range failed {
		out = append(out, envelope.Error{Action: f.Action.Verb, Target: f.Action.Target, Code: f.Code,
			At: e.Now().Format(time.RFC3339), Msg: oneLine(f.Err)})
	}
	return out
}

func (e *Env) failAll(fc changes.FileChange, acts []adapter.Action, err error, r *Report) error {
	var results []adapter.Result
	for _, a := range acts {
		results = append(results, adapter.Result{Action: a, Err: err})
	}
	return e.finish(context.Background(), fc, nil, results, false, r)
}

func (e *Env) refuse(fc changes.FileChange, remote *adapter.Resource, r *Report) error {
	doc := *fc.Local
	doc.Conflict = &envelope.Conflict{RemoteVersion: remote.Version, By: remote.By, At: remote.At}
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   remote moved to v%s (--no-merge)\n", fc.Path, remote.Version)
	e.Log("merge", fc.Path, "", "FAIL", "remote moved to v"+remote.Version+" (--no-merge)")
	return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, e.Adapter.Schema()))
}

func (e *Env) writeConflict(fc changes.FileChange, remote *adapter.Resource, res merge.Result, r *Report) error {
	doc := &envelope.Doc{Action: fc.Local.Action, Params: fc.Local.Params, Conflict: &envelope.Conflict{
		RemoteVersion: remote.Version, By: remote.By, At: remote.At, Elements: res.Elements, Hunks: res.Hunks}}
	if err := e.Tree.WriteFile(fc.Path, []byte(envelope.Header(doc)+res.Text+"\n"+envelope.Footer)); err != nil {
		return err
	}
	base := *remote
	base.Path = fc.Path
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   conflict with remote v%s by %s %s, %s, %s\n", fc.Path, remote.Version, remote.By, remote.At,
		plural(res.Elements, "element"), plural(res.Hunks, "hunk"))
	e.Log("merge", fc.Path, "", "FAIL", "conflict with remote v"+remote.Version)
	return e.StoreBase(&base, fc.Entry.Path)
}

func (e *Env) finish(ctx context.Context, fc changes.FileChange, content *xmltree.Node, results []adapter.Result, merged bool, r *Report) error {
	s := e.Adapter.Schema()
	var failed []adapter.Result
	id := fc.ID
	moveFailed, deleted := false, false
	for _, res := range results {
		r.Actions++
		if res.Err != nil {
			r.Failed++
			failed = append(failed, res)
			moveFailed = moveFailed || res.Action.Verb == "move"
			continue
		}
		if res.ID != "" && !res.Action.IsAttachment() {
			id = res.ID
		}
		if res.Action.Verb == "delete" && res.Action.Target == "" {
			deleted = true
		}
	}
	report := func(newPath string) {
		for _, res := range results {
			a := res.Action
			outcome, detail := "ok", res.Detail
			if merged {
				outcome = "merged"
			}
			if a.IsAttachment() && a.Verb == "create" && detail == "" && res.ID != "" {
				detail = "id=" + res.ID
			}
			if res.Err != nil {
				outcome, detail = "FAIL", oneLine(res.Err)
			}
			np, target := "", a.Target
			if !a.IsAttachment() && (a.Verb == "move" || res.Err == nil) {
				np = newPath
			}
			if a.IsAttachment() {
				target = "" // the path already names the attachment
			}
			p := actPath(fc, a)
			e.line(a.Verb, p, np, outcome, strings.TrimSpace(target+" "+detail))
			e.Log(a.Verb, p, np, outcome, strings.TrimSpace(target+" "+detail))
		}
	}
	if len(failed) == len(results) {
		report("")
		if fc.Local == nil { // deleted file: nothing to annotate
			return nil
		}
		doc := *fc.Local
		doc.Errors, doc.Conflict = e.errorsFor(failed), nil
		return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, s))
	}
	if deleted {
		report("")
		return e.Forget(fc.ID, fc.Entry.Path)
	}
	remote, err := e.Session.Fetch(ctx, id)
	if err != nil {
		report("")
		r.Failed++
		e.line("fetch", fc.Path, "", "FAIL", "write-back: "+oneLine(err))
		return nil
	}
	wb := remote.Root.Clone()
	if fc.Quiet && content != nil {
		wb = e.withRemoteAttachments(content, remote.Root) // keep unselected local edits
	}
	doc := envelope.New(wb)
	path := remote.Path
	if len(failed) > 0 {
		keepLocal(wb, content, failed, s)
		doc.Errors = e.errorsFor(failed)
		if fc.Local.Action != "" {
			doc.Action, doc.Params = fc.Local.Action, fc.Local.Params
		}
		if moveFailed {
			path = fc.Path
		}
	}
	if err := e.Tree.WriteFile(path, envelope.Bytes(doc, s)); err != nil {
		return err
	}
	if path != fc.Path && e.Tree.Exists(fc.Path) {
		if err := e.Tree.Remove(fc.Path); err != nil {
			return err
		}
	}
	report(path)
	if err := e.StoreBase(remote, fc.Entry.Path); err != nil {
		return err
	}
	return e.syncCommitted(id, path, remote.Root, results)
}

// keepLocal puts the local form of failed parts back into the written-back tree,
// so that a retry is a plain re-commit.
func keepLocal(wb, local *xmltree.Node, failed []adapter.Result, s *schema.Schema) {
	if local == nil {
		return
	}
	for _, f := range failed {
		a := f.Action
		if a.IsAttachment() && a.Verb != "delete" {
			continue // the local file and its tracking line stay as they are
		}
		if a.Target == "" {
			if a.Verb == "update" && a.Group != "" {
				replaceGroup(wb, local, a.Group)
			}
			continue
		}
		name, id, nth, ok := changes.ParseTarget(a.Target)
		e := schema.Find(s.Elems, name)
		if !ok || e == nil {
			continue
		}
		switch a.Verb {
		case "create":
			i := 0
			for _, c := range local.ChildrenNamed(name) {
				if _, has := c.Attr(e.ID); !has {
					if i++; i == nth {
						wb.Children = append(wb.Children, c.Clone())
					}
				}
			}
		case "update":
			if old, nw := validate.FindSub(wb, name, e.ID, id), validate.FindSub(local, name, e.ID, id); old != nil && nw != nil {
				*old = *nw.Clone()
			}
		case "delete":
			old := validate.FindSub(wb, name, e.ID, id)
			var kept []*xmltree.Node
			for _, c := range wb.Children {
				if c != old {
					kept = append(kept, c)
				}
			}
			wb.Children = kept
		}
	}
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS, the existing commit tests included.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "engine: commit uploads, deletes and conflicts for attachments"
```

---

### Task 8: `engine.Pull` and `engine.Resolve` with attachments

Spec 4.5 and 4.6.

**Files:**
- Modify: `internal/engine/pull.go` (call `pullAttachments`; add it), `internal/engine/resolve.go` (dispatch to `resolveAttachment`; add it)
- Test: `internal/engine/pull_attach_test.go` (new)

**Interfaces:**
- Consumes: Task 6 helpers.
- Produces: `func (e *Env) pullAttachments(ctx context.Context, o PullOpts, say func(string, ...any), r *PullReport) error`; `func (e *Env) resolveAttachment(ctx context.Context, p string, ours bool) error`. `Resolve` accepts sidecar paths.

- [ ] **Step 1: Write the failing tests** `internal/engine/pull_attach_test.go`

```go
package engine

import (
	"strings"
	"testing"
)

func TestPullRefreshesFetchedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	before := ad.Remote.Downloads
	ad.Remote.EditAttachment("1", "10", []byte("new!"))
	r := pull(t, env, PullOpts{})
	if r.ExitCode() != 0 || !strings.Contains(out.String(), "  ~  a/one.files/x.png") || read(t, env, "a/one.files/x.png") != "new!" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if ad.Remote.Downloads != before+1 {
		t.Fatalf("exactly one download expected, got %d", ad.Remote.Downloads-before)
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "2" {
		t.Fatalf("%+v", l)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullNeverDownloadsUnfetched(t *testing.T) {
	env, ad, _ := attached(t)
	ad.Remote.EditAttachment("2", "20", []byte("changed"))
	pull(t, env, PullOpts{})
	if ad.Remote.Downloads != 0 || env.Tree.Exists("b/two.files") {
		t.Fatal("pull must not download attachments that were never fetched")
	}
	if !strings.Contains(read(t, env, "b/two.xml"), `size="7" version="2"`) {
		t.Fatal("the element must still be refreshed")
	}
}

func TestPullAttachmentConflictAndForce(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.EditAttachment("1", "10", []byte("rem"))
	write(t, env, "a/one.files/x.png", "mine")
	r := pull(t, env, PullOpts{})
	if r.Conflicts != 1 || !strings.Contains(out.String(), "  C  a/one.files/x.png   changed locally and on remote v2; remote copy: x.remote-v2.png") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if read(t, env, "a/one.files/x.png") != "mine" || read(t, env, "a/one.files/x.remote-v2.png") != "rem" {
		t.Fatal("both copies must be kept")
	}
	if l, _ := env.Atts.Get("1", "10"); l.Version != "1" {
		t.Fatal("the line stays at the old version until resolved")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Attachments[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
	out.Reset()
	pull(t, env, PullOpts{Force: true})
	if read(t, env, "a/one.files/x.png") != "rem" || env.Tree.Exists("a/one.files/x.remote-v2.png") {
		t.Fatalf("--force takes the remote and drops the copy\n%s", out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullAttachmentDeletedOnRemote(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x (2).png", "edited")
	ad.Remote.DeleteAttachment("1", "10")
	ad.Remote.DeleteAttachment("1", "11")
	r := pull(t, env, PullOpts{})
	if env.Tree.Exists("a/one.files/x.png") || !strings.Contains(out.String(), "  -  a/one.files/x.png   (deleted on remote)") {
		t.Fatalf("unchanged file must go\n%s", out)
	}
	if read(t, env, "a/one.files/x (2).png") != "edited" || r.Conflicts != 1 {
		t.Fatalf("changed file must stay as a conflict: %+v\n%s", r, out)
	}
}

func TestPullAttachmentRenamedOnRemote(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.RenameAttachment("1", "10", "y.png")
	pull(t, env, PullOpts{})
	if read(t, env, "a/one.files/y.png") != "abc" || read(t, env, "a/one.files/x.png") != "second" || env.Tree.Exists("a/one.files/x (2).png") {
		t.Fatalf("files must follow the derived names\n%s", out)
	}
	if !strings.Contains(out.String(), "  ~  a/one.files/x.png -> a/one.files/y.png") {
		t.Fatal(out)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestPullResourceMovedTakesSidecar(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	ad.Remote.Move("1", "c/one.xml")
	pull(t, env, PullOpts{})
	if read(t, env, "c/one.files/x.png") != "abc" || env.Tree.Exists("a/one.files") {
		t.Fatalf("sidecar must move with the resource\n%s", out)
	}
}

func TestPullResourceDeletedKeepsChangedAttachment(t *testing.T) {
	env, ad, out := attached(t)
	fetchAll(t, env, out)
	write(t, env, "a/one.files/x (2).png", "edited")
	ad.Remote.Delete("1")
	r := pull(t, env, PullOpts{})
	if env.Tree.Exists("a/one.xml") || env.Tree.Exists("a/one.files/x.png") || read(t, env, "a/one.files/x (2).png") != "edited" || r.Conflicts != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	cs := status(t, env)
	if len(cs) != 1 || !cs[0].Quiet || cs[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
}

func TestResolveAttachment(t *testing.T) {
	conflicted := func(t *testing.T) *Env {
		env, ad, out := attached(t)
		fetchAll(t, env, out)
		ad.Remote.EditAttachment("1", "10", []byte("rem"))
		write(t, env, "a/one.files/x.png", "mine")
		pull(t, env, PullOpts{})
		return env
	}
	env := conflicted(t)
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, false); err != nil {
		t.Fatal(err)
	}
	if read(t, env, "a/one.files/x.png") != "rem" || env.Tree.Exists("a/one.files/x.remote-v2.png") || len(status(t, env)) != 0 {
		t.Fatal("--theirs takes the remote copy")
	}
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, true); err == nil {
		t.Fatal("a clean attachment is not in conflict")
	}

	env = conflicted(t)
	if err := Resolve(ctx, env, []string{"a/one.files/x.png"}, true); err != nil {
		t.Fatal(err)
	}
	cs := status(t, env)
	if read(t, env, "a/one.files/x.png") != "mine" || env.Tree.Exists("a/one.files/x.remote-v2.png") ||
		len(cs) != 1 || cs[0].Attachments[0].Status != 'M' {
		t.Fatalf("--ours keeps the local file as a change: %+v", cs)
	}
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v", r)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run 'PullRefreshes|PullNever|PullAttachment|PullResource|ResolveAttachment'`
Expected: FAIL. `TestPullRefreshesFetchedAttachment` reports the file still `abc`, and `Resolve` reports `a/one.files/x.png: not in conflict`.

- [ ] **Step 3: Implement**

`internal/engine/pull.go`:

1. Add imports `"context"` (already there), `"path"`, `"sort"`, `"github.com/KrzysztofBogdan/gitfs/internal/attach"`, `"github.com/KrzysztofBogdan/gitfs/internal/workdir"`.
2. In `Pull`, a `Quiet` change means the resource file equals base, so pull treats the resource as unchanged locally. Its attachments are handled by `pullAttachments`. Change the `localByID` loop to:

```go
	for _, c := range cs {
		switch {
		case c.Status == 'D':
			deletedLocally[c.ID] = true
		case c.ID != "" && !c.Quiet:
			localByID[c.ID] = c
		}
	}
```

3. In `Pull`, just before `e.Index.Cursor = l.Cursor`:

```go
	if err := e.pullAttachments(ctx, o, say, &r); err != nil {
		return r, err
	}
```

4. Append:

```go
// pullAttachments refreshes fetched attachments against the updated bases
// (attachments spec 4.5). Attachments never fetched are never downloaded.
func (e *Env) pullAttachments(ctx context.Context, o PullOpts, say func(string, ...any), r *PullReport) error {
	el := e.attachmentElem()
	if el == nil {
		return nil
	}
	roots := map[string]*xmltree.Node{}
	rootOf := func(en workdir.Entry) (*xmltree.Node, error) {
		if root, ok := roots[en.ID]; ok {
			return root, nil
		}
		root, err := e.baseRoot(en)
		if err != nil {
			return nil, err
		}
		roots[en.ID] = root
		return root, nil
	}
	selected := func(l workdir.AttEntry) bool {
		if o.Filter == nil || o.Filter(l.Path) {
			return true
		}
		res, _ := attach.ResourceOf(l.Path)
		return o.Filter(res)
	}
	for _, l := range e.Atts.All() {
		if !selected(l) || !e.Tree.Exists(l.Path) {
			continue // evicted, or moved with its sidecar: placeAttachments below
		}
		var rel *xmltree.Node
		if en, ok := e.Index.ByID(l.ResID); ok {
			root, err := rootOf(en)
			if err != nil {
				return err
			}
			rel = attach.Elements(root, el)[l.AttID]
		}
		changed, err := attach.Changed(e.Tree, l.Path, l)
		if err != nil {
			return err
		}
		if rel == nil {
			if changed && !o.Force {
				r.Conflicts++
				say("  C  %s   deleted on remote; kept because it changed locally", l.Path)
				continue
			}
			if err := e.forgetAttachment(l.ResID, l.AttID, l.Path); err != nil {
				return err
			}
			e.removeConflictCopies(l.Path)
			r.Deleted++
			say("  -  %s   (deleted on remote)", l.Path)
			continue
		}
		v := attach.Version(rel, el)
		if v == l.Version || v == "-" {
			continue
		}
		if changed && !o.Force {
			cp := attach.ConflictCopy(l.Path, v)
			if !e.Tree.Exists(cp) {
				if _, err := e.download(ctx, l.ResID, l.AttID, cp); err != nil {
					return err
				}
			}
			r.Conflicts++
			say("  C  %s   changed locally and on remote v%s; remote copy: %s", l.Path, v, path.Base(cp))
			continue
		}
		line, err := e.download(ctx, l.ResID, l.AttID, l.Path)
		if err != nil {
			return err
		}
		e.Atts.Put(line)
		e.removeConflictCopies(l.Path)
		r.Updated++
		say("  ~  %s", l.Path)
	}
	ids := map[string]bool{}
	for _, l := range e.Atts.All() {
		ids[l.ResID] = true
	}
	var order []string
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		en, ok := e.Index.ByID(id)
		if !ok {
			continue
		}
		root, err := rootOf(en)
		if err != nil {
			return err
		}
		moves, err := e.placeAttachments(id, en.Path, root)
		if err != nil {
			return err
		}
		for _, m := range moves {
			r.Moved++
			say("  ~  %s -> %s", m[0], m[1])
		}
	}
	return e.saveAtts()
}
```

`internal/engine/resolve.go`:

1. Add imports `"github.com/KrzysztofBogdan/gitfs/internal/attach"` and `"github.com/KrzysztofBogdan/gitfs/internal/xmltree"`.
2. At the top of the `for _, p := range paths` loop body in `Resolve`:

```go
		if _, inSidecar := attach.ResourceOf(p); inSidecar {
			if err := e.resolveAttachment(ctx, p, ours); err != nil {
				return err
			}
			fmt.Fprintf(e.Out, "resolved %s (%s)\n", p, side)
			continue
		}
```

3. Append:

```go
// resolveAttachment picks one side of a conflicted attachment (attachments spec 4.6).
func (e *Env) resolveAttachment(ctx context.Context, p string, ours bool) error {
	el := e.attachmentElem()
	l, ok := e.Atts.ByPath(p)
	if el == nil || !ok {
		return fmt.Errorf("%s: not a tracked attachment", p)
	}
	changed, err := attach.Changed(e.Tree, p, l)
	if err != nil {
		return err
	}
	en, known := e.Index.ByID(l.ResID)
	var rel *xmltree.Node
	if known {
		root, err := e.baseRoot(en)
		if err != nil {
			return err
		}
		rel = attach.Elements(root, el)[l.AttID]
	}
	if rel == nil { // deleted on remote
		switch {
		case !changed:
			return fmt.Errorf("%s: not in conflict", p)
		case !ours:
			return e.forgetAttachment(l.ResID, l.AttID, p)
		case !known:
			return fmt.Errorf("%s: its resource is gone from the remote; move the file elsewhere to keep it", p)
		}
		e.Atts.Delete(l.ResID, l.AttID) // the file becomes a new attachment (A)
		return e.saveAtts()
	}
	v := attach.Version(rel, el)
	if v == l.Version || !changed {
		return fmt.Errorf("%s: not in conflict", p)
	}
	cp := attach.ConflictCopy(p, v)
	if !e.Tree.Exists(cp) {
		if _, err := e.download(ctx, l.ResID, l.AttID, cp); err != nil {
			return err
		}
	}
	if ours {
		remote, err := attach.Entry(e.Tree, l.ResID, l.AttID, v, cp)
		if err != nil {
			return err
		}
		remote.Path, remote.MTime = p, 0 // base = remote bytes, so the local file reads as changed
		e.Atts.Put(remote)
		if err := e.Tree.Remove(cp); err != nil {
			return err
		}
		return e.saveAtts()
	}
	if err := e.Tree.Rename(cp, p); err != nil {
		return err
	}
	line, err := attach.Entry(e.Tree, l.ResID, l.AttID, v, p)
	if err != nil {
		return err
	}
	e.Atts.Put(line)
	return e.saveAtts()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "engine: pull refreshes and resolve picks sides for attachments"
```

---
### Task 9: CLI — `gfs get`, attachment lines in `status`, `diff`, `actions`

**Files:**
- Create: `internal/cli/get.go`
- Modify: `internal/cli/status.go`, `internal/cli/diff.go`, `internal/cli/actions.go`, `internal/cli/root.go`
- Test: `internal/cli/cli_attach_test.go` (new)

**Interfaces:**
- Consumes: `engine.Get`, `FileChange.Attachments/Quiet`, `attach.Hash`, `Env.Atts`.
- Produces:
  - `gfs get <path>...`
  - `status` prints a resource line only when the change is not `Quiet`, then one line per attachment: `  <letter>  <path padded to 40> <detail>`
  - `diff` prints `Binary files a/<p> (<size>, <sha8>) and b/<p> (<size>, <sha8>) differ` for `M`/`C`, `Binary files /dev/null and b/<p> (...) differ` for `A`, `Binary files a/<p> and /dev/null differ` for `D`
  - `actions` ends with `attachments  <attachment>  create update delete` (or `read-only`)

- [ ] **Step 1: Write the failing test** `internal/cli/cli_attach_test.go`

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAttachments(t *testing.T) {
	fk.Remote.Put("7", "att/n.xml", `<note><title>N</title></note>`)
	fk.Remote.PutAttachment("7", "70", "x.png", []byte("abc"))
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt"))
	if _, err := os.Stat("att/n.files"); !os.IsNotExist(err) {
		t.Fatal("clone must not download attachments")
	}
	if out, code := gfs(t, "get"); code != 2 {
		t.Fatalf("get without a path is a usage error: %d\n%s", code, out)
	}
	if out, code := gfs(t, "get", "att/n.xml"); code != 0 || !strings.Contains(out, "  +  att/n.files/x.png   3 B   v1") {
		t.Fatalf("%d\n%s", code, out)
	}
	os.WriteFile("att/n.files/x.png", []byte("abcd"), 0o644)
	out, _ := gfs(t, "status")
	if !strings.Contains(out, "  M  att/n.files/x.png") || !strings.Contains(out, "update attachment[id=70]") || strings.Contains(out, "  M  att/n.xml") {
		t.Fatal(out)
	}
	out, _ = gfs(t, "diff", "att")
	if !strings.Contains(out, "Binary files a/att/n.files/x.png (3 B, ba7816bf) and b/att/n.files/x.png (4 B, ") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "actions"); !strings.Contains(out, "attachments  <attachment>  create update delete") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit", "att"); code != 0 || !strings.Contains(out, "update  att/n.files/x.png   ok") {
		t.Fatalf("%d\n%s", code, out)
	}
	if out, _ := gfs(t, "log"); !strings.Contains(out, "update  att/n.files/x.png  ok") {
		t.Fatal(out)
	}
	if data, v, _ := fk.Remote.Attachment("7", "70"); string(data) != "abcd" || v != 2 {
		t.Fatalf("%q v%d", data, v)
	}
}
```

(`fk` and `gfs` come from `cli_test.go`. Resource `7` only adds a file to other tests' clones of `fake://x`; none of them looks at it.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/ -run TestCLIAttachments`
Expected: FAIL, `unknown command "get" for "gfs"`.

- [ ] **Step 3: Implement**

`internal/cli/get.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get <path>...",
		Short: "Download attachment bytes on demand",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usage("gfs get needs a path: a resource file, a folder or an attachment ('gfs get .' for everything)")
			}
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			var rels []string
			for _, a := range args {
				r, err := env.Tree.Rel(a)
				if err != nil {
					return usage("%v", err)
				}
				rels = append(rels, r)
			}
			r, err := engine.Get(cmd.Context(), env, rels)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
}
```

`internal/cli/root.go`: register the command:

```go
	root.AddCommand(newClone(), newStatus(), newDiff(), newCommit(), newPull(), newResolve(), newLog(), newActions(), newGet())
```

`internal/cli/status.go`: replace `printChange` with:

```go
func printChange(w io.Writer, c changes.FileChange, pol *policy.Policy) {
	if !c.Quiet {
		printResource(w, c, pol)
	}
	for _, a := range c.Attachments {
		detail := a.Note
		if a.Action != nil {
			detail = a.Action.Detail + mark(pol, *a.Action)
		}
		fmt.Fprintf(w, "  %c  %-40s %s\n", a.Status, a.Path, detail)
	}
}

func printResource(w io.Writer, c changes.FileChange, pol *policy.Policy) {
	var acts []adapter.Action
	for _, a := range c.Actions {
		if !a.IsAttachment() {
			acts = append(acts, a)
		}
	}
	p := c.Path
	if c.OldPath != "" {
		p = c.OldPath + " -> " + c.Path
	}
	detail := ""
	switch {
	case c.Status == 'C':
		detail = "unresolved conflict"
	case c.Err != nil:
		detail = "invalid: " + strings.ReplaceAll(c.Err.Error(), "\n", "; ")
	case c.Status == '!' && c.Local != nil && len(c.Local.Errors) > 0:
		detail = "failed: " + c.Local.Errors[0].Msg
	case len(acts) == 1:
		detail = acts[0].Detail + mark(pol, acts[0])
	}
	if c.Note != "" {
		detail = strings.TrimSpace(detail + "  (" + c.Note + ")")
	}
	fmt.Fprintf(w, "  %c  %-40s %s\n", c.Status, p, detail)
	if len(acts) > 1 && c.Err == nil {
		for _, a := range acts {
			fmt.Fprintf(w, "        %s%s\n", a.Detail, mark(pol, a))
		}
	}
}
```

`internal/cli/diff.go` (whole file):

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/textdiff"
)

func newDiff() *cobra.Command {
	return &cobra.Command{
		Use:   "diff [<path>...]",
		Short: "Show resolved actions and the canonical diff against base",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cs, pol, err := computeChanges(env, args)
			if err != nil {
				return err
			}
			s := env.Adapter.Schema()
			w := cmd.OutOrStdout()
			for _, c := range cs {
				printChange(w, c, pol)
				if !c.Quiet {
					var before, after string
					if c.Base != nil {
						before = string(envelope.Bytes(c.Base, s))
					}
					switch {
					case c.Status == 'D':
					case c.Local != nil:
						after = string(envelope.Bytes(c.Local, s))
					default:
						raw, _ := env.Tree.ReadFile(c.Path)
						after = string(raw)
					}
					basePath := c.Path
					if c.OldPath != "" {
						basePath = c.OldPath
					}
					fmt.Fprint(w, textdiff.Unified("a/"+basePath, "b/"+c.Path, textdiff.Lines(before), textdiff.Lines(after)))
				}
				for _, a := range c.Attachments {
					if line := binaryDiff(env, c, a); line != "" {
						fmt.Fprintln(w, line)
					}
				}
			}
			return nil
		},
	}
}

// binaryDiff is git's notice for a changed attachment, with sizes and short
// hashes because no base bytes are kept (attachments spec 4.3).
func binaryDiff(env *engine.Env, c changes.FileChange, a changes.AttChange) string {
	now := func() string {
		sha, size, _, err := attach.Hash(env.Tree, a.Path)
		if err != nil {
			return "missing"
		}
		return fmt.Sprintf("%d B, %s", size, short(sha))
	}
	switch a.Status {
	case 'A':
		return fmt.Sprintf("Binary files /dev/null and b/%s (%s) differ", a.Path, now())
	case 'D':
		return fmt.Sprintf("Binary files a/%s and /dev/null differ", a.Path)
	case 'M', 'C':
		l, _ := env.Atts.Get(c.ID, a.AttID)
		return fmt.Sprintf("Binary files a/%s (%d B, %s) and b/%s (%s) differ", a.Path, l.Size, short(l.SHA), a.Path, now())
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
```

`internal/cli/actions.go`: after the `for _, v := range verbs` loop, before `return nil`:

```go
			if el := env.Adapter.Schema().Attachment(); el != nil {
				ops := strings.Join(el.Ops, " ")
				if ops == "" {
					ops = "read-only"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "attachments  <%s>  %s\n", el.Name, ops)
			}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "cli: gfs get; attachments in status, diff and actions"
```

---

### Task 10: Confluence — fake server attachments and streaming client

**Files:**
- Modify: `internal/adapter/confluence/cftest/server.go`, `internal/adapter/confluence/client.go` (replaced whole)
- Test: `internal/adapter/confluence/transfer_test.go` (new)

**Interfaces:**
- Produces (package `cftest`):
  - `type Attachment struct{ ID, PageID, Title, MediaType, AuthorID, CreatedAt string; Data []byte; Version int }`
  - `Server.Fail map[string]int`: `"METHOD /path"` → HTTP status returned instead of handling the request
  - `AddAttachment(a Attachment) *Attachment` (defaults: `ID` `att<n>`, `Version` 1, `AuthorID` `me`, `CreatedAt` `2026-03-01T10:00:00.000Z`, `MediaType` `application/octet-stream`), `EditAttachment(id string, data []byte)` (version+1, author `bob`, `CreatedAt` = server clock), `Attachment(id string) (*Attachment, bool)`, `Attachments(pageID string) []*Attachment` (sorted by id)
  - Endpoints: `GET /wiki/api/v2/pages/{id}/attachments` (paged), `GET /wiki/api/v2/attachments/{id}`, `DELETE /wiki/api/v2/attachments/{id}`, `GET /wiki/download/attachments/{page}/{name}` (302 to `/media/{id}`), `GET /media/{id}`, `POST /wiki/rest/api/content/{id}/child/attachment` and `POST /wiki/rest/api/content/{id}/child/attachment/{att}/data` (multipart part `file`, header `X-Atlassian-Token: no-check` or 403; a duplicate title on create is 400)
- Produces (package `confluence`):
  - `func apiError(resp *http.Response) error`, `func (c *client) newRequest(...)`
  - `func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error)`
  - `func (c *client) upload(ctx context.Context, path, filename string, r io.Reader, out any) error`
  - `client.xfer`: an `http.Client` without timeout for attachment bytes

- [ ] **Step 1: Write the failing tests** `internal/adapter/confluence/transfer_test.go`

```go
package confluence

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func TestAttachmentEndpoints(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", SpaceID: "100", Storage: "<p/>"})
	c := newClient(target{base: srv.URL, space: "ENG", email: "me@x.com", token: "t"})

	var created struct {
		Results []struct {
			ID      string `json:"id"`
			Version struct{ Number int } `json:"version"`
		} `json:"results"`
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment", "rollback flow.png", strings.NewReader("png1"), &created); err != nil || len(created.Results) != 1 {
		t.Fatalf("%+v %v", created, err)
	}
	id := created.Results[0].ID
	a, ok := srv.Attachment(id)
	if !ok || a.Title != "rollback flow.png" || string(a.Data) != "png1" || a.MediaType != "image/png" || a.Version != 1 {
		t.Fatalf("%+v", a)
	}
	var meta struct {
		FileSize     int64  `json:"fileSize"`
		DownloadLink string `json:"downloadLink"`
	}
	if err := c.do(bg, http.MethodGet, "/wiki/api/v2/attachments/"+id, nil, &meta); err != nil || meta.FileSize != 4 || meta.DownloadLink == "" {
		t.Fatalf("%+v %v", meta, err)
	}
	var buf bytes.Buffer
	if n, err := c.download(bg, "/wiki"+meta.DownloadLink, &buf); err != nil || n != 4 || buf.String() != "png1" {
		t.Fatalf("%d %v %q", n, err, buf.String())
	}
	if !slices.Contains(srv.Requests, "GET /media/"+id) {
		t.Fatalf("download must follow the redirect: %v", srv.Requests)
	}
	var updated struct {
		Version struct{ Number int } `json:"version"`
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment/"+id+"/data", "rollback flow.png", strings.NewReader("png2"), &updated); err != nil || updated.Version.Number != 2 {
		t.Fatalf("%+v %v", updated, err)
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment", "rollback flow.png", strings.NewReader("dup"), nil); codeOf(err) != "400" {
		t.Fatalf("duplicate title must be refused: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wiki/rest/api/content/98130/child/attachment", strings.NewReader(""))
	req.SetBasicAuth("me@x.com", "t")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("missing XSRF header must be 403: %v %v", resp, err)
	}
	if err := c.do(bg, http.MethodDelete, "/wiki/api/v2/attachments/"+id, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Attachment(id); ok {
		t.Fatal("not deleted")
	}
	srv.Fail = map[string]int{"GET /wiki/api/v2/pages/98130/attachments": 500}
	err := c.paginate(bg, "/wiki/api/v2/pages/98130/attachments?limit=250", func(json.RawMessage) error { return nil })
	if codeOf(err) != "500" {
		t.Fatalf("fault injection: %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestTransfersStream(t *testing.T) {
	const size = 64 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				io.Copy(io.Discard, p)
			}
			w.Write([]byte(`{}`))
			return
		}
		io.CopyN(w, zeros{}, size)
	}))
	defer srv.Close()
	c := newClient(target{base: srv.URL, email: "e", token: "t"})
	measure := func(name string, f func() error) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runtime.ReadMemStats(&after)
		if d := after.TotalAlloc - before.TotalAlloc; d > 16<<20 {
			t.Errorf("%s allocated %d MB for %d MB", name, d>>20, size>>20)
		}
	}
	measure("download", func() error {
		n, err := c.download(bg, "/blob", io.Discard)
		if err == nil && n != size {
			t.Errorf("downloaded %d bytes", n)
		}
		return err
	})
	measure("upload", func() error { return c.upload(bg, "/blob", "big.bin", io.LimitReader(zeros{}, size), nil) })
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/ -run 'AttachmentEndpoints|TransfersStream'`
Expected: FAIL, `c.upload undefined`.

- [ ] **Step 3: Implement**

`internal/adapter/confluence/client.go` (whole file):

```go
package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var m struct{ Message string }
	if json.Unmarshal([]byte(e.Body), &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return fmt.Sprintf("confluence: HTTP %d: %s", e.Status, msg)
}

func codeOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return strconv.Itoa(ae.Status)
	}
	return ""
}

type client struct {
	t    target
	hc   *http.Client // JSON calls, with a timeout
	xfer *http.Client // attachment bytes: no timeout, the context cancels
}

func newClient(t target) *client {
	return &client{t: t, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{}}
}

func (c *client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.t.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.t.email != "" || c.t.token != "" {
		req.SetBasicAuth(c.t.email, c.t.token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// apiError maps an HTTP error response: 404 wraps adapter.ErrNotFound and 409
// adapter.ErrLock, both also wrapping the *APIError.
func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ae := &APIError{Status: resp.StatusCode, Body: string(data)}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
	}
	return ae
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *client) paginate(ctx context.Context, path string, each func(json.RawMessage) error) error {
	for path != "" {
		var resp struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		for _, r := range resp.Results {
			if err := each(r); err != nil {
				return err
			}
		}
		path = resp.Links.Next
	}
	return nil
}

// download streams the body of GET path into w; redirects are followed.
func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.xfer.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, apiError(resp)
	}
	return io.Copy(w, resp.Body)
}

// upload POSTs r as the multipart "file" part named filename, streaming it,
// with the XSRF header Confluence requires, and decodes the JSON reply into out.
func (c *client) upload(ctx context.Context, path, filename string, r io.Reader, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() { pw.CloseWithError(writeMultipart(mw, filename, r)) }()
	req, err := c.newRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	resp, err := c.xfer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func writeMultipart(mw *multipart.Writer, filename string, r io.Reader) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	if err := mw.WriteField("minorEdit", "true"); err != nil {
		return err
	}
	return mw.Close()
}
```

`internal/adapter/confluence/cftest/server.go`:

1. Add imports `"io"` and `"net/url"`.
2. Add the type after `Comment`:

```go
type Attachment struct {
	ID, PageID, Title, MediaType, AuthorID, CreatedAt string
	Data                                              []byte
	Version                                           int
}
```

3. In `Server`, add the exported field after `Requests`, and the private map after `comments`:

```go
	Fail      map[string]int // "METHOD /path" -> status returned instead of handling the request
```

```go
	attachments map[string]*Attachment
```

4. In `New`, initialise `attachments: map[string]*Attachment{}` in the struct literal and register the handlers after the existing ones:

```go
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/attachments", s.listAttachments)
	mux.HandleFunc("GET /wiki/api/v2/attachments/{id}", s.getAttachment)
	mux.HandleFunc("DELETE /wiki/api/v2/attachments/{id}", s.deleteAttachment)
	mux.HandleFunc("GET /wiki/download/attachments/{page}/{name}", s.downloadRedirect)
	mux.HandleFunc("GET /media/{id}", s.media)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/child/attachment", s.createAttachment)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/child/attachment/{att}/data", s.updateAttachment)
```

5. In the `httptest.NewServer` handler, after the Basic-auth check and before `mux.ServeHTTP(w, r)`:

```go
		if code := s.Fail[r.Method+" "+r.URL.Path]; code != 0 {
			fail(w, code, "injected failure")
			return
		}
```

6. Append helpers and handlers:

```go
func (s *Server) AddAttachment(a Attachment) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = "att" + s.next()
	}
	if a.Version == 0 {
		a.Version = 1
	}
	if a.AuthorID == "" {
		a.AuthorID = "me"
	}
	if a.CreatedAt == "" {
		a.CreatedAt = "2026-03-01T10:00:00.000Z"
	}
	if a.MediaType == "" {
		a.MediaType = "application/octet-stream"
	}
	s.attachments[a.ID] = &a
	return &a
}

// EditAttachment replaces an attachment's bytes as another user would.
func (s *Server) EditAttachment(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attachments[id]
	a.Data, a.Version, a.AuthorID, a.CreatedAt = data, a.Version+1, "bob", s.now
}

func (s *Server) Attachment(id string) (*Attachment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[id]
	return a, ok
}

func (s *Server) Attachments(pageID string) []*Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachmentsOf(pageID)
}

func (s *Server) attachmentsOf(pageID string) []*Attachment {
	var out []*Attachment
	for _, a := range s.attachments {
		if a.PageID == pageID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *Attachment) json() map[string]any {
	return map[string]any{"id": a.ID, "status": "current", "title": a.Title, "pageId": a.PageID,
		"mediaType": a.MediaType, "fileSize": len(a.Data),
		"downloadLink": fmt.Sprintf("/download/attachments/%s/%s?version=%d&api=v2", a.PageID, url.PathEscape(a.Title), a.Version),
		"version":      map[string]any{"number": a.Version, "authorId": a.AuthorID, "createdAt": a.CreatedAt}}
}

// v1json is the shape of the REST v1 upload responses.
func (a *Attachment) v1json() map[string]any {
	return map[string]any{"id": a.ID, "type": "attachment", "title": a.Title, "version": map[string]any{"number": a.Version}}
}

func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for _, a := range s.attachmentsOf(r.PathValue("id")) {
		items = append(items, a.json())
	}
	s.paged(w, r, items)
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "attachment not found")
		return
	}
	writeJSON(w, 200, a.json())
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.attachments[r.PathValue("id")]; !ok {
		fail(w, 404, "attachment not found")
		return
	}
	delete(s.attachments, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) downloadRedirect(w http.ResponseWriter, r *http.Request) {
	for _, a := range s.attachments {
		if a.PageID == r.PathValue("page") && a.Title == r.PathValue("name") {
			http.Redirect(w, r, "/media/"+a.ID, http.StatusFound)
			return
		}
	}
	fail(w, 404, "attachment not found")
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "attachment not found")
		return
	}
	w.Header().Set("Content-Type", a.MediaType)
	w.Write(a.Data)
}

// readUpload reads the multipart "file" part of an upload request.
func readUpload(w http.ResponseWriter, r *http.Request) (name, mediaType string, data []byte, ok bool) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		fail(w, 403, "XSRF check failed")
		return "", "", nil, false
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, err.Error())
		return "", "", nil, false
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, err.Error())
			return "", "", nil, false
		}
		if part.FormName() == "file" {
			name, mediaType = part.FileName(), part.Header.Get("Content-Type")
			data, _ = io.ReadAll(part)
		}
	}
	if name == "" {
		fail(w, 400, "a file part is required")
		return "", "", nil, false
	}
	return name, mediaType, data, true
}

func (s *Server) createAttachment(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	if _, ok := s.pages[pageID]; !ok {
		fail(w, 404, "page not found")
		return
	}
	name, mediaType, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	for _, a := range s.attachmentsOf(pageID) {
		if a.Title == name {
			fail(w, 400, "Cannot add a new attachment with same file name as an existing attachment: "+name)
			return
		}
	}
	a := &Attachment{ID: "att" + s.next(), PageID: pageID, Title: name, MediaType: mediaType, Data: data,
		Version: 1, AuthorID: "me", CreatedAt: s.now}
	s.attachments[a.ID] = a
	writeJSON(w, 200, map[string]any{"results": []any{a.v1json()}})
}

func (s *Server) updateAttachment(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("att")]
	if !ok || a.PageID != r.PathValue("id") {
		fail(w, 404, "attachment not found")
		return
	}
	_, mediaType, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	a.Data, a.MediaType, a.Version, a.AuthorID, a.CreatedAt = data, mediaType, a.Version+1, "me", s.now
	writeJSON(w, 200, a.v1json())
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/... -v -run 'Attachment|Transfers|ErrorMapping|Paginate' && go test ./... && go vet ./... && gofmt -l .`
Expected: PASS; the existing `TestErrorMapping` still passes on the refactored `do`.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: attachment endpoints in cftest; streaming upload and download"
```

---

### Task 11: Confluence adapter attachments

Spec 7.

**Files:**
- Modify: `internal/adapter/confluence/attachments.go` (replaced whole, drops the Task 4 stub), `schema.go`, `session.go` (Fetch, Check, Apply, create), `adapter.go` (Describe), `paths.go` (sanitize)
- Test: `internal/adapter/confluence/session_attach_test.go` (new)

**Interfaces:**
- Consumes: Task 10 client and cftest; `adapter.ParseAttachmentTarget`, `Action.IsAttachment`; `attach.SidecarSuffix`.
- Produces: `type apiAttachment`, `attachmentNodes`, `(*session).listAttachments`, `(*session).Download`, `(*session).attachment`, `checkAttachment`. `<attachment id name type size version created author>` in every page.

- [ ] **Step 1: Write the failing tests** `internal/adapter/confluence/session_attach_test.go`

```go
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
	srv, sess := space(t)
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
	srv, sess := space(t)
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
	srv, sess := space(t)
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
```

(`space`, `resource`, `known`, `bg` come from `session_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/ -run 'AttachmentsFetch|CreatePageWith|AttachmentCheck|ReservesFiles'`
Expected: FAIL: the page XML has no `<attachment>`, and `Download` returns "attachments are not supported yet".

- [ ] **Step 3: Implement**

`internal/adapter/confluence/attachments.go` (whole file, replacing the Task 4 stub):

```go
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiAttachment struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	MediaType    string     `json:"mediaType"`
	FileSize     int64      `json:"fileSize"`
	DownloadLink string     `json:"downloadLink"`
	Version      apiVersion `json:"version"`
}

// v1Attachment is the attachment shape of the REST v1 upload responses.
type v1Attachment struct {
	ID      string `json:"id"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
}

// attachmentNodes renders a page's attachments as <attachment> elements (attachments spec 7).
func attachmentNodes(atts []apiAttachment, name func(string) string) []*xmltree.Node {
	var out []*xmltree.Node
	for _, a := range atts {
		author := name(a.Version.AuthorID)
		if author == "" {
			author = a.Version.AuthorID
		}
		out = append(out, el("attachment", "id", a.ID, "name", a.Title, "type", a.MediaType,
			"size", strconv.FormatInt(a.FileSize, 10), "version", strconv.Itoa(a.Version.Number),
			"created", a.Version.CreatedAt, "author", author))
	}
	return out
}

func (s *session) listAttachments(ctx context.Context, pageID string) ([]apiAttachment, error) {
	var out []apiAttachment
	err := s.c.paginate(ctx, "/wiki/api/v2/pages/"+pageID+"/attachments?limit=250", func(raw json.RawMessage) error {
		var a apiAttachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

// Download streams an attachment's current bytes: its metadata names the
// download link, which Confluence redirects to the media store.
func (s *session) Download(ctx context.Context, _ string, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	var a apiAttachment
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/attachments/"+url.PathEscape(attID), nil, &a); err != nil {
		return adapter.AttachmentInfo{}, err
	}
	if a.DownloadLink == "" {
		return adapter.AttachmentInfo{}, fmt.Errorf("confluence: attachment %s has no download link", attID)
	}
	n, err := s.c.download(ctx, "/wiki"+a.DownloadLink, w)
	return adapter.AttachmentInfo{Version: strconv.Itoa(a.Version.Number), Size: n}, err
}

// attachment executes one attachment action on page pageID. Uploads carry no
// version lock; commit checks the remote version just before (spec 4.4, 7).
func (s *session) attachment(ctx context.Context, pageID string, req adapter.ApplyRequest, a adapter.Action) (id, version string, err error) {
	attID, _, ok := adapter.ParseAttachmentTarget(a.Target)
	if !ok {
		return "", "", fmt.Errorf("confluence has no attachment target %q", a.Target)
	}
	upload := func(apiPath string, out any) error {
		if req.Open == nil {
			return errors.New("confluence: no file reader for uploads")
		}
		f, err := req.Open(a.File)
		if err != nil {
			return err
		}
		defer f.Close()
		return s.c.upload(ctx, apiPath, path.Base(a.File), f, out)
	}
	switch a.Verb {
	case "create":
		var resp struct {
			Results []v1Attachment `json:"results"`
		}
		if err := upload("/wiki/rest/api/content/"+pageID+"/child/attachment", &resp); err != nil {
			return "", "", err
		}
		if len(resp.Results) == 0 {
			return "", "", errors.New("confluence: upload returned no attachment")
		}
		return resp.Results[0].ID, strconv.Itoa(resp.Results[0].Version.Number), nil
	case "update":
		var resp v1Attachment
		if err := upload("/wiki/rest/api/content/"+pageID+"/child/attachment/"+attID+"/data", &resp); err != nil {
			return "", "", err
		}
		return attID, strconv.Itoa(resp.Version.Number), nil
	case "delete":
		return attID, "", s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/attachments/"+url.PathEscape(attID), nil, nil)
	}
	return "", "", fmt.Errorf("confluence has no action %q on %s", a.Verb, a.Target)
}

// checkAttachment is the dry-run check of one attachment action: Confluence
// refuses a second attachment with the same name on a page.
func checkAttachment(req adapter.ApplyRequest, a adapter.Action) error {
	if a.Verb != "create" || req.Local == nil || req.Local.Root == nil {
		return nil
	}
	_, file, _ := adapter.ParseAttachmentTarget(a.Target)
	for _, c := range req.Local.Root.ChildrenNamed("attachment") {
		if n, _ := c.Attr("name"); n == file {
			return fmt.Errorf("an attachment named %q already exists on this page; edit its file instead", file)
		}
	}
	return nil
}
```

`internal/adapter/confluence/schema.go`: append to `Elems`, after `comment`:

```go
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Ops: []string{"create", "update", "delete"},
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "type", ReadOnly: true},
				{Name: "size", ReadOnly: true}, {Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true},
				{Name: "author", ReadOnly: true}}},
```

`internal/adapter/confluence/session.go`:

1. In `Fetch`, after the footer-comments `paginate` block and before `pageNode`:

```go
	atts, err := s.listAttachments(ctx, id)
	if err != nil {
		return nil, err
	}
```

and after `root, err := pageNode(...)` and its error check:

```go
	root.Children = append(root.Children, attachmentNodes(atts, func(a string) string { return s.name(ctx, a) })...)
```

2. In `Check`, make attachment actions the first case of the `switch`:

```go
		case a.IsAttachment():
			res.Err = checkAttachment(req, a)
```

3. In `Apply`, declare `var pageIdx, labelIdx, commentIdx, attIdx []int`, make attachment actions the first case of the `switch`:

```go
		case a.IsAttachment():
			attIdx = append(attIdx, i)
```

and after the `commentIdx` loop, before `return out`:

```go
	for _, i := range attIdx {
		out[i].ID, out[i].Version, out[i].Err = s.attachment(ctx, id, req, req.Actions[i])
		out[i].Code = codeOf(out[i].Err)
	}
```

4. In `create`, after the loop that posts new comments and before `return out`:

```go
	for _, a := range req.Actions {
		if !a.IsAttachment() {
			continue
		}
		attID, ver, err := s.attachment(ctx, p.ID, req, a)
		out = append(out, adapter.Result{Action: a, ID: attID, Version: ver, Err: err, Code: codeOf(err)})
	}
```

`internal/adapter/confluence/adapter.go`, in `Describe`, add as the first case of the `switch`:

```go
	case a.IsAttachment():
		switch a.Verb {
		case "create":
			title := titleOf(local.Root)
			if title == "" {
				title = baseName(local.Path)
			}
			a.Detail = fmt.Sprintf("attach to %q", title)
		case "update":
			a.Detail = "upload new version of attachment"
		case "delete":
			a.Detail = "delete attachment"
		default:
			a.Detail = a.Verb + " attachment (not supported by confluence)"
		}
```

`internal/adapter/confluence/paths.go`: import `"github.com/KrzysztofBogdan/gitfs/internal/attach"` and, in `sanitize`, just before the final `return s`:

```go
	if strings.HasSuffix(s, attach.SidecarSuffix) {
		s += "_" // a page folder must never look like a sidecar
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS. Existing Confluence tests see one more request per fetched page (the attachment list, empty for them) and no XML change.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: page attachments: list, download, upload, update, delete"
```

---

### Task 12: End-to-end test and docs

**Files:**
- Create: `internal/cli/confluence_attach_e2e_test.go`
- Modify: `Readme.md` (`# Current repo state` section only)

**Interfaces:**
- Consumes: everything.

- [ ] **Step 1: Write the end-to-end test** `internal/cli/confluence_attach_e2e_test.go`

```go
package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func TestConfluenceAttachmentsEndToEnd(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Home</p>"})
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	att := srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "rollback-flow.png", MediaType: "image/png", Data: []byte("v1")})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?base="+srv.URL, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	img := "eng/Home/Runbooks.files/rollback-flow.png"
	readFile := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
	if _, err := os.Stat(img); !os.IsNotExist(err) {
		t.Fatal("clone must not download attachments")
	}
	mustRun(t, 0, "get", "eng/Home/Runbooks.xml")
	if readFile(img) != "v1" {
		t.Fatal("get")
	}

	os.WriteFile(img, []byte("v2-local"), 0o644)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "  M  "+img) || !strings.Contains(out, "upload new version of attachment") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit")
	if a, _ := srv.Attachment(att.ID); string(a.Data) != "v2-local" || a.Version != 2 {
		t.Fatalf("%+v", a)
	}

	srv.EditAttachment(att.ID, []byte("v3-remote"))
	os.WriteFile(img, []byte("mine"), 0o644)
	if out := mustRun(t, 1, "pull"); !strings.Contains(out, "  C  "+img) {
		t.Fatal(out)
	}
	if readFile("eng/Home/Runbooks.files/rollback-flow.remote-v3.png") != "v3-remote" || readFile(img) != "mine" {
		t.Fatal("pull must keep both copies")
	}
	mustRun(t, 0, "resolve", "--theirs", img)
	if readFile(img) != "v3-remote" {
		t.Fatal("resolve --theirs")
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	os.WriteFile("eng/Home/Runbooks.files/oncall.csv", []byte("week,primary\n"), 0o644)
	if out := mustRun(t, 0, "commit"); !strings.Contains(out, "create  eng/Home/Runbooks.files/oncall.csv   ok  id=att") {
		t.Fatal(out)
	}
	if as := srv.Attachments("98130"); len(as) != 2 {
		t.Fatalf("%+v", as)
	}

	page := "eng/Home/Runbooks.xml"
	re := regexp.MustCompile(`(?m)^\s*<attachment id="` + att.ID + `"[^\n]*\n`)
	os.WriteFile(page, re.ReplaceAll([]byte(readFile(page)), nil), 0o644)
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "--allow delete") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit", "--allow", "delete")
	if _, ok := srv.Attachment(att.ID); ok {
		t.Fatal("not deleted on the server")
	}
	if _, err := os.Stat(img); !os.IsNotExist(err) {
		t.Fatal("the local file must be gone")
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
}
```

(`mustRun` comes from `confluence_e2e_test.go`.)

- [ ] **Step 2: Run it**

Run: `go test ./internal/cli/ -run ConfluenceAttachmentsEndToEnd -v`
Expected: PASS. A failure here is an integration bug in an earlier task: fix it there, with a unit test that reproduces it, rather than special-casing this test.

- [ ] **Step 3: Update the README's "Current repo state" section**

In `Readme.md`, under `# Current repo state`:

1. Replace the sentence `three-way merge, policy) and one adapter, **Confluence Cloud**.` with:

```markdown
three-way merge, policy) and one adapter, **Confluence Cloud**. Attachments are listed in every page and
downloaded on demand with `gfs get` (`docs/superpowers/specs/2026-09-23-gfs-attachments-design.md`).
```

2. In the shell block, after the `cd confluence` line, add:

```shell
gfs get eng/Home/Runbooks.xml      # download a page's attachments into eng/Home/Runbooks.files/
```

Do not touch the rest of the file.

- [ ] **Step 4: Full verification**

Run: `gofmt -l . && go vet ./... && go test -count=1 ./... && go build -o "$TMPDIR/gfs" ./cmd/gfs && "$TMPDIR/gfs" --help`
Expected: `gofmt` prints nothing, all tests PASS (the corpus test SKIPs), and help lists `get` among the commands. Set `TMPDIR` to the session scratchpad if it is not set.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/confluence_attach_e2e_test.go Readme.md
git commit -m "Attachments end-to-end test through the CLI; README mentions gfs get"
```

---

## Self-review notes (for the executor)

- Spec coverage: 3.1 (Task 1 validate), 3.2 (Task 3 Derive, Task 11 `.files` suffix), 3.3 (Tasks 2–3), 3.4 (Task 5 table test), 3.5 (Task 5 rename warning and moved sidecar, Task 6 `placeAttachments`), 4.1 (Task 6), 4.2–4.3 and 4.7 (Task 9), 4.4 (Task 7), 4.5–4.6 (Task 8), 5.3 (Task 3 names; Tasks 7–8 write copies), 5.4 (Task 6 `dropAttachments`, Task 8 tests), 5.5 (Task 2 and Task 10 streaming tests, Task 5 `MaxSize`), 6 (Task 4), 7 (Tasks 10–11), 8 (every task's tests).
- Not implemented, by the spec's own scope: attachments nested in sub-resources (Slack), automatic download rules, local attachment renames (warned about), inline `ri:attachment` references.
- Known limitations to report after execution:
  - The Confluence upload endpoints take no version lock, so a remote edit landing between commit's check and the upload is overwritten. The check narrows the window; it cannot close it.
  - `gfs get` parses every working file to find locally moved resources, so it takes longer on very large trees.
  - `pull` downloads refreshed attachments one at a time.
- Run the opt-in corpus check (`GFS_CORPUS_URL`) once against a scratch space that has attachments: the round trip must also leave the attachment elements unchanged.
