# Parent and children in one commit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One `gfs commit` creates a new parent and its new children: Confluence page trees (any depth) and Jira issues naming a new parent by `<parent file="…"/>`.

**Architecture:** Adapters may implement `adapter.Dependent` to name the files a resource needs created first. Commit orders new files topologically, keeps a per-commit `renamed` map (write-back moves) and, in dry runs, a `pending` set so `IDByPath` resolves files the commit creates. Children of a parent that was not created are failed without sending anything.

**Tech Stack:** Go; fakes `internal/adapter/fake`, `confluence/cftest`, `jira/jtest`.

**Spec:** `docs/superpowers/specs/2026-10-06-parent-child-create-design.md`

## Global Constraints

- Commits without dependencies run in exactly today's order and output.
- Messages, verbatim: `parent cycle: a -> b -> a`; `parent X is not tracked and not part of this commit`; `parent X was not created`; `<parent> takes a key or file="…", not both`; `<parent file> names a file in this project folder`; `parent file X is not an issue in this tree`; dry-run suffixes ` (created in this commit)`.
- `adapter.Pending` is never sent to a service.
- Run `go test ./...` and `go vet ./...` before every commit.

---

### Task 1: Dependency ordering in commit

**Files:**
- Modify: `internal/adapter/adapter.go` (add `Dependent`, `Pending`)
- Modify: `internal/adapter/fake/fake.go` (test hooks)
- Create: `internal/engine/depend.go`
- Modify: `internal/engine/commit.go` (`Commit` loop)
- Test: `internal/engine/depend_test.go`

**Interfaces:**
- Produces: `adapter.Dependent{ DependsOn(local *adapter.Resource) []string }`, `adapter.Pending = "(pending)"`; fake `Adapter.Deps func(*adapter.Resource) []string`, `Remote.Parent func(string) string`, `Remote.Parents map[string]string` (record id → parent id), `Remote.CreatePath map[string]string`, `Remote.FailCreate map[string]error`; engine `orderCreates(cs, deps) ([]changes.FileChange, map[string]string)`, `(*Env).dependencies(cs) map[string][]string`.

- [ ] **Step 1: adapter.go** — after `BaseDescriber`:

```go
// Dependent is implemented by adapters whose new resources need another
// resource created first (a parent page, a parent issue). Paths are
// working-tree paths; commit creates those files first.
type Dependent interface{ DependsOn(local *Resource) []string }

// Pending is the id IDByPath returns during a dry run for a file the same
// commit would create. Adapters never send it to the service.
const Pending = "(pending)"
```

- [ ] **Step 2: fake hooks** — `Adapter` gets `Deps func(local *adapter.Resource) []string` and

```go
func (a *Adapter) DependsOn(local *adapter.Resource) []string {
	if a.Deps == nil {
		return nil
	}
	return a.Deps(local)
}
```

`Remote` gets:

```go
	Parent     func(path string) string // create: the working path whose id becomes the new record's parent ("" none)
	Parents    map[string]string        // record id -> parent id, set on create
	CreatePath map[string]string        // create: working path -> remote path (a title-derived rename)
	FailCreate map[string]error         // create of this working path fails
```

and session helper:

```go
// parentID resolves the parent Remote.Parent names for a new file ("" none).
func (s session) parentID(req adapter.ApplyRequest) (string, error) {
	if s.r.Parent == nil {
		return "", nil
	}
	pp := s.r.Parent(req.Local.Path)
	if pp == "" {
		return "", nil
	}
	if id, ok := req.IDByPath(pp); ok {
		return id, nil
	}
	return "", fmt.Errorf("parent %s not found", pp)
}
```

In `Apply`, case `a.Verb == "create" && a.Target == ""` starts with:

```go
			if err := s.r.FailCreate[req.Local.Path]; err != nil {
				res.Err, res.Code = err, "500"
				out = append(out, res)
				continue
			}
			pid, err := s.parentID(req)
			if err != nil {
				res.Err = err
				out = append(out, res)
				continue
			}
```

