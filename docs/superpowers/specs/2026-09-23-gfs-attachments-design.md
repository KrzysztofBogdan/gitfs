# gfs attachments design

Status: draft for review. Date: 2026-09-23.
Extends `2026-09-23-gfs-cli-design.md` (the "Attachments" open question) and
is illustrated in `example/` (`confluence/`, `mail/`, `slack/`, and the
"Attachments" section of `example/README.md`).

## 1. Purpose

Resources carry binary content: Confluence page attachments, mail
attachments, Slack files. Downloading all of it on `clone` and `pull` makes
working trees huge and slow, and most of it is never opened.

Design principle: **resource files always list attachments; bytes are fetched
on demand; once fetched, an attachment syncs both ways like any other file.**

### Goals

* `clone` and `pull` never download attachment bytes.
* `gfs get <path>` downloads the bytes of one attachment, or of every
  attachment of a resource or directory.
* A fetched attachment is tracked: `pull` refreshes it when the remote
  changes, `commit` uploads it when it changes locally.
* A new file dropped next to a resource is uploaded by `commit`.
* One shared mechanism in the core; adapters only map it to their API.

### Non-goals

* Merging binary content. Changed on both sides is a conflict with both
  copies kept.
* Automatic download rules (by type or size). Can be added later on top of
  `gfs get`.
* Renaming attachments from the working tree (v1 warns, see 3.5).
* Attachment version history. Only the current version is mirrored.

### Scope of the first implementation

The core mechanism plus the Confluence adapter. The contract covers mail and
Slack (read-only kinds, attachments nested in sub-resources, name prefixes)
so that those adapters can be added without changing the core; nesting below
the resource root is implemented together with the first adapter that needs
it (Slack).

## 2. Concepts

| term        | meaning                                                                          |
|-------------|----------------------------------------------------------------------------------|
| attachment  | binary content owned by a resource (or a sub-resource) and stored by the service  |
| element     | the attachment's metadata element in the resource file, e.g. `<attachment>`      |
| bytes       | the attachment's content, a regular file in the working tree once fetched        |
| sidecar     | the directory `X.files/` next to the resource file `X.xml`                       |
| fetched     | the bytes have been downloaded and are tracked in `.gfs/attachments`             |
| evicted     | the bytes of a fetched attachment were deleted locally; the element stays        |

## 3. File model

### 3.1 The element

Every attachment the remote has is listed in the resource file, fetched or
not. The adapter declares the element (name, identity, version) in its schema
with the new kind `schema.Attachment`. All attributes are service-owned and
read-only:

```xml
<page id="98130" version="2" ...>
  <title>Runbooks</title>
  <body .../>
  <attachment id="att98231" name="rollback-flow.png" type="image/png" size="74" version="2" created="2026-03-02T09:15:00+01:00" author="kbogdan"/>
  <attachment id="att98232" name="runbook-template.pdf" type="application/pdf" size="88312" version="1" created="2026-01-20T11:31:00+01:00" author="alice"/>
</page>
```

* `id` is the identity, `name` the service's file name, `version` the
  service's version token (absent where the service has none, e.g. mail).
* The element has no children. It is sorted by the adapter's sort key
  (usually `created`, then `id`), like sub-resources.
* The user never writes an element. An element without `id` is a validation
  error: *new attachments are created by adding a file to `X.files/`*.
* Removing an element is the way to delete an attachment on the remote.

### 3.2 Sidecar paths

The bytes of the attachments of `a/b/Name.xml` live in `a/b/Name.files/`.
The file name is derived from the element:

1. Sanitise `name` with the adapter's filename rules (path separators and
   control characters replaced, same as resource names).
2. If the adapter declares a prefix attribute (Slack: the parent message's
   `ts`), prepend `<value>-`.
3. De-duplicate within the sidecar: sort the resource's attachment elements
   by `id` (numeric where both are numeric, else string order); the first
   occurrence of a name keeps it, later ones get ` (2)`, ` (3)`, ... before
   the extension: `scan.pdf`, `scan (2).pdf`.

