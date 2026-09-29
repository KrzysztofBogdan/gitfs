# Confluence site clone design

Status: draft for review. Date: 2026-09-29.
Changes `2026-09-23-gfs-cli-design.md` (one working tree = one space) and the
Confluence adapter. No backward compatibility: existing working trees are
re-cloned.

## 1. Purpose

Today a working tree mirrors exactly one Confluence space, and the remote URL
must name it. Users want the whole site (or a chosen set of spaces) in one
tree, to read and search it and to edit across spaces, with one `pull` and one
`commit`.

A whole-site tree makes the current pull unusable: `List` fetches every page
body, label list, comment list and attachment list on every pull (four or more
requests per page, about 40,000 for 10,000 pages). Pull must fetch only what
changed.

### Goals

* `gfs clone confluence://<site>` clones every space the account sees,
  narrowed by `filter`, `type` and `exclude`.
* `confluence://<site>/<KEY>` and a pasted browser URL are shorthand for a
  one-space filter.
* One `pull` and one `commit` cover every space in the tree.
* A pull with nothing new fetches no page bodies.
* New comments and attachments are picked up without the page version
  changing.

### Non-goals

* Creating spaces (a new top-level folder is rejected).
* Moving a page between spaces (rejected).
* Blog posts, whiteboards, databases (unchanged: not supported).
* Detecting label-only changes, deleted comments or deleted attachments
  incrementally; `gfs pull --full` catches them (§5.4).
* Migrating existing single-space working trees.

## 2. Remote URL

### 2.1 Accepted forms

| input | normalised |
|-------|------------|
| `confluence://[<email>@]<site>[?filter=…&type=…&exclude=…]` | unchanged |
| `confluence://[<email>@]<site>/<KEY>` | `confluence://[<email>@]<site>?filter=<KEY>` |
| `confluence:https://<site>/wiki/spaces/<KEY>[/…]` | `confluence://<site>?filter=<KEY>` |
| `confluence:https://<site>/wiki[/…]` without `/spaces/<KEY>` | `confluence://<site>` |

* The browser form is any URL copied from the address bar: `/overview`,
  `/pages/123/Title` and so on all mean the whole space, never a subtree.
* `url.Parse` gives scheme `confluence` with the `https://…` part in
  `Opaque`; `adapter.ForURL` needs no change, `parseTarget` handles both.
* Any other path (`/a/b`, `/wiki/HF`) is an error that lists the accepted
  forms.
* `?base=<url>` keeps working (tests only).
* Combining a path key with `filter` (`confluence://site/HF?filter=ENG`) is an
  error.

### 2.2 Space selection

| parameter | meaning | default |
|-----------|---------|---------|
| `filter=K1,K2` | exactly these spaces, any type, archived included | all spaces |
| `type=global\|personal\|all` | which spaces, when `filter` is absent | `global` |
| `exclude=K1,K2` | removed from the result, applied last | none |

* Every key in `filter` must exist and be visible; otherwise the command fails
  and names the missing key.
* Unknown keys in `exclude` are ignored (the space may have been deleted).
* Without `filter`, archived spaces are skipped.
* `type` together with `filter` is an error (the filter already decides).
* Keys are matched as Confluence stores them (`HF`, `~jan`,
  `~5b12…` for account-id personal spaces).

### 2.3 Where it lives

`clone` writes the normalised URL to `[remote] url`, as today
(`engine/clone.go`). Every later command reads the selection from it. To
change the selection, edit `.gfs/config`; the next `pull` applies it (§5.5).

### 2.4 Default directory

`DefaultDir` returns the lower-cased key when `filter` names exactly one
space (`hf`), otherwise the site's first host label (`warsaw-dynamics`).

## 3. Layout

Unchanged below the space folder:

```text
warsaw-dynamics/
├── .gfs/
├── hf/                     space HF, key lower-cased
│   ├── Handoff Home.xml
│   └── Handoff Home/…
└── ~jan/                   personal space ~jan
    └── Jan's Home.xml
```

Global keys are upper-case letters and digits and personal keys start with
`~`, so lower-casing cannot make two spaces share a folder. Page file names,
duplicate-title suffixes and `.files/` sidecars follow the existing per-space
rules (`paths.go`).