then uses `p := req.Local.Path; if np, ok := s.r.CreatePath[p]; ok { p = np }` for `record.path`, and after `res.ID = id`:

```go
			if pid != "" {
				if s.r.Parents == nil {
					s.r.Parents = map[string]string{}
				}
				s.r.Parents[id] = pid
			}
```

In `Check`, for a resource create with no fault: `pid, err := s.parentID(req)`; err → `res.Err`; `pid == adapter.Pending` → `res.Detail = "under a new parent"`.

- [ ] **Step 3: failing tests** — `internal/engine/depend_test.go`:

```go
package engine

import (
	"path"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

// treeParent is a Confluence-like parent: a/p/c.xml needs a/p.xml.
func treeParent(p string) string {
	d := path.Dir(p)
	if !strings.Contains(d, "/") {
		return ""
	}
	return d + ".xml"
}

// nested makes the fake tree-shaped: a new file depends on, and is created
// under, the file of its folder.
func nested(ad *fake.Adapter) {
	ad.Deps = func(l *adapter.Resource) []string {
		if p := treeParent(l.Path); p != "" {
			return []string{p}
		}
		return nil
	}
	ad.Remote.Parent = treeParent
}

func noteXML(title string) string { return "<note><title>" + title + "</title></note>" }

func idOf(t *testing.T, env *Env, p string) string {
	t.Helper()
	en, ok := env.Index.ByPath(p)
	if !ok {
		t.Fatalf("%s is not tracked", p)
	}
	return en.ID
}

func TestCommitCreatesParentFirst(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Deps = func(l *adapter.Resource) []string {
		if l.Path == "a/1 child.xml" {
			return []string{"a/2 parent.xml"}
		}
		return nil
	}
	ad.Remote.Parent = func(p string) string {
		if p == "a/1 child.xml" {
			return "a/2 parent.xml"
		}
		return ""
	}
	write(t, env, "a/1 child.xml", noteXML("Child"))
	write(t, env, "a/2 parent.xml", noteXML("Parent"))
	write(t, env, "a/0 other.xml", noteXML("Other"))
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	want := "create a/0 other.xml|create a/2 parent.xml|create a/1 child.xml"
	if got := strings.Join(ad.Remote.Calls, "|"); got != want {
		t.Fatalf("order: %s", got)
	}
	if ad.Remote.Parents[idOf(t, env, "a/1 child.xml")] != idOf(t, env, "a/2 parent.xml") {
		t.Fatalf("%v", ad.Remote.Parents)
	}
}

func TestCommitRefusesCycleAndMissingParent(t *testing.T) {
	env, ad, out := cloned(t)
	deps := map[string][]string{"a/x.xml": {"a/y.xml"}, "a/y.xml": {"a/x.xml"}, "a/c.xml": {"a/nope.xml"}}
	ad.Deps = func(l *adapter.Resource) []string { return deps[l.Path] }
	for _, p := range []string{"a/x.xml", "a/y.xml", "a/c.xml", "a/z.xml"} {
		write(t, env, p, noteXML(p))
	}
	r := commit(t, env, CommitOpts{})
	if r.Failed != 3 || strings.Join(ad.Remote.Calls, "|") != "create a/z.xml" {
		t.Fatalf("%+v %v\n%s", r, ad.Remote.Calls, out)
	}
	for _, s := range []string{"parent cycle: a/x.xml -> a/y.xml -> a/x.xml",
		"parent a/nope.xml is not tracked and not part of this commit"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("missing %q:\n%s", s, out)
		}
	}
}
```

- [ ] **Step 4: run** `go test ./internal/engine -run 'ParentFirst|CycleAndMissing'` — Expected: FAIL (wrong order / no messages).

- [ ] **Step 5: `internal/engine/depend.go`**

