# gfs CLI design

Status: draft for review. Date: 2026-09-23.
Supersedes the CLI sketches in `Readme.md` ("CLI ideas (spike)") and is
illustrated by the frozen working trees in `example/`.

## 1. Purpose

`gfs` mirrors a net service (IMAP/SMTP, Jira, Slack, Confluence, DNS, X, ...)
as a directory of XML files, so that humans and coding agents can read, search
and change the service with ordinary file tools. The CLI borrows git's
vocabulary. The remote is a live, mutating service, not a content-addressed
store, and `gfs` keeps no history graph.

Design principle, in one sentence: **a file change means "make the remote look
like my working tree"; anything more than that is written explicitly into the
file's envelope; anything irreversible is gated by policy.**

### Non-goals

* Version control. There is one base snapshot per file, not a history.
* Converting service content. Bodies are stored in the service's native
  format (text, HTML, Confluence storage, Slack mrkdwn). No Markdown
  rendering in either direction.
* Multi-remote working trees. One directory is one remote (see open questions).
* A daemon or FUSE mount. Everything happens in `clone`, `pull`, `commit`.

## 2. Concepts

| term          | meaning                                                                             |
|---------------|-------------------------------------------------------------------------------------|
| remote        | one service instance and scope, identified by URL: `jira://instance/ABC`            |
| adapter       | the per-service module that maps remote resources to files and back                 |
| working tree  | the cloned directory; every regular file in it is a resource or a pending resource  |
| base          | the canonical content of each file as last seen from the remote, kept in `.gfs/base/` |
| resource      | one remote object (email, issue, page, day of channel messages, zone, post)         |
| sub-resource  | an object owned by a resource and shown inside its file (comment, worklog, reply, record) |
| envelope      | the `<gfs>` root element of every file; carries sync state, never service data      |
| content       | the `<content>` child of the envelope; holds exactly one resource root              |
| action        | one remote operation derived from the diff between base and working tree            |
| verb          | the class of an action: `create`, `update`, `delete`, `move`, or an explicit verb such as `send` |
| policy        | per-verb setting `allow`, `ask`, or `deny` in `.gfs/config`                          |

## 3. Commands

```
gfs clone <url> [<dir>]
gfs status [<path>...]
gfs diff   [<path>...]
gfs commit [--dry-run] [--allow <verb>]... [--no-merge] [<path>...]
gfs pull   [--force] [<path>...]
gfs resolve (--ours | --theirs) <path>...
gfs log    [-n <count>] [<path>...]
gfs actions
gfs get    <path>...
```

`gfs get` downloads attachment bytes on demand; see
`2026-09-23-gfs-attachments-design.md`.

All commands except `clone` locate the working tree by walking up from the
current directory to the nearest `.gfs/`. Paths are relative to the current
directory, as in git. No path means the whole tree.

### 3.1 clone

Creates `<dir>` (default: derived from the URL by the adapter), writes
`.gfs/config`, fetches the remote scope, writes every resource as a file in
canonical form, and copies each file into `.gfs/base/`. Credentials are
resolved by the adapter (environment, keyring, or prompt); how they are stored
is out of scope for this document.

### 3.2 status

Compares working tree to base and prints one line per changed file with the
**resolved** remote action, because that is what the user is about to do.

```
On remote imap+smtp://kbogdan@dwa.ovh
  A  drafts/notes.xml                  store in Drafts
  M  drafts/re-dh.xml                  SEND via smtp, then move to sent/       [ask]
  R  inbox/x.xml -> archive/2026/x.xml move
  D  junk/y.xml                        delete                                  [ask]
  !  drafts/to-typo.xml                SEND via smtp   failed: 550 5.1.1 Recipient address rejected
  C  eng/Home/Architecture.xml         conflict with remote v8 (1 element, 1 hunk)
```

| letter | meaning                                                                   |
|--------|---------------------------------------------------------------------------|
| `A`    | new file, not in base                                                     |
| `M`    | content differs from base                                                 |
| `R`    | same resource (by id) at a different path                                 |
| `D`    | in base, not in working tree                                              |
| `!`    | last commit failed for this file; details in the envelope's `<errors>`    |
| `C`    | unresolved merge conflict; envelope has `<conflict/>`, content has markers |

Files with several actions (a Jira file with a field change and a new comment)
print the actions indented under the file line. `[ask]` and `[deny]` mark the
policy class of the action.

### 3.3 diff

Unified diff between base and working tree in canonical form, preceded by the
same resolved action header as `status`. Because both sides are canonical,
reordering attributes or records never shows as a change.

### 3.4 commit

