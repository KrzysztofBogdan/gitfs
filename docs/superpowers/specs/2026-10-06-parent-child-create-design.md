# Parent and children in one commit

Status: design approved in conversation 2026-10-06. Current behaviour
confirmed by a throwaway e2e probe against `cftest` the same day.

## 1. Purpose

Let one `gfs commit` create a new resource and new resources under it:
a Confluence page with child pages (any depth), a Jira issue with child
issues or sub-tasks.

### Behaviour before this change

- Confluence: a real commit of `Guide.xml` + `Guide/Step.xml` works only
  by accident (new files run in path order and `Guide.xml` sorts before
  `Guide/…`).
- Confluence bug 1: `commit --dry-run` reports each child as `parent page
  … is not on the remote yet; commit it first`.
- Confluence bug 2: when the parent's `<title>` differs from its file name,
  write-back renames `Guide.xml` to `User Guide.xml`; the children still
  look the parent up by `Guide.xml`, fail, and stay `!`.
- Jira: `<parent>` takes only an existing key, so a new parent and its
  children need two commits. New Jira files are flat and run in path
  order, so a child can run before its parent.

### Non-goals

- Cross-project Jira parents by file (`file` with a `/`).
- Moving whole folders on write-back; children move by their own
  write-back.
- Retrying a failed parent within the same commit.

## 2. Engine

### 2.1 Dependency hook

`internal/adapter/adapter.go`:

```go
// Dependent is implemented by adapters whose new resources need another
// resource created first. Paths are working-tree paths.
type Dependent interface{ DependsOn(local *Resource) []string }

// Pending is the id IDByPath returns during a dry run for a file this
// commit would create. Adapters never send it to the service.
const Pending = "(pending)"
```

The engine calls `DependsOn` for every file with local content (`A` and
`M`). A returned path counts as a dependency only when that path is an
`A` file of this commit; tracked paths already resolve.

### 2.2 Ordering

`Commit` keeps its category sort (A, M, R, D). Inside the A group it then
orders topologically by dependency, keeping path order wherever no
dependency forces otherwise, so commits without dependencies run exactly
as today.

- Cycle: every file in the cycle fails `invalid` with
  `parent cycle: a.xml -> b.xml -> a.xml`; nothing is sent.
- A dependency of an `A` file that is neither tracked nor an `A` file
  of this commit (typo, or excluded by a path filter): the file fails
  `invalid` with `parent X.xml is not tracked and not part of this
  commit`. `M` files are not checked this way; the adapter reports what
  it cannot resolve.

### 2.3 Per-commit state

Held on `Env`, reset at the start of every `Commit`:

- `renamed map[string]string`: old path → new path, recorded when the
  write-back of a create moves the file. `IDByPath` follows it (chains
  too), so `Guide.xml` resolves to the id now indexed under
  `User Guide.xml`.
- `pending map[string]bool`: A files of this commit not yet applied.
  During a dry run `IDByPath` returns `(adapter.Pending, true)` for them.
  In a real commit they resolve once applied (the index has them).
- `failed map[string]bool`: A files whose create failed, was denied, was
  invalid or conflicted. Before applying a file the engine checks its
  dependencies; if one is in `failed`, the file is reported
  `FAIL parent X.xml was not created`, gets `<errors>` like any failure,
  and is itself added to `failed` (so grandchildren skip too). It stays
  `A`, and the next commit retries parent and children together.

A parent whose create succeeded but whose write-back fetch failed counts
as created (its id is indexed); children proceed.

### 2.4 Empty folders

When a write-back moves a file out of a folder, `Tree.Remove` already
prunes the folder and its ancestors once empty; no new code. A folder
with any file left is kept.

## 3. Adapters

### 3.1 Confluence

- `DependsOn` returns `parentPath(local.Path)` when it is non-empty.
- `parentFor` is unchanged; the engine's `renamed` and `pending` lookups
  fix both bugs. A parent page file that is truly missing is now reported
  by the engine (§2.2) before the adapter's `… commit it first` is
  reached; that message stays for direct adapter callers.
- Dry-run detail for a pending parent: `create page under "Guide"
  (created in this commit)`.

### 3.2 Jira

`<parent>` takes a key as text **or** a `file` attribute naming the parent
issue's file:

```xml
<parent file="Q4 platform epic.xml"/>   <!-- new or tracked issue, by file -->
<parent>GEN-759</parent>                <!-- existing issue, by key -->
```

- Schema (`schema.go`): `<parent>` gains the attribute `file` (the
  RelaxNG already allows any attribute). The adapter has no status-time
  validation hook, so the encoder enforces the form, and the errors
  show in `commit --dry-run` and `commit`: text and `file` together, or
  `file=""`, is `<parent> takes a key or file="…", not both`; a `file`
  containing `/` is `<parent file> names a file in this project folder`.
- `DependsOn` returns `path.Join(path.Dir(local.Path), file)`.
- Encode (`encode.go`, case `parent`): with `file`, `IDByPath` gives the
  issue id, sent as `{"id": …}`. Not found: `parent file X.xml is not an
  issue in this tree`. `Pending` (dry run): no request; Check detail
  `parent: X.xml (created in this commit)`.
- Write-back is unchanged: Jira returns the parent key and the file ends
  up with `<parent>GEN-761</parent>`.
- An `M` file may switch to `file=` to re-parent onto an issue created
  in the same commit (A runs before M).
- `docs/jira.md`: `file=` in the issue example and the hierarchy bullet.

## 4. Failures

| Situation | Result |
|---|---|
| Parent create fails or is denied | children `FAIL parent X.xml was not created`, nothing sent, stay `A` |
| Parent conflicts / invalid | same as failed |
| Cycle | all files in the cycle invalid, nothing sent |
| Parent not tracked and not in this commit | child fails `parent X.xml is not tracked and not part of this commit` |
| Parent created, write-back fetch failed | counts as created; children proceed |

## 5. Testing

- Engine unit (fake adapter with a `DependsOn` stub): topological order
  and stable path order, cycle, skip after failed and denied parent
  (transitively), `renamed` lookup, `Pending` in dry run, empty folder
  removed (by the existing prune) and non-empty kept.
- Confluence e2e (`confluence_site_e2e_test.go`): three-level new tree,
  parent title differs from its file name. Dry run 0 failed; commit
  creates all three under the right parents; files end under
  `User Guide/…`; old `Guide/` gone; `status` clean.
- Jira e2e (`jira_e2e_test.go`): new epic + story + sub-task whose file
  names sort before the epic, all `file=`. Server sees parent created
  first; write-back has `<parent>KEY</parent>`; denied parent leaves
  children `A`; an `M` file re-parents onto a new epic.
- Jira unit (`planCreate`): key, `file` resolving to an id, both,
  `file=""`, `file` with `/`, unknown file.
- `jtest`: `parent` given by id or key is stored as `{id, key}` like real
  Jira returns it.