```go
package engine

import (
	"slices"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

// dependencies maps each file with local content to the paths the adapter
// says it needs created first (parent-child spec §2.1).
func (e *Env) dependencies(cs []changes.FileChange) map[string][]string {
	d, ok := e.Adapter.(adapter.Dependent)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for _, fc := range cs {
		if fc.Local == nil || fc.Err != nil {
			continue
		}
		if ps := d.DependsOn(&adapter.Resource{ID: fc.ID, Path: fc.Path, Root: fc.Local.Content}); len(ps) > 0 {
			out[fc.Path] = ps
		}
	}
	return out
}

// orderCreates puts every new file after the new files it depends on,
// keeping the given order otherwise. New files must lead cs (Commit sorts
// them first). It also returns, per file in a cycle, the cycle.
func orderCreates(cs []changes.FileChange, deps map[string][]string) ([]changes.FileChange, map[string]string) {
	n := 0
	for n < len(cs) && cs[n].Status == 'A' {
		n++
	}
	at := map[string]int{}
	for i, fc := range cs[:n] {
		at[fc.Path] = i
	}
	const visiting, done = 1, 2
	state, cycles := map[string]int{}, map[string]string{}
	var stack []string
	out := make([]changes.FileChange, 0, len(cs))
	var visit func(p string)
	visit = func(p string) {
		switch state[p] {
		case done:
			return
		case visiting:
			loop := stack[slices.Index(stack, p):]
			msg := "parent cycle: " + strings.Join(append(slices.Clone(loop), p), " -> ")
			for _, q := range loop {
				if _, seen := cycles[q]; !seen {
					cycles[q] = msg
				}
			}
			return
		}
		state[p] = visiting
		stack = append(stack, p)
		for _, d := range deps[p] {
			if _, ok := at[d]; ok {
				visit(d)
			}
		}
		stack = stack[:len(stack)-1]
		state[p] = done
		out = append(out, cs[at[p]])
	}
	for _, fc := range cs[:n] {
		visit(fc.Path)
	}
	return append(out, cs[n:]...), cycles
}

// untracked is the first dependency that is neither tracked nor new in this commit.
func (e *Env) untracked(deps []string, adds map[string]bool) string {
	for _, p := range deps {
		if _, ok := e.IDByPath(p); !ok && !adds[p] {
			return p
		}
	}
	return ""
}
```

- [ ] **Step 6: `Commit`** — after the `sort.SliceStable(...)` and `d := …`:

```go
	deps := e.dependencies(cs)
	cs, cycles := orderCreates(cs, deps)
	adds := map[string]bool{}
	for _, fc := range cs {
		if fc.Status == 'A' {
			adds[fc.Path] = true
		}
	}
```

and at the top of the per-file loop:

```go
		if msg, ok := cycles[fc.Path]; ok {
			fc.Err = errors.New(msg)
		} else if p := e.untracked(deps[fc.Path], adds); p != "" && fc.Status == 'A' {
			fc.Err = fmt.Errorf("parent %s is not tracked and not part of this commit", p)
		}
```

- [ ] **Step 7: run** `go test ./internal/engine ./internal/adapter/...` — Expected: PASS.

- [ ] **Step 8: commit** — `git add -A internal && git commit -m "commit: create new parents before their new children"`

---

### Task 2: Skip children of a failed parent; follow write-back renames

**Files:**
- Modify: `internal/engine/engine.go` (`Env` fields, `IDByPath`)
- Modify: `internal/engine/commit.go` (`Commit` loop, `finish`)
- Modify: `internal/engine/depend.go`
- Test: `internal/engine/depend_test.go`

**Interfaces:**
- Consumes: Task 1 hooks and helpers.
- Produces: `Env.renamed map[string]string`, `Env.pending map[string]bool`; `IDByPath` follows `renamed`, then the index, then `pending` (→ `adapter.Pending`).

- [ ] **Step 1: failing tests**