The derived path is used for `gfs get` arguments, for status output and for
downloads. Once fetched, the actual path is stored in `.gfs/attachments` and
is authoritative, so a later change in id order or name never repoints a
fetched file silently (a remote rename is an explicit move, 5.2).

Reserved suffix: a resource whose derived file name would end in `.files`
before `.xml` gets `_` appended (`Foo.files_.xml`), so its sidecar can never
be mistaken for a resource directory. Directory names ending in `.files` are
not allowed for resources in `tree` path models for the same reason.

### 3.3 Tracking: `.gfs/attachments`

One line per fetched attachment, tab-separated, sorted by path:

```
<resource id>  <attachment id>  <version>  <sha256>  <size>  <mtime>  <path>
98130          att98231         2          9a25…     74      1758612000000000000  eng/Home/Runbooks.files/rollback-flow.png
```

* `version`, `sha256`, `size`: the remote version and content as last synced
  (downloaded or uploaded). This is the attachment's **base**. No copy of the
  base bytes is kept.
* `mtime`: the file's mtime (Unix nanoseconds) when it was last synced. A
  file whose size and mtime both match is unchanged without hashing it; any
  mismatch triggers a hash.
* `version` is `-` for services without versions.
* `path` is last so it may contain spaces; a path containing a tab or newline is refused.

`.gfs/` therefore holds `config`, `base/`, `index`, `attachments`, `log`,
`lock`. Like `index`, `attachments` is regenerable except for which
attachments were fetched; deleting it makes every attachment unfetched and
leaves the bytes as untracked files (3.4).

### 3.4 States

For each attachment element of a resource, and each file in its sidecar:

| element | tracked line | file | condition | state | status |
|---|---|---|---|---|---|
| yes | no | no | | not fetched | silent |
| yes | yes | yes | hash = line, element version = line version | clean | silent |
| yes | yes | yes | hash ≠ line, element version = line version | changed locally | `M` update |
| yes | yes | yes | hash = line, element version ≠ line version | changed on remote | silent; `pull` refreshes |
| yes | yes | yes | hash ≠ line, element version ≠ line version | both changed | `C` |
| yes | yes | no | | evicted | silent; the line is dropped by the next command that takes the lock |
| no | any | any | element was in base | removed locally | `D` delete [ask] |
| no | no | yes | | new file | `A` create |
| no | yes | yes | element not in base either (removed on remote) | deleted on remote, changed locally | `C` deleted on remote |
| yes | no | yes | file at the derived path, not tracked | untracked collision | `!` refuse: "file exists, not fetched by gfs; move it away or delete it" |

`element in base` means the element is present in `.gfs/base/` for that
resource. "Removed locally" also applies when the attachment was never
fetched: removing its element from the XML deletes it on the remote.

A file named `<name>.remote-v<N>.<ext>` next to a `C` attachment is the
conflict copy (5.3); it is not an attachment and not shown by status.

Every other file under `X.files/` whose resource `X.xml` does not exist, and
is not tracked for a resource that moved (3.5), is reported
`!  <dir>: attachment folder without its resource file`.

### 3.5 Moves and renames

* Moving or renaming the resource file (`R`): commit moves `X.files/` along
  with the resource on write-back and rewrites the tracked paths. The user may
  move the sidecar too, or not; both work, because tracked lines are found by
  resource id.
* Remote rename of an attachment (same `id`, new `name`): `pull` renames the
  local file to the new derived path and updates the line.
* Local rename of a file inside `X.files/`: the old tracked path is missing
  and a new untracked file with the same hash appears. v1 treats this as a
  warning, not as evict + create: `rename of attachments is not supported;
  rename it on the remote or delete it and add it again`. Commit skips both
  paths until the user undoes or completes it.

## 4. Commands

### 4.1 `gfs get <path>...`