## 4. Session model

One `session` (`internal/adapter/confluence/session.go`) serves every
selected space. There is no per-space sub-session.

```go
type space struct {
	key, id, dir string // "HF", "98307", "hf"
	loaded       bool   // its pages are in tree
}

type session struct {
	c      *client
	sel    selection          // parsed filter/type/exclude
	spaces map[string]*space  // by dir
	byID   map[string]*space  // by space id
	tree   map[string]pageRef // pageRef gains Space (id) and Version
	names  map[string]string
}
```

`spaceKey`, `spaceID` and `spaceDir` are removed.

### 4.1 Opening

`openSession` resolves the selection with one paginated request:

* with `filter`: `GET /wiki/api/v2/spaces?keys=K1,K2&limit=250`, with no
  status filter so archived spaces are found; fail on any missing key;
* without: `GET /wiki/api/v2/spaces?type=<type>&status=current&limit=250`
  (`type=all` omits the parameter), then drop `exclude` keys.

An empty selection is not an error: clone creates an empty tree and says so.

### 4.2 Page tree, loaded per space

`loadSpace(sp)` pages through `GET /wiki/api/v2/spaces/{id}/pages?limit=250`
and records id, title, parent, space and version number for each page. Spaces
are loaded on demand:

* `List` loads every selected space.
* `Fetch` first GETs the page (the response carries `spaceId`), then loads
  that page's space if needed to compute its path.
* `Apply`/`Check` load only the spaces of the files in the request.

So `commit` of one file in `hf/` loads the `hf` tree only, not the whole
site.

### 4.3 Paths