```go
func TestCommitSkipsChildrenOfFailedParent(t *testing.T) {
	env, ad, out := cloned(t)
	nested(ad)
	ad.Remote.FailCreate = map[string]error{"a/p.xml": errors.New("500 boom")}
	write(t, env, "a/p.xml", noteXML("P"))
	write(t, env, "a/p/c.xml", noteXML("C"))
	write(t, env, "a/p/c/g.xml", noteXML("G"))
	r := commit(t, env, CommitOpts{})
	if r.Failed != 3 || strings.Join(ad.Remote.Calls, "|") != "create a/p.xml" {
		t.Fatalf("%+v %v\n%s", r, ad.Remote.Calls, out)
	}
	for _, s := range []string{"parent a/p.xml was not created", "parent a/p/c.xml was not created"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("missing %q:\n%s", s, out)
		}
	}
	if !strings.Contains(read(t, env, "a/p/c.xml"), "<msg>parent a/p.xml was not created</msg>") {
		t.Fatal(read(t, env, "a/p/c.xml"))
	}
	if n := len(status(t, env)); n != 3 {
		t.Fatalf("all three stay new: %d", n)
	}
	ad.Remote.FailCreate = nil
	out.Reset()
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("retry: %+v\n%s", r, out)
	}
	if ad.Remote.Parents[idOf(t, env, "a/p/c/g.xml")] != idOf(t, env, "a/p/c.xml") {
		t.Fatalf("%v", ad.Remote.Parents)
	}
}

func TestCommitChildrenFollowRenamedParent(t *testing.T) {
	env, ad, out := cloned(t)
	nested(ad)
	ad.Remote.CreatePath = map[string]string{
		"a/guide.xml":             "a/User guide.xml",
		"a/guide/step.xml":        "a/User guide/step.xml",
		"a/guide/step/detail.xml": "a/User guide/step/detail.xml",
	}
	write(t, env, "a/guide.xml", noteXML("User guide"))
	write(t, env, "a/guide/step.xml", noteXML("Step"))
	write(t, env, "a/guide/step/detail.xml", noteXML("Detail"))
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	for _, p := range []string{"a/User guide.xml", "a/User guide/step.xml", "a/User guide/step/detail.xml"} {
		if !env.Tree.Exists(p) {
			t.Fatalf("missing %s", p)
		}
	}
	if env.Tree.Exists("a/guide.xml") || env.Tree.Exists("a/guide") {
		t.Fatal("the old file and folder must go")
	}
	if ad.Remote.Parents[idOf(t, env, "a/User guide/step/detail.xml")] != idOf(t, env, "a/User guide/step.xml") {
		t.Fatalf("%v", ad.Remote.Parents)
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status must be clean")
	}
}
```

(add `"errors"` to the imports)

- [ ] **Step 2: run** — Expected: FAIL (children sent / "parent … not found").

- [ ] **Step 3: engine.go** — `Env` gets

```go
	// One commit's view of paths (parent-child spec §2.3): where write-back
	// moved files, and in a dry run the new files the commit would create.
	renamed map[string]string
	pending map[string]bool
```

and

```go
func (e *Env) IDByPath(path string) (string, bool) {
	for range len(e.renamed) {
		next, ok := e.renamed[path]
		if !ok {
			break
		}
		path = next
	}
	if en, ok := e.Index.ByPath(path); ok {
		return en.ID, true
	}
	if e.pending[path] {
		return adapter.Pending, true
	}
	return "", false
}
```

- [ ] **Step 4: finish** — before `report(path)` at the end: `if path != fc.Path && e.renamed != nil { e.renamed[fc.Path] = path }`.

- [ ] **Step 5: depend.go** — add

```go
// failedDep is the first dependency whose create did not happen in this commit.
func failedDep(deps []string, failed map[string]bool) string {
	for _, p := range deps {
		if failed[p] {
			return p
		}
	}
	return ""
}

// skipBlocked fails fc without sending anything: its parent was not created
// in this commit (parent-child spec §2.3).
func (e *Env) skipBlocked(fc changes.FileChange, parent string, dryRun bool, r *Report) error {
	err := fmt.Errorf("parent %s was not created", parent)
	if dryRun {
		r.Failed++
		e.line(fc.Actions[0].Verb, fc.Path, "", "FAIL", err.Error())
		return nil
	}
	return e.failAll(fc, fc.Actions, err, r)
}
```

- [ ] **Step 6: Commit loop** — after `adds` is built:

```go
	e.renamed = map[string]string{}
	defer func() { e.renamed, e.pending = nil, nil }()
	failed := map[string]bool{}
```