```
gfs get <path>...
```

* A resource file: every attachment of that resource.
* A directory: every attachment of every resource under it, recursively.
* A sidecar path (`X.files/<name>`, derived or tracked): that attachment.
* No path is a usage error (exit 2); `gfs get .` fetches everything.

Per attachment: skip if clean (`=  <path>  up to date`); refuse if the local
file exists and is changed, conflicted, or untracked (`!`). Otherwise
download through the adapter, write atomically, record the line with the
version the adapter reports for the downloaded bytes. If that version is
newer than the element's, the resource file is stale; the line records the
real version and the report adds `(remote has v<N>; run gfs pull)`. `get`
never rewrites resource files.

Output, one line per attachment, then a footer:

```
  +  eng/Home/Runbooks.files/rollback-flow.png   74 B   v2
  =  eng/Home/Runbooks.files/runbook-template.pdf   up to date
  !  eng/Home.files/logo.svg   changed locally; commit or resolve first
2 fetched, 1 up to date, 1 refused (88.4 KB)
```

Exit code: 0 if nothing was refused or failed, 1 otherwise. Logged as verb
`get`. `get` takes the lock; it is not gated by policy (it changes nothing
remote).

### 4.2 `status`

Attachments are listed with their own sidecar path, sorted with the resource
lines:

```
  M  eng/Home/Runbooks.files/rollback-flow.png   upload new version of attachment (v2)
  A  eng/Home/Runbooks.files/oncall-rota.csv     attach to "Runbooks"
  D  eng/Home/Runbooks.files/runbook-template.pdf   delete attachment (not fetched)   [ask]
  C  eng/Home.files/logo.svg                     changed locally and on remote v4 by bob; remote copy: logo.remote-v4.svg
  !  mail/archive/2026/…/invoice-4411.pdf        attachments of received mail are read-only
```

`!` also shows the last commit's failure for an attachment action, read from
the resource envelope's `<errors>` (4.4).

### 4.3 `diff`

Attachments print git's binary notice with sizes and short hashes, because
no base bytes are kept:

```
Binary files a/eng/Home/Runbooks.files/rollback-flow.png (74 B, 9a2524d1) and b/eng/Home/Runbooks.files/rollback-flow.png (74 B, 3c1f0e77) differ
```

A removed element shows in the resource file's XML diff as usual.

### 4.4 `commit`

Attachment actions are actions of their resource and go through the
existing per-file algorithm (CLI spec section 6), with these rules:

* Targets: `attachment[id=<id>]` for existing attachments,
  `attachment[file=<file name>]` for new files.
* Order inside one resource: resource actions (create, update, move) first,
  then attachment creates and updates, then attachment deletes. A new page
  with a sidecar is created before its files are uploaded; a failed page
  create skips its uploads. A resource delete has no attachment actions (5.4).
* Policy classes default to the verb (`create`, `update`, `delete`); the
  adapter may re-class. Refusal of any action skips the whole resource, as
  today.
* Remote check (step 3): the fetched remote resource lists the attachment
  elements with their current versions. For each attachment update, remote
  element version ≠ line version means the remote moved: the attachment
  becomes `C` (5.3), its update is skipped, other actions continue. Services
  without an attachment version lock have a race window between this check
  and the upload; it is accepted and documented per adapter.
* Explicit verbs (mail `send`) receive the local sidecar files along with the
  content; the adapter decides how to attach them. Implicit attachment
  actions are not resolved for that file (CLI plan design decision 1).
* Write-back: the re-fetched resource includes the new or changed elements.
  Each uploaded file gets its line updated from the upload result (new id,
  version, the local hash, size, mtime); the bytes are not re-downloaded.
  Deleted attachments lose their line and, if fetched, their local file.
* Failure: the failed attachment keeps its local file and line unchanged, and
  an `<error action= target="attachment[...]" ...>` is added to the resource
  envelope, like any other partial failure.