Executes the resolved actions. Detailed algorithm in section 6.

* `--dry-run`: resolves everything, including checks the adapter can perform
  against the remote without side effects (recipient MX, transition
  availability, post length), prints the plan, executes nothing. Exit code as
  if it had run, so scripts can gate on it.
* `--allow <verb>`: treats `ask` as `allow` for that verb in this run. Repeatable.
* `--no-merge`: if the remote moved since base, refuse the file instead of
  merging. Git-push-style strictness.
* Paths restrict the commit to matching files. Files not listed are untouched.

### 3.5 pull

Brings the working tree up to date with the remote. Algorithm in section 7.
`--force` takes the remote version for files marked `C`, discarding local edits.

### 3.6 resolve

Resolves a conflicted file wholesale by picking one side, removes markers and
the `<conflict/>` element, and leaves the file ready to commit. Nothing is sent.

### 3.7 log

Prints `.gfs/log`, newest last, optionally filtered by path.

### 3.8 actions

Lists the explicit verbs the current adapter understands, with their policy
class and parameters.

## 4. File model

### 4.1 One document shape

Every file is one well-formed XML document:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <mail id="1873" message-id="&lt;9f3c2a@example.com&gt;" date="2026-09-01T10:14:02+02:00">
      <from>Bob &lt;bob@example.com&gt;</from>
      <to>kbogdan@dwa.ovh</to>
      <subject>Re: Diffie–Hellman key exchange</subject>
      <body type="text/plain">Hi Carol, ...</body>
    </mail>
  </content>