per file, after the cycle/untracked block:

```go
		if p := failedDep(deps[fc.Path], failed); p != "" && fc.Err == nil && len(fc.Actions) > 0 {
			failed[fc.Path] = true
			if err := e.skipBlocked(fc, p, o.DryRun, &r); err != nil {
				return r, err
			}
			continue
		}
```

real commit branch: after `commitFile`, `if _, ok := e.IDByPath(fc.Path); fc.Status == 'A' && !ok { failed[fc.Path] = true }`.

- [ ] **Step 7: run** `go test ./internal/engine` — Expected: PASS.

- [ ] **Step 8: commit** — `git commit -am "commit: skip children of a parent that was not created; follow write-back renames"`

---

### Task 3: Dry run resolves new parents

**Files:**
- Modify: `internal/engine/commit.go` (`dryRunFile` returns bool; `Commit`)
- Test: `internal/engine/depend_test.go`

- [ ] **Step 1: failing test**

```go
func TestDryRunResolvesNewParents(t *testing.T) {
	env, ad, out := cloned(t)
	nested(ad)
	write(t, env, "a/p.xml", noteXML("P"))
	write(t, env, "a/p/c.xml", noteXML("C"))
	r := commit(t, env, CommitOpts{DryRun: true})
	if r.Failed != 0 || len(ad.Remote.Calls) != 0 || !strings.Contains(out.String(), "under a new parent") {
		t.Fatalf("%+v\n%s", r, out)
	}
	ad.Remote.FailVerb = map[string]error{"create": errors.New("500 boom")}
	out.Reset()
	if r := commit(t, env, CommitOpts{DryRun: true}); r.Failed != 2 || !strings.Contains(out.String(), "parent a/p.xml was not created") {
		t.Fatalf("%+v\n%s", r, out)
	}
}
```

- [ ] **Step 2: run** — Expected: FAIL (`parent a/p.xml not found`).