* Report and log lines use the sidecar path:
  `create  eng/Home/Runbooks.files/oncall-rota.csv   ok   id=att98240`.
* `--dry-run` prints attachment actions with sizes and runs adapter checks
  (read-only kind, allowed operations, size limits).

A path argument selects attachments under it; `gfs commit X.files/a.png`
commits only that attachment (and nothing else of `X.xml`).

### 4.5 `pull`

After the existing per-resource algorithm has updated the resource file and
base (the element list is part of the canonical content, so element changes
are ordinary merges), for every tracked attachment of the resource:

| remote element | local file | action | report |
|---|---|---|---|
| same version | any | nothing | |
| new version | unchanged | download, replace file, update line | `~  <path>` |
| new version | changed | write remote bytes to `<name>.remote-v<N>.<ext>`, keep line at the old version | `C  <path>` |
| removed | unchanged or evicted | delete local file, drop line | `-  <path>` |
| removed | changed | keep file, keep line | `C  <path>  deleted on remote` |
| renamed | any | move file to the new derived path, then apply the rows above | `~  <old> -> <new>` |

`pull --force` replaces `C` attachments with the remote bytes (or removes
them when deleted on remote) and drops conflict copies. Untracked
attachments are never downloaded by `pull`.

### 4.6 `resolve`

`gfs resolve --ours|--theirs <attachment path>...`:

* `--theirs`: replace the file with the conflict copy, set the line to the
  remote version and hash. Deleted on remote: delete file and line.
* `--ours`: keep the file, set the line's version to the remote version and
  its hash to the remote hash (so the next commit uploads the local file as
  a new version). Deleted on remote: drop the line; the file becomes a new
  file (`A`) and the next commit re-creates it.
* Both delete the conflict copy. The conflict copy's hash is recomputed
  from the file, so `--theirs` needs no download.

### 4.7 `log`, `actions`

`log` lines for attachments use the sidecar path. `actions` lists
attachment verbs per attachment kind with their policy level and the
operations the kind allows (`create update delete`, or `read-only`).

## 5. Behaviour details

### 5.1 Scan

`workdir.Scan` no longer returns files under a directory whose name ends in
`.files` as resources. The change set computation walks sidecars separately:
for each resource (in the working tree or in the index), its sidecar path,
plus any tracked line of that resource id elsewhere (moved sidecar).

### 5.2 Remote rename

Detected on pull by `id`: same id, derived path changed. The move is applied
before the version comparison.

### 5.3 Conflict copies

`<stem>.remote-v<N><ext>` where `<stem><ext>` is the local file name and
`N` the remote version (`-` services: `.remote`). If that name is taken it is
de-duplicated like 3.2. Conflict copies are never tracked and never uploaded.

### 5.4 Resource deletion

A resource `D` (file deleted, `ask`) deletes its attachments on the remote
implicitly (services delete them with the resource). On success its sidecar
and its lines are removed. A resource deleted on the remote whose tracked
attachments are unchanged loses its sidecar with it; changed attachments are
kept and reported `C  deleted on remote`.

### 5.5 Size

Downloads and uploads stream to and from disk; they are never held in
memory whole. `gfs get` reports total bytes. An adapter may declare a
maximum upload size, checked on `--dry-run` and before upload.

## 6. Adapter contract additions

Declarative, in the schema:

* `schema.Attachment` element kind with: element name, identity attribute,
  name attribute, version attribute (optional), sort key, allowed operations
  (`create`, `update`, `delete`; none = read-only), optional prefix attribute
  taken from the parent element, optional maximum size.
* An attachment element may be declared at the resource root or inside a
  `Sub` (Slack files in messages). The first implementation supports the
  root; nested kinds come with the Slack adapter.

Operations, on the session:

* `Download(ctx, resourceID, attachmentID, w io.Writer) (AttachmentInfo, error)`
  streams the current bytes and returns their `version` and `size`.