</gfs>
```

* **`<gfs>` is the envelope.** It carries sync state and nothing else
  (section 4.3). At rest it is bare: no attributes, `<content>` as its only
  child.
* **`<content>` holds exactly one resource root.** The root's name is the
  resource type (`mail`, `issue`, `messages`, `page`, `zone`, `post`) and is
  declared by the adapter. Nothing inside `<content>` is reserved by `gfs`.
* **Elements are what the user writes.** Fields, bodies, sub-resources.
* **Attributes are what the service owns.** Identity, versions, timestamps,
  authors. `gfs` writes them; a local change to one is a commit error. A
  sub-resource element without its identity attribute is new.
* **Lists are repeated elements**, never delimited strings.
* **Body elements carry the service's native content**, declared by `type`.
  `text/*` bodies are character data (escaped or CDATA). `application/xhtml+xml`
  bodies contain the markup as child elements, including foreign namespaces
  such as Confluence's `ac:` and `ri:`. `gfs` never interprets a body; the
  adapter passes it through.

### 4.2 Canonical form

The canonical printer is the single most important shared component: base,
local and remote are compared as canonical text, and the merge is line-based
over it.

* XML declaration on line 1, `<gfs>` start tag on line 2.
* UTF-8, LF, two-space indentation, one element per line for element-only
  content, start tag with all attributes on one line.
* **No whitespace changes inside mixed content** (text with inline elements),
  because whitespace is significant there. A text body is printed exactly as
  received.
* Attributes in the adapter's declared order; unknown attributes sorted after.
* Sub-resources in the adapter's declared sort order (usually creation time),
  so a local reorder is not a change.
* Any character data containing `<` or `&` is printed as CDATA, so files at
  rest are always safe to edit without thinking about escaping.
* Empty optional elements are omitted.
* The printer is idempotent: `canon(canon(x)) == canon(x)`.

### 4.3 Envelope contents

| in `<gfs>`                                   | written by | meaning                                                                            |
|----------------------------------------------|------------|------------------------------------------------------------------------------------|
| `action="<verb>"`                            | user       | explicit verb for this file, imperative (`send`). Needed only where a file change is ambiguous |
| `<param>="<value>"`                          | user       | parameter of the verb, as declared by `gfs actions`                                |
| `<errors>` with one or more `<error action= target= code= at=><msg>...</msg></error>` | gfs | last commit failed; one entry per failed action; removed on the next attempt |
| `<conflict remote-version= by= at= elements= hunks=/>` | gfs | content contains merge markers; commit refuses until the element is removed |

`target` in `<error>` names the sub-resource the action was for, as a simple
path from the resource root (`comment[3]`, `record[type=A][name=@]`), and is
absent for actions on the resource itself.

On successful commit the envelope is written bare. On failure the user's
`action` and parameters stay and `<errors>` is added or replaced.

### 4.4 Lenient input

A new file may be a bare resource root with no envelope and no XML
declaration; commit accepts it and writes it back canonical. Indentation and
attribute order on input are irrelevant. Everything else (well-formedness,
schema, read-only attributes) is enforced.

### 4.5 Paths and filenames

Each adapter declares a **path model**:

* `tree`: the path is meaningful (IMAP folders, Slack channels and days,
  Confluence page hierarchy). Renaming or moving a file is a `move` action.
* `flat`: the path is derived from fields by a filename template
  (`<key> <summary>.xml` for Jira, `<date> <subject>.xml` for mail inside a
  folder). A local rename is a no-op with a warning; commit and pull re-derive
  the name.

Mail is `tree` for directories and `flat` for the filename within a directory.
Derived filenames are sanitised (path separators and control characters
replaced) and de-duplicated with a numeric suffix. Identity across renames is
the resource root's identity attribute, backed by `.gfs/index`.

No directory name is reserved. If the remote has an `outbox` folder the
working tree has an `outbox/` directory and it means nothing to `gfs`.

## 5. Actions and policy

### 5.1 Implicit actions

Without an `action` on the envelope, the diff between base and working tree
resolves to:

| working tree change                              | verb     | notes                                              |
|--------------------------------------------------|----------|----------------------------------------------------|
| new file                                         | `create` | for mail: store in the folder, never send          |
| element text or list differs                     | `update` | one action per field group, per adapter            |
| new sub-resource element without identity        | `create` | reported under the file                            |
| changed sub-resource element                     | `update` |                                                    |
| sub-resource element removed                     | `delete` |                                                    |
| file removed                                     | `delete` |                                                    |
| file moved (`tree` model)                        | `move`   |                                                    |
| file renamed (`flat` model)                      | none     | warning                                            |

Some field updates map to special remote calls (a Jira `<status>` change is a
transition). The adapter owns that mapping; from the user's side it is an
`update`. If the remote refuses (no transition path), the envelope gets an
`<error>` like any other failure.

### 5.2 Explicit verbs

Required only where the filesystem change is ambiguous. Today that is:

| adapter | verb   | meaning                                                                 |
|---------|--------|-------------------------------------------------------------------------|
| mail    | `send` | send via SMTP, then move the file to the adapter's sent folder          |

Adapters may add verbs; `gfs actions` lists them. The bar for adding one is
"a file change cannot express this without guessing".

### 5.3 Policy

`.gfs/config`:

```
[policy]
send    = ask
delete  = ask
publish = ask
```

* `allow`: run. `ask`: prompt `y/N` per action on a TTY; **on a non-TTY `ask`
  behaves as `deny`**, so unattended runs never fire a gated action by
  accident. `deny`: skip and report.
* Defaults: every verb `allow` except `send`, `delete`, `publish` which are
  `ask`. Adapters may tighten defaults for their verbs (X classes its
  implicit `create` as `publish`).
* `--allow <verb>` on `commit` overrides `ask` for that verb for that run.
  This is deliberately a distinct command-line shape so that an agent's
  permission system can permit `gfs commit` and deny `gfs commit --allow`.
* Denied or skipped actions leave the file unchanged; `status` keeps showing
  them.

## 6. Commit algorithm

For each candidate file, independently, in this order: creates, updates,
moves, deletes. Files never block each other; a failure is recorded and the
run continues. Exit code 0 if every action succeeded, 1 if any failed, was
denied, or conflicted, 2 on usage or configuration errors.

Per file:

1. **Parse and validate** locally: well-formedness, envelope, exactly one
   resource root, adapter schema, read-only attributes unchanged. Refuse the
   file if it contains conflict markers or a `<conflict/>` element. Any error
   here is reported and the file is skipped; nothing is fetched.
2. **Resolve actions** from base vs. working tree plus the envelope `action`.
   Apply policy; prompt or deny as configured.
3. **Fetch the remote version** of the resource (skipped for `create`) and
   canonicalise it.
   * Equal to base: proceed with the local content.
   * Different and `--no-merge`: refuse, mark `C` with a `<conflict/>`
     summary, no markers.
   * Different: three-way merge (section 8). Clean: continue with the merged
     content and print `merged` in the report. Conflict: write markers and
     `<conflict/>`, mark `C`, skip the file.
4. **Execute** the actions through the adapter, passing the remote version
   token from step 3 as an optimistic lock where the API supports one
   (Confluence `version`, Slack `ts`, IMAP UID/MODSEQ). A lock failure is
   treated as "remote moved": go back to step 3 once, then fail.
5. **Write back.** Re-fetch the resource after the actions and overwrite the
   file with its canonical form: bare envelope, assigned identity attributes,
   derived filename, server timestamps, provider changes (footer appended to
   a sent mail). Rename or move the file if the adapter says so (mail `send`
   moves to the sent folder; Jira `create` renames to `<key> <summary>.xml`).
6. **Update base and index** with the written-back content, then append to
   `.gfs/log`.

On failure at step 4: keep the user's `action` and parameters, write
`<errors>`, leave content as the user wrote it, leave base untouched, log
`FAIL`.

Partial failure inside one file (field update succeeded, comment create
failed) is reported per action and per `<error>`; write-back still happens for
the resource so the file reflects what the remote now holds, and the failed
sub-resource keeps its local form (the new element stays without identity) so
a retry is a plain re-commit.

Report format, one line per action:

```
<verb>  <path>[ -> <new path>]   <ok|FAIL|denied|merged>  [<detail>]
<n> actions, <n> failed, <n> denied
```

## 7. Pull algorithm

For each remote resource in scope:

* **Not in index**: write the file, add to base and index. Report `+`.
* **In index, remote equals base**: nothing.
* **In index, remote changed, local equals base**: overwrite, update base.
  Report `~`.
* **In index, remote changed, local changed**: three-way merge into the
  working tree. Clean: write merged content, update base to the new remote
  version (not to the merged content). Report `~ merged`. Conflict: markers
  and `<conflict/>`, report `C`; base is still updated so the markers are
  against the true remote.
* **Moved remotely** (same identity, new path): move the local file. If the
  local file is also modified, move it and then apply the rules above.
* **Deleted remotely**: delete the local file if it equals base, else keep it,
  report `C deleted on remote`, and let the user delete it or re-create it by
  committing.

Local files not in the index (new, uncommitted) are never touched.
`--force` replaces `C` files with the remote version. Remote scope is
adapter-defined and may be incremental (IMAP since last UID, Slack since last
`ts`, Jira `updated >=`).

## 8. Merge and conflicts

Inputs: base `B`, local `L`, remote `R`, all in canonical form. One engine:

1. **Structural pass** over the resource root. For each field element and
   each sub-resource (matched by identity attribute): if `L == B` take `R`;
   if `R == B` take `L`; if `L == R` take it; otherwise mark the element as
   conflicting. Sub-resources present only in `L` without identity are new
   and always kept. A sub-resource deleted on one side and changed on the
   other conflicts.
2. **Textual pass** inside each conflicting element and inside every body:
   line-based diff3 over canonical text. Non-overlapping hunks merge;
   overlapping hunks conflict.
3. **Validation.** The merged document must be well-formed and pass the
   adapter schema; if it does not, the file conflicts even when diff3 was
   clean.

Conflict markers are git's, with the base section always present, placed in
`<content>` at the lines that conflict:

```
<<<<<<< local
        <label>reviewed-2026q3</label>
||||||| base
=======
        <label>needs-review</label>
>>>>>>> remote v8
```

The envelope gets `<conflict remote-version="8" by="bob" at="..."
elements="1" hunks="1"/>`. A conflicted file is deliberately not well-formed.
`commit` refuses any file containing a marker line or a `<conflict/>`
element. Resolution is: edit, delete the `<conflict/>` element, commit.
`gfs resolve --ours|--theirs` does that wholesale.

Because commit re-runs step 3 of section 6 after resolution, a remote that
moved again in the meantime produces a fresh conflict rather than a silent
overwrite.

## 9. `.gfs/` layout

```
.gfs/
  config     INI: [remote] url, adapter options; [policy] verb = allow|ask|deny
  base/      mirror of the working tree in canonical form as last synced
  index      path <-> identity <-> remote version token, one line per resource
  attachments  fetched attachments: ids, version, sha256, size, mtime, path (attachments spec)
  log        append-only: <timestamp> <verb> <path>[ -> <path>] <ok|FAIL> <detail>
  lock       present while a command runs; commands refuse to run concurrently
```

Everything under `.gfs/` is regenerable from the remote except `config` and
`log`. Deleting `base/` and `index` followed by `pull` rebuilds them, treating
every local file as unchanged if it equals the remote.

## 10. Adapter contract

An adapter provides, declaratively where possible:

* URL scheme(s) and credential resolution.
* Resource root element name, path model (`tree`, `flat`, or per level),
  filename template, sanitisation.
* Schema: elements (name, type, list or scalar, canonical order), attributes
  (name, read-only, canonical order), sub-resource elements with their
  identity attribute and sort key, nesting depth, body elements with allowed
  `type` values.
* Explicit verbs with parameters and default policy class.
* Operations: list (optionally incremental), fetch, create, update (field
  group, body, sub-resource), delete, move, execute verb, dry-run checks.
* Version token per resource and whether the API accepts it as a lock.
* Mapping from field updates to special calls (transition, rename) where the
  API is not a plain update.

Everything else (parsing, canonical printing, status, diff, merge, policy,
base, index, log, write-back) is shared and adapter-independent.

### 10.1 Adapter specifics

| adapter    | root         | path model                                            | identity / lock                | body types                         | notable behaviour                                                                                       |
|------------|--------------|-------------------------------------------------------|--------------------------------|------------------------------------|---------------------------------------------------------------------------------------------------------|
| mail       | `<mail>`     | tree (folders), flat (`<date> <subject>.xml`)         | IMAP UID; MODSEQ               | `text/plain`, `text/html`          | new file stores (APPEND); `send` verb sends and moves to sent; write-back adds `message-id`, provider footer; `<attachment>` elements are metadata only |
| jira       | `<issue>`    | flat `<key> <summary>.xml`                            | issue id; `updated`            | `application/xhtml+xml`            | new file creates and is renamed; `<status>` update maps to a transition; sub-resources `<comment>`, `<worklog>` |
| slack      | `<messages>` | tree `channels/<name>/<date>.xml`, `dm/<user>/<date>.xml` | message `ts` (lock for edits) | `text/mrkdwn` on the root        | posting is implicit create of `<message>`; `<reply>` nests one level; new day file allowed, future dates warn; edits of others' messages fail with the API error |
| confluence | `<page>`     | tree `<Space>/<Title>.xml`, children in `<Title>/`    | page id; `version` (lock)      | `application/xhtml+xml` with `ac:`/`ri:` | renaming the file renames the page; `version` bumped by gfs; `<comment>` sub-resources; storage format passed through untouched |
| dns        | `<zone>`     | flat, one file per zone                               | record id; none                | none                               | `<record type name ttl [priority] [proxied]>` elements; diff to per-record calls; `delete` is `ask`     |
| x          | `<post>`     | flat `posts/<date> <first line>.xml`                  | post id; none                  | `text/plain`                       | implicit create is classed `publish` (`ask`); editing a published body is an error; `<metrics>` refreshed on pull; threads as several `<post>` children |

## 11. Working with agents

The design choices that exist specifically for coding agents:

* One file shape for every service; an agent that has seen one adapter's
  files knows the rules for all of them.
* Adding a sub-resource is inserting one element before a closing tag;
  requesting a send is a one-line edit to the envelope's start tag. No
  parser required on the agent side; `xmllint` is enough to check the result.
* `status` and `--dry-run` print resolved actions, so an agent can check its
  intent before it fires.
* `ask` on a non-TTY is `deny`. Unattended runs must say `--allow <verb>`
  explicitly, and that shape is distinguishable by command-prefix permission
  systems.
* Errors land in the envelope of the file the agent is already looking at,
  structured and escaped, not only on stderr.
* `.gfs/log` is the audit trail after the fact.

## 12. Testing

* **Canonical printer**: idempotence on a fixture corpus per adapter; mixed
  content byte-identical after a round trip; CDATA emitted exactly when
  needed.
* **Format fixtures** per adapter: pairs of remote payload and canonical file,
  and payload → file → payload equivalence under the remote's own
  normalisation.
* **Merge table tests**: every rule of section 8 with at least one clean and
  one conflicting case; marker output is golden; the validation step catches
  a diff3-clean but ill-formed result.
* **Fake remotes** implementing the adapter operations in memory, to test the
  commit and pull algorithms end to end: fast path, merged path, conflict
  path, lock failure, partial failure with `<errors>`, denied policy on TTY
  and non-TTY, lenient input wrapped on write-back.
* **Confluence corpus check** before shipping that adapter: pull a few dozen
  real pages, canonicalise, push unchanged, pull again, assert no diff.

## 13. Open questions

* **Multi-remote roots.** `gfs/mail`, `gfs/jira`, `gfs/slack` under one parent
  and an agent that wants one `gfs status` for all. Deferred; each is its own
  tree for now.
* **Attachments.** Designed in `2026-09-23-gfs-attachments-design.md`
  (metadata elements always, bytes fetched on demand with `gfs get`).
* **Credential storage.** Adapter-resolved; no shared design yet.
* **Schema publication.** Whether adapters ship an XSD or RELAX NG so editors
  can validate and complete; the internal schema exists either way.
* **Staging area.** No `gfs add`. Revisit if partial commits by path prove
  insufficient.
* **Clone size.** Mailboxes and Slack history need a window (`--since`,
  `--folders`). Adapter option, syntax not fixed.
* **Slack scheduled posts and X threads** are sketched in `example/` but not
  specified here.