- [ ] **Step 3: implement** — in `Commit`, `if o.DryRun { e.pending = adds }`. `dryRunFile` returns `bool` (would the file's actions all run): `false` when precheck stops it, on `policy.Deny`, on an ask with `d.Prompt == nil`, or on a Check error. The loop:

```go
		if o.DryRun {
			if !e.dryRunFile(ctx, fc, d, &r) && fc.Status == 'A' {
				failed[fc.Path] = true
			}
		} else { … }
```

- [ ] **Step 4: run** `go test ./internal/engine` — Expected: PASS.

- [ ] **Step 5: commit** — `git commit -am "commit --dry-run: files created earlier in the same commit resolve"`

---

### Task 4: Confluence

**Files:**
- Modify: `internal/adapter/confluence/adapter.go` (`DependsOn`)
- Modify: `internal/adapter/confluence/session.go` (Check create detail)
- Modify: `docs/confluence.md:225`
- Test: `internal/cli/confluence_site_e2e_test.go`

- [ ] **Step 1: failing e2e**

```go
// A new page tree commits in one go, even when the parent's title moves its
// file and folder on write-back.
func TestConfluenceNewPageTree(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "acme"))
	page := func(p, title string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(`<page><title>`+title+`</title><body type="application/xhtml+xml"><p>x</p></body></page>`), 0o644)
	}
	page("eng/Home/Guide.xml", "User Guide")
	page("eng/Home/Guide/Step one.xml", "Step one")
	page("eng/Home/Guide/Step one/Detail.xml", "Detail")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), `create page under "Guide" (created in this commit)`, "0 failed")
	mustRun(t, 0, "commit")
	ids := map[string]string{}
	for _, p := range []string{"eng/Home/User Guide.xml", "eng/Home/User Guide/Step one.xml", "eng/Home/User Guide/Step one/Detail.xml"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`<page id="(\d+)"`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s:\n%s", p, b)
		}
		ids[p] = string(m[1])
	}
	if exists("eng/Home/Guide") || exists("eng/Home/Guide.xml") {
		t.Fatal("the old file and folder must go")
	}
	if p, _ := srv.Page(ids["eng/Home/User Guide.xml"]); p.ParentID != "81001" {
		t.Fatalf("%+v", p)
	}
	if p, _ := srv.Page(ids["eng/Home/User Guide/Step one/Detail.xml"]); p.ParentID != ids["eng/Home/User Guide/Step one.xml"] {
		t.Fatalf("%+v", p)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
}
```

- [ ] **Step 2: run** `go test ./internal/cli -run NewPageTree` — Expected: FAIL (children commit first? no: dry run reports the parent missing).

- [ ] **Step 3: implement**

```go
// DependsOn names the page file of the folder a page sits in: a new parent
// page is created before its children.
func (*Adapter) DependsOn(local *adapter.Resource) []string {
	if p := parentPath(local.Path); p != "" {
		return []string{p}
	}
	return nil
}
```

In `Check`, case `a.Verb == "create"`: keep the parent id from `parentFor` and, when it is `adapter.Pending`, set `res.Detail = a.Detail + " (created in this commit)"`.

`docs/confluence.md`: replace "The parent is the page whose folder the file sits in, and that parent must already exist on Confluence (commit it first)." with "The parent is the page whose folder the file sits in. It may be new in the same commit: gfs creates parents before their children, at any depth, and skips the children of a parent that was not created (`parent … was not created`)."

- [ ] **Step 4: run** `go test ./internal/cli ./internal/adapter/confluence` — Expected: PASS.

- [ ] **Step 5: commit** — `git commit -am "confluence: a new page tree commits in one go"`

---

### Task 5: Jira `<parent file="…"/>`

**Files:**
- Modify: `internal/adapter/jira/jtest/edit.go` (`setField` case `parent`)
- Modify: `internal/adapter/jira/schema.go:40`
- Modify: `internal/adapter/jira/encode.go` (encoder fields, `parent` case, `parentFile`)
- Modify: `internal/adapter/jira/create.go:51`, `internal/adapter/jira/edit.go:125`
- Modify: `internal/adapter/jira/adapter.go` (`DependsOn`)
- Modify: `internal/adapter/jira/apply.go` (Check create detail)
- Test: `internal/adapter/jira/create_test.go`

**Interfaces:**
- Consumes: `adapter.Dependent`, `adapter.Pending`.
- Produces: `encoder.dir string`, `encoder.idByPath func(string) (string, bool)`.

- [ ] **Step 1: failing tests** (create_test.go)

```go
func TestPlanCreateParentFile(t *testing.T) {
	srv, s := editSite(t)
	srv.SetFields("GEN", "2",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})
	ids := map[string]string{"gen/Epic.xml": "10001", "gen/New epic.xml": adapter.Pending}
	req := func(parent string) adapter.ApplyRequest {
		r := newFile(t, "gen/Sub.xml", `<issue><type>Sub-task</type><summary>x</summary>`+parent+`</issue>`)
		r.IDByPath = func(p string) (string, bool) { id, ok := ids[p]; return id, ok }
		return r
	}
	plan := func(parent string) (any, error) {
		p, err := s.planCreate(bg, req(parent))
		if err != nil {
			return nil, err
		}
		return p.fields["parent"], nil
	}
	if v, err := plan(`<parent file="Epic.xml"/>`); err != nil || v.(map[string]any)["id"] != "10001" {
		t.Fatalf("%v %v", v, err)
	}
	if v, err := plan(`<parent>GEN-1</parent>`); err != nil || v.(map[string]any)["key"] != "GEN-1" {
		t.Fatalf("%v %v", v, err)
	}
	for parent, want := range map[string]string{
		`<parent file="Epic.xml">GEN-1</parent>`: `<parent> takes a key or file="…", not both`,
		`<parent file=""/>`:                      `<parent> takes a key or file="…", not both`,
		`<parent file="../ops/Epic.xml"/>`:       `<parent file> names a file in this project folder`,
		`<parent file="Nope.xml"/>`:              `parent file Nope.xml is not an issue in this tree`,
	} {
		if _, err := plan(parent); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", parent, err, want)
		}
	}
	if out := s.Check(bg, req(`<parent file="New epic.xml"/>`)); out[0].Err != nil ||
		!strings.Contains(out[0].Detail, "parent: New epic.xml (created in this commit)") {
		t.Fatalf("%+v", out)
	}
	ad := &Adapter{}
	if got := ad.DependsOn(req(`<parent file="Epic.xml"/>`).Local); len(got) != 1 || got[0] != "gen/Epic.xml" {
		t.Fatal(got)
	}
	for _, p := range []string{`<parent>GEN-1</parent>`, `<parent file="a/b.xml"/>`, ``} {
		if got := ad.DependsOn(req(p).Local); got != nil {
			t.Errorf("%s: %v", p, got)
		}
	}
}
```

- [ ] **Step 2: run** `go test ./internal/adapter/jira -run ParentFile` — Expected: FAIL (compile: no DependsOn).

- [ ] **Step 3: jtest** — `setField`, before the generic handling:

```go
	case "parent":
		if v == nil {
			delete(is.Fields, "parent")
			return ""
		}
		m, _ := v.(map[string]any)
		ref, _ := m["key"].(string)
		if ref == "" {
			ref, _ = m["id"].(string)
		}
		par := s.lookup(ref)
		if par == nil {
			return "Could not find issue by id or key."
		}
		is.Fields["parent"] = map[string]any{"id": par.ID, "key": par.Key}
		return ""
```

- [ ] **Step 4: schema / encoder / adapter**

schema.go: `{Name: "parent", Kind: schema.Field, Attrs: []schema.Attr{{Name: "file"}}},`

encode.go — `encoder` gains

```go
	dir      string                           // the issue file's folder, for <parent file>
	idByPath func(path string) (string, bool) // nil: no file resolves
```

case `parent`:

```go
	case "parent":
		if f, ok := attr(n, "file"); ok {
			return e.parentFile(f, textOf(n))
		}
		if textOf(n) == "" {
			return nil, nil
		}
		return map[string]any{"key": textOf(n)}, nil
```

```go
// parentFile is the parent <parent file="…"/> names: the issue of that file
// in this project folder, possibly created earlier in the same commit.
func (e encoder) parentFile(file, key string) (any, error) {
	if file == "" || key != "" {
		return nil, errors.New(`<parent> takes a key or file="…", not both`)
	}
	if strings.Contains(file, "/") {
		return nil, errors.New("<parent file> names a file in this project folder")
	}
	if e.idByPath != nil {
		if id, ok := e.idByPath(path.Join(e.dir, file)); ok {
			return map[string]any{"id": id}, nil
		}
	}
	return nil, fmt.Errorf("parent file %s is not an issue in this tree", file)
}
```

create.go:51: `enc := encoder{s: s, project: p.Key, dir: path.Dir(req.Local.Path), idByPath: req.IDByPath}`.
edit.go:125: `enc := encoder{s: s, issueKey: ic.key, project: ic.p.Key, idByPath: req.IDByPath}` and in the `req.Local != nil` branch `enc.dir = path.Dir(req.Local.Path)`.

adapter.go:

```go
// DependsOn names the issue file an issue's <parent file="…"/> points at, so
// commit creates that issue first.
func (*Adapter) DependsOn(local *adapter.Resource) []string {
	if local.Root == nil || local.Root.Name != "issue" {
		return nil
	}
	f, ok := attr(child(local.Root, "parent"), "file")
	if !ok || f == "" || strings.Contains(f, "/") {
		return nil
	}
	return []string{path.Join(path.Dir(local.Path), f)}
}
```

apply.go, `Check` case `create`:

```go
		out[i].Err = s.checkCreate(ctx, req)
		if out[i].Err == nil {
			out[i].Detail = pendingParent(req, out[i].Action.Detail)
		}
```

```go
// pendingParent is a dry run's detail for a parent the same commit creates
// first ("" otherwise).
func pendingParent(req adapter.ApplyRequest, detail string) string {
	f, ok := attr(child(req.Local.Root, "parent"), "file")
	if !ok || req.IDByPath == nil {
		return ""
	}
	if id, _ := req.IDByPath(path.Join(path.Dir(req.Local.Path), f)); id != adapter.Pending {
		return ""
	}
	return strings.TrimSpace(detail + "  parent: " + f + " (created in this commit)")
}
```

- [ ] **Step 5: run** `go test ./internal/adapter/jira/...` — Expected: PASS.

- [ ] **Step 6: commit** — `git commit -am "jira: <parent file=…/> names a parent issue by its file"`

---

### Task 6: Jira end to end and docs

**Files:**
- Test: `internal/cli/jira_e2e_test.go`
- Modify: `docs/jira.md` (hierarchy bullet, issue example)

- [ ] **Step 1: test**

```go
// A new issue and new children naming it by file commit together; the
// children's files sort before the parent's.
func TestJiraNewParentAndChildren(t *testing.T) {
	srv := jiraSite(t)
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink"})
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "jira://acme.atlassian.net/GEN?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "gen"))
	issue := func(name, extra string) {
		os.WriteFile("gen/"+name+".xml", []byte(`<issue><summary>`+name+`</summary><type>Task</type>`+extra+`</issue>`), 0o644)
	}
	key := func(summary string) string {
		m, _ := filepath.Glob("gen/GEN-* " + summary + ".xml")
		if len(m) != 1 {
			t.Fatalf("%s: %v", summary, m)
		}
		return strings.Fields(filepath.Base(m[0]))[0]
	}
	parentOf := func(k string) any {
		p, _ := srv.Issue(srv.ID(k)).Fields["parent"].(map[string]any)
		return p["key"]
	}

	issue("Z epic", "")
	issue("A login page", `<parent file="Z epic.xml"/>`)
	issue("B button", `<parent file="A login page.xml"/>`)
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "parent: Z epic.xml (created in this commit)", "0 failed")
	mustRun(t, 0, "commit")
	epic, page, button := key("Z epic"), key("A login page"), key("B button")
	if parentOf(page) != epic || parentOf(button) != page {
		t.Fatalf("%s<-%v %s<-%v", page, parentOf(page), button, parentOf(button))
	}
	if b, _ := os.ReadFile("gen/" + button + " B button.xml"); !strings.Contains(string(b), "<parent>"+page+"</parent>") {
		t.Fatalf("%s", b)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// a parent that fails leaves its child new; both go next time
	issue("C child", `<parent file="D bad.xml"/>`)
	os.WriteFile("gen/D bad.xml", []byte(`<issue><summary>D bad</summary><type>Nope</type></issue>`), 0o644)
	mustContain(t, mustRun(t, 1, "commit"), "parent gen/D bad.xml was not created")
	replaceIn(t, "gen/D bad.xml", "<type>Nope</type>", "<type>Task</type>")
	mustRun(t, 0, "commit")
	if parentOf(key("C child")) != key("D bad") {
		t.Fatal(parentOf(key("C child")))
	}

	// an existing issue re-parents onto an issue created in the same commit
	issue("E epic", "")
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "</summary>", `</summary><parent file="E epic.xml"/>`)
	mustRun(t, 0, "commit")
	if parentOf("GEN-1") != key("E epic") {
		t.Fatal(parentOf("GEN-1"))
	}
}
```

- [ ] **Step 2: run** `go test ./internal/cli -run JiraNewParent` — fix whatever fails at the seams (expected: PASS after Tasks 1–5).

- [ ] **Step 3: docs/jira.md** — hierarchy bullet: "Hierarchy is `<parent>`, never folders. Sub-tasks sit next to their parent. A parent that is new too is named by its file, `<parent file="Q4 platform epic.xml"/>`: commit creates it first and writes its key back." Add `<parent file="…"/>` as a comment line under `<parent>SUP-4000</parent>` in the issue example.

- [ ] **Step 4: full run** `go vet ./... && go test ./...` — Expected: PASS.

- [ ] **Step 5: commit** — `git commit -am "jira: new parent and children in one commit, end to end; docs"`