* Apply handles actions with target `attachment[...]`: `create` (with the
  file path and a reader), `update` (attachment id, reader, the line's
  version as lock where the API supports one), `delete` (attachment id).
  Results carry the new attachment id and version for write-back.
* `Check` validates attachment actions without side effects.
* Explicit verbs receive the resource's sidecar files in the apply request.

Everything else (sidecar paths, tracking, hashing, status, conflict copies,
get/pull/commit/resolve integration) is in the core.

## 7. Confluence mapping

Element: `<attachment id name type size version created author>` at the page
root, after `<comment>`, sorted by `created` then `id`. Operations: create,
update, delete. `name` is the attachment title; Confluence requires titles
unique per page, so de-duplication never applies.

| purpose | request |
|---|---|
| list a page's attachments (for the page XML) | `GET /wiki/api/v2/pages/<id>/attachments?limit=250` → `id,title,mediaType,fileSize,version.{number,createdAt,authorId}`; follow `_links.next` |
| download | `GET /wiki/api/v2/attachments/<id>` → `downloadLink`, then `GET /wiki<downloadLink>` (follows redirects, streamed) |
| create | `POST /wiki/rest/api/content/<pageId>/child/attachment` multipart `file`, `minorEdit=true`, header `X-Atlassian-Token: no-check` |
| update bytes | `POST /wiki/rest/api/content/<pageId>/child/attachment/<attId>/data` multipart, same header |
| delete | `DELETE /wiki/api/v2/attachments/<id>` |

* Version lock: the upload endpoints take no expected version. Commit's
  step-3 check (4.4) is the only guard; the race window is documented.
* Attachment changes do not bump the page version. `List` already fetches
  every page fully, so element changes are seen; `Fetch` adds the
  attachments request.
* `version` is `version.number`; `created` is `version.createdAt` of the
  current version (API v2 does not expose the first upload time), like
  comments; `author` is the display name of `version.authorId`.
* A new page with a sidecar: page create, then uploads (4.4 order).
* `cftest` (the fake server) gains the attachment endpoints, multipart
  parsing, a download redirect, and fault injection per endpoint.

## 8. Testing

* **Paths**: derivation, sanitising, `.files` suffix escaping, duplicate
  names in id order (mail-shaped fixture), prefix attribute.
* **Tracking file**: parse and print, size+mtime fast path (a touched file
  with equal bytes is clean after hashing and its mtime is refreshed).
* **State table 3.4**: one table test per row, through `changes.Compute`,
  using the fake adapter extended with attachments.
* **Commit**: upload new, update changed, delete by removed element (fetched
  and never fetched), page create then upload, remote-moved attachment
  becomes `C` while other actions of the resource still run, partial failure
  writes `<errors>` with the attachment target, read-only kind refused on
  dry run and on commit, move of a resource moves its sidecar.
* **Pull**: every row of 4.5, including rename and `--force`.
* **Resolve**: `--ours` and `--theirs`, including deleted on remote.
* **get**: resource, directory and sidecar arguments, refuse rules, stale
  element report.
* **Streaming**: download and upload a 64 MB generated attachment through the
  fake adapter and assert heap growth (`runtime.ReadMemStats`) stays under
  8 MB.
* **Confluence**: every endpoint in 7 against `cftest`, including the
  multipart shape and the download redirect; an end-to-end CLI test: clone,
  `get`, edit, commit, remote edit, pull conflict, resolve.

## 9. Open questions

* Automatic download rules (`[attachments]` in `.gfs/config`, e.g. images
  under 1 MB on pull).
* Local rename of attachments (3.5), for services that support it
  (Confluence can rename an attachment title).
* Inline references: Confluence bodies reference attachments by name
  (`<ri:attachment ri:filename=…/>`); a renamed attachment breaks them.
  Out of scope; the service's behaviour applies.
* Mail drafts: IMAP has no in-place update, so a draft with changed
  attachments is re-stored as a new message; how its identity carries over
  is part of the mail adapter design.