`paths(sp)` passes that space's pages to the existing `pagePaths(sp.dir,
refs)`. A page's path depends only on pages in its own space, so paths are
computed per space and merged where the full map is needed.

### 4.4 Fetch outside the selection

If `Fetch` finds that a page's `spaceId` is not selected (moved to another
space on Confluence), it drops the page from the tree and returns
`adapter.ErrNotFound`, as for a deleted page. `List` never reports such a
page (it is in no selected space's page list), so `pull` removes its file as
deleted on the remote.

### 4.5 Space-dependent helpers

`spaceFor(path)` maps a working path's first segment to a `*space`, or fails
with `no space "<dir>" in this tree (spaces: hf, eng, …)`. The helpers take
that space:

* `titleTaken(sp, title, except)`: titles are unique per space.
* `parentFor(p, …)` returns `(sp, parentID)`. A file directly in the space
  folder still goes under that space's homepage.
* `create` sends `spaceId: sp.id`.

## 5. Incremental pull

The engine already supports what this needs. `pull` passes
`Index.Cursor` to `List` and stores `Listing.Cursor` back
(`engine/pull.go:49`, `:223`). A listing entry with `Root == nil` whose
version and path match the index is skipped (`pull.go:81`); any other entry
is fetched through `Env.Resolved`. The work is in the adapter, plus one pull
flag.

### 5.1 List with an empty cursor (clone, `pull --full`)

Load every selected space and return a full `Resource` (body fetched) for
every page, `Full: true`, as today. Set `Cursor` (§5.3).

### 5.2 List with a cursor

1. Load every selected space. That gives a stub (`ID`, `Version`, `Path`,
   `Root == nil`) for every page.
2. Search for comments and attachments changed since the cursor (§5.3). Each
   result's container is a page; collect the page ids that are in the tree.
3. For those ids, `Fetch` the page and return the full `Resource` in place of
   the stub, so the engine compares content even though the version did not
   change. Pull already treats "same canonical content" as no change
   (`pull.go:97`), so an extra fetch costs requests, not correctness.
4. Return all entries with `Full: true`. Pages missing from the listing are
   deleted on the remote, as today.

A pull with nothing new: one spaces request plus one paginated page-list
request per space, and two searches. No page bodies.

### 5.3 Cursor and search

* `Cursor` is the UTC time `List` started, RFC 3339.
* CQL dates are read in the account's time zone, so the search uses a
  relative date instead of the stored time:
  `lastmodified >= now("-<N>m")`, where `N` is the minutes from the cursor to
  now, rounded up, plus 10 minutes of overlap for clock skew and search
  indexing delay.
* Two searches, both through
  `GET /wiki/rest/api/content/search?cql=…&expand=container&limit=250`:
  * `type = comment AND lastmodified >= now("-Nm")`
  * `type = attachment AND lastmodified >= now("-Nm")`
* With `filter`, add `AND space in ("K1","K2")`. Without it, search the whole
  site and ignore containers that are not in the tree.
* Inline comments also match `type = comment`. That costs an unneeded fetch
  of their page, nothing else.
* The implementation checks that `client.paginate` follows the v1 search's
  `_links.next`; if not, it gains v1 pagination.

### 5.4 Not detected incrementally

* Label-only changes (labels have no modification time in CQL).
* Deleted comments and deleted attachments (search does not return deleted
  content).

These appear the next time the page's version changes, or with
`gfs pull --full`, which passes an empty cursor and refetches everything.
`--full` is a new flag in `internal/cli/pull.go`, carried to the engine as
`PullOpts.Full`; the engine then calls `List(ctx, "")`.

### 5.5 Spaces entering or leaving the selection

* A space that is now selected (created on Confluence, or the selection was
  widened) has no pages in the index; its pages come back as stubs the index
  does not know, and pull adds them (`pull.go:62`). No special case.
* A space no longer selected (deleted, archived, excluded, or no longer
  visible) has no pages in the listing; pull deletes its files like any
  remote deletion, and locally modified files become conflicts under the
  existing rules.

## 6. Commit across spaces

`Check` and `Apply` resolve each file's space with `spaceFor`:

| local change | result |
|--------------|--------|
| edit, create, delete, attachments inside a space folder | as today, in that space |
| new file under a folder that is not a selected space (`new/Page.xml`) | refused: `no space "new" in this tree; gfs does not create spaces` |
| move a page from `hf/…` to `eng/…` | refused: `moving pages between spaces is not supported` |
| move to the space root | refused, as today |

Refusals appear in `commit --dry-run` and `commit`, as Check results do
today (`status` does not call the remote).

## 7. Errors

* Missing `filter` key, bad URL form, `type` with `filter`: fail before any
  page request, naming the problem.
* A failed space lookup, page list or search fails the whole `pull`, and the
  cursor is not advanced. There is no silent partial listing, because a
  missing space would otherwise look like deleted pages.
* Search rejected by Confluence (for example a CQL syntax change): the error
  suggests `gfs pull --full`.

## 8. Testing

### 8.1 Fake server (`internal/adapter/confluence/cftest/server.go`)

* Several spaces: global, personal, archived.
* `GET /wiki/api/v2/spaces` honours `keys`, `type`, `status`, pagination.
* `GET /wiki/rest/api/content/search`: parses the two CQL shapes above
  (type, relative `now()`, optional `space in`), returns content with
  `container`, paginates. Comments and attachments carry a modification time
  the test can set.
* A per-endpoint request counter.

### 8.2 Unit tests

* `parseTarget`: every row of §2.1, error forms, combined parameters.
* Selection: `filter`, `type`, `exclude`, archived handling, missing keys.
* Cursor to `now("-Nm")`: rounding, overlap.
* `paths` per space: the same title in two spaces, each keeping its own
  duplicate-title rules.

### 8.3 End-to-end (`internal/cli/confluence_e2e_test.go`)

* Clone the whole site; clone with `filter`, `type=personal`, `exclude`; the
  path shorthand and a browser URL give the same tree as `?filter=`.
* A pull with no changes fetches no page bodies (request counter).
* Page edited on the remote → updated. New footer comment only → picked up.
  New attachment only → listed. Label-only change → not picked up; with
  `--full` → picked up.
* New space created remotely → added on pull. Space excluded in config →
  its files removed.
* Commit: create in space A and edit in space B in one commit; a new
  top-level folder and a move between spaces are refused.
* Commit touching one space loads only that space's page list.

## 9. Docs

* `docs/confluence.md`: remote URL forms and selection, whole-site layout,
  incremental pull and its limits, `--full`, the new "not supported" rows.
* `start.md`: `pull --full` in the pull flags table; the clone example.
* Built-in help picks both up (`embed.go`); `internal/cli/docs_test.go` keeps
  passing.
