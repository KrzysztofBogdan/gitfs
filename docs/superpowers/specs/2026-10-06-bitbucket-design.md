# Bitbucket adapter design: pull requests and pipelines

Status: design approved in conversation 2026-10-06. Date: 2026-10-06.
Adds a `bitbucket` adapter (`bitbucket://`) for Bitbucket Cloud and a new
command, `gfs tail`. Builds on `2026-09-23-gfs-cli-design.md`,
`2026-09-23-gfs-attachments-design.md`, `2026-09-24-gfs-credentials-design.md`
and follows the selection model and people handling of
`2026-09-29-jira-adapter-design.md`.

API facts marked *(verify)* are from Bitbucket's documentation, not yet
measured; the real-site checks (§11.4) confirm them before the adapter is
called done.

## 1. Purpose

Mirror the pull requests and pipeline runs of a Bitbucket Cloud workspace as
files. Pull requests are read and changed by editing files (title,
description, reviewers, comments, review state, merge, decline, new PRs).
Pipeline runs are read-only files with their step logs; `gfs tail` streams
the logs of a running pipeline.

### Goals

* `gfs clone bitbucket://bitbucket.org/<workspace>` clones every repository
  the account can read, narrowed by `filter`, `exclude`, `since`, `limit`.
* Every PR in every state and every pipeline run, each in full detail
  (clone may take hours on busy repos: §6.6).
* PR verbs are declarative: `<state>merged</state>` merges,
  `<my-review>approved</my-review>` approves.
* Step logs up to `pipelineLimitSize` (default 1 KB) inline; bigger ones on
  demand with `gfs get`.
* `gfs tail <run-or-pr-file>` streams a running pipeline's logs and exits
  with its result.
* Merge and decline ask first.

### Non-goals

* Bitbucket Data Center / Server.
* Triggering, stopping or re-running pipelines; pipeline variables,
  schedules, settings, SSH keys, OIDC.
* Test reports, build artifacts, deployments and environments.
* PR tasks, comment reactions, the PR diff (git's job), commit comments.
* Repository, branch, branch-restriction or webhook management.
* Workspace / repository access tokens (Bearer auth). Possible later; they
  are what unlocks scaled rate limits.

## 2. Packages

| package | role |
|---------|------|
| `internal/adapter/bitbucket` (new) | the adapter (files in §2.1) |
| `internal/adapter/bitbucket/bbtest` (new) | fake Bitbucket API |
| `internal/adapter/atlassian` | `APIError.Message` learns Bitbucket's error shape `{"type":"error","error":{"message","detail","fields"}}`; `Client` gains a `Range` download (§8.2) and exposes the last `X-RateLimit-NearLimit` |
| `internal/adapter` | new optional interface `Tailer` (§9.4) |
| `internal/policy` | defaults gain `merge = ask`, `decline = ask` |
| `internal/cli` | registers the adapter; new `tail.go` (§9); `gfs help bitbucket` |
| `internal/engine` | no change expected: flat path model, partial listings, read-only attachments, Advisor exist |

### 2.1 Files in `internal/adapter/bitbucket`

| file | knows about |
|------|-------------|
| `adapter.go` | registration, `Name`, `Schemes`, `PathModel` (`Flat`), `DefaultDir`, `Describe`, `Open`, `Login`, `Normalize` |
| `url.go` | URL forms, selection parameters, sizes (`1kb`), normalisation (§3) |
| `client.go` | base URL, paging (`next` links), `q=` building, rate-limit hint |
| `session.go` | `List`, `Fetch`, `Download`, cursor (§6) |
| `pr.go` | PR + comments + statuses JSON ↔ `<pullrequest>` (pure) |
| `run.go` | pipeline + steps + logs JSON ↔ `<pipeline>` (pure) |
| `people.go` | workspace members cache, user resolution (§5.4) |
| `apply.go` | `Check` and `Apply` for PR files; refusal for run files |
| `advise.go` | `Available` for `gfs actions` (§7.7) |
| `tail.go` | `Tail` (§9) |
| `schema.go`, `relaxng.go` | `<pullrequest>` and `<pipeline>` schemas |

`pr.go` and `run.go` are pure and tested without HTTP.

## 3. Remote URL and selection

```text
bitbucket://[<email>@]bitbucket.org/<workspace>[?filter=r1,r2&exclude=r3&since=<date>&limit=<n>&pipelineLimitSize=<size>]
bitbucket://[<email>@]bitbucket.org/<workspace>/<repo>          one repository
bitbucket:https://bitbucket.org/<workspace>/<repo>/…            a pasted browser URL: its repository
```

| parameter | meaning | default |
|-----------|---------|---------|
| `filter` | exactly these repository slugs | every repository you can read |
| `exclude` | removed from the selection | none |
| `since` | PRs updated / runs created on or after: `2026-01-01`, `-90d`, `-2w`, `-6m` | none |
| `limit` | at most n most recently updated PRs and n newest runs per repository: `500`, `5k` | none |
| `pipelineLimitSize` | inline log ceiling: `0`, `512`, `1kb`, `64kb`, `1mb` | `1kb` |

* `since` and `limit` apply when a repository is listed in full (clone, a
  repository new to the selection, `pull --full`), as in Jira §3.1.
* A pasted browser URL names its repository: `/<ws>/<repo>`,
  `/<ws>/<repo>/pull-requests/142`, `/<ws>/<repo>/pipelines/results/1873`, …
  `/<ws>` alone means the workspace.
* Normalised form: `bitbucket://bitbucket.org/<workspace>[/<repo>]` with the
  query parameters sorted; a pasted URL becomes `?filter=<repo>`.
* Default folder: the repository slug for one repository, else the workspace.

### 3.1 Credentials

* Bitbucket Cloud takes an Atlassian **API token with Bitbucket scopes**
  over Basic auth (username = Atlassian account email). It is not the same
  token as Jira/Confluence; an unscoped Atlassian token is refused *(verify)*.
* Resolved by `atlassian.Creds` with prefix `GFS_BITBUCKET` and host
  `bitbucket.org`: `gfs auth set me@acme.io --host bitbucket.org`.
* `gfs auth login bitbucket://bitbucket.org/<ws>` opens
  `https://id.atlassian.com/manage-profile/security/api-tokens`, says to
  pick "Create API token with scopes" → Bitbucket and tick
  `read:user:bitbucket`, `read:workspace:bitbucket`,
  `read:repository:bitbucket`, `read:pullrequest:bitbucket`,
  `write:pullrequest:bitbucket`, `read:pipeline:bitbucket` *(verify names)*,
  reads the email and token, checks them with `GET /2.0/user` and
  `GET /2.0/repositories/<ws>?pagelen=1`, and stores them.
* A 401 adds `(run gfs auth login bitbucket://bitbucket.org/<ws>)`; a 403
  on one endpoint names the scope it needs.

## 4. Layout

```text
acme/
├── .gfs/cache/bitbucket/          workspace members (people.json)
├── api-gateway/                   repository api-gateway
│   ├── pr/
│   │   ├── 142 Add rate limiting.xml
│   │   └── 97 Drop v1 routes.xml
│   └── pipelines/
│       ├── 1873.xml
│       ├── 1873.files/            big step logs, fetched with gfs get
│       │   └── 1-build.log
│       └── 1872.xml
└── billing/
    └── …
```

* Every repository is a folder named by its slug, also in a one-repository
  clone (paths do not change when the selection grows).
* PR file name: `<id> <title>.xml`, the title sanitised as for Jira
  summaries (`issuePath`). Editing `<title>` renames the file on commit;
  renaming the file yourself does nothing.
* Run file name: `<build_number>.xml`.
* A new file in `<repo>/pr/` creates a PR in that repository. A new file
  anywhere else (including `pipelines/`) is refused.
* Resource ids, unique across the tree: `<repo>/pr/<id>` and
  `<repo>/pipelines/<uuid>` (the uuid without braces). Repository renames
  appear as one repository leaving the selection and another joining.

## 5. File format

### 5.1 Pull request

```xml
<pullrequest id="api-gateway/pr/142" number="142" created="2026-10-01T09:12:00Z" updated="2026-10-05T14:22:10Z" version="2026-10-05T14:22:10Z">
  <title>Add rate limiting</title>
  <state>open</state>
  <draft>false</draft>
  <source branch="feat/rate-limit" commit="a1b2c3d4e5f6"/>
  <destination branch="main" commit="0f9e8d7c6b5a"/>
  <close-source-branch>true</close-source-branch>
  <author account="712020:aa…">Ana Nowak</author>
  <reviewer account="712020:bb…" state="approved">Bo Smith</reviewer>
  <reviewer account="712020:cc…" state="changes_requested">Cy Lee</reviewer>
  <participant account="712020:dd…" state="approved">Dee Park</participant>
  <my-review>none</my-review>
  <merge-commit/>
  <description>Markdown, as Bitbucket stores it (`content.raw`).</description>
  <build key="pipeline-1873" state="FAILED" url="https://bitbucket.org/…" run="../pipelines/1873.xml">Pipeline #1873 for feat/rate-limit</build>
  <comment id="9012" account="712020:bb…" author="Bo Smith" created="…" updated="…">General remark</comment>
  <comment id="9013" account="712020:cc…" author="Cy Lee" created="…" updated="…" path="src/limit.go" line="42" side="new" resolved="false">Off by one?</comment>
  <comment id="9020" account="712020:aa…" author="Ana Nowak" created="…" updated="…" parent="9013">Fixed.</comment>
</pullrequest>
```

| element / attribute | owner | notes |
|---|---|---|
| root `id`, `number`, `created`, `updated`, `version` | service | `version` = `updated_on` (§6.2 says how status-only changes are caught) |
| `title` | user | |
| `state` | user | `open`, `merged`, `declined`, `superseded`. Only `open` → `merged` / `declined` is an edit (§7.4); `superseded` is never written by the user |
| `state@strategy`, `state@message` | user, only with `merged` | `merge_commit`, `squash`, `fast_forward`; omitted = the destination's default. Not written by gfs; dropped after a successful merge |
| `draft` | user | `true` / `false` *(verify the PUT accepts it)* |
| `source@branch` | user on create, else service | |
| `source@commit`, `destination@commit` | service | |
| `destination@branch` | user | changing it retargets the PR |
| `close-source-branch` | user | |
| `author` | service | |
| `reviewer` (repeated) | user, except `state` | sorted by `account`; `state` = `approved`, `changes_requested` or absent |
| `participant` (repeated) | service | non-reviewers who commented or reviewed |
| `my-review` | user | `none`, `approved`, `changes_requested`: the account's own review |
| `merge-commit` | service | hash once merged |
| `description` | user | Markdown text; empty element = no description |
| `build` (repeated) | service | commit statuses of `source@commit`, sorted by `key`; `run` when the status is a pipeline of this tree |
| `comment` (Sub, id `id`, sorted by `created`) | see §5.3 | |

### 5.2 Which fields appear

Every element above is always written, empty when unset, except `build`,
`reviewer`, `participant` and `comment`, which appear once per item.
Comments with `deleted: true` are omitted.

### 5.3 Comments

* Service-owned attributes: `id`, `account`, `author`, `created`,
  `updated`. On an existing comment `parent`, `path`, `line`, `side` are
  also fixed (changing them is refused in Check).
* Text is the comment's Markdown (`content.raw`).
* A reply is a flat `<comment parent="<id>">`. Threads are rebuilt by
  readers from `parent`; sorting stays by `created`.
* Inline comments: `path` (file in the repository), `line`, `side`
  (`new` → API `inline.to`, `old` → `inline.from`).
* `resolved` (`true`/`false`, top-level comments only) is user-owned:
  flipping it resolves or reopens the thread.
* A new comment has no `id`; it may carry `parent`, or `path`+`line`
  (+`side`, default `new`).
* Editing or deleting another user's comment is refused in Check:
  `comment 9012 is Bo Smith's; you can only edit your own`.

### 5.4 People

* A user is `<element account="<account_id>">Display Name</element>`, as in
  Jira §5.7. Bitbucket hides emails.
* A new reviewer is written without `account`; its text is a display name,
  a Bitbucket nickname or an account_id. Commit resolves it against the
  workspace members (`GET /2.0/workspaces/<ws>/members`, cached in
  `.gfs/cache/bitbucket/people.json`, refreshed once per run when a name is
  not found): exact account_id, then a unique exact nickname, then a unique
  exact display name. Ambiguous or not found: an error listing candidates
  with their account_ids.
* Editing the text while keeping `account` is refused.
* The PR author cannot be a reviewer; Bitbucket's error is reported.

### 5.5 Pipeline run (read-only)

```xml
<pipeline id="api-gateway/pipelines/0b1c…" number="1873" created="2026-10-05T14:22:31Z" completed="2026-10-05T14:30:02Z" version="completed:failed:2026-10-05T14:30:02Z">
  <state>completed</state>
  <result>failed</result>
  <trigger type="push" account="712020:aa…">Ana Nowak</trigger>
  <target type="pullrequest" ref="feat/rate-limit" commit="a1b2c3d4e5f6" pipeline="pull-requests:**" pr="142"/>
  <duration build-seconds="402">451</duration>
  <step id="5d6e…" n="1" name="Build" image="golang:1.25" state="completed" result="successful" started="…" completed="…" duration="122" log-size="48213"/>
  <step id="7f80…" n="2" name="Lint" parallel="checks" state="completed" result="failed" started="…" completed="…" duration="31" log-size="612">
    <log>golangci-lint run
src/limit.go:42: …</log>
  </step>
  <step id="91a2…" n="3" name="Test" parallel="checks" stage="" state="completed" result="successful" duration="44" log-size="980">
    <log>…</log>
  </step>
  <steplog step="5d6e…" name="1-build.log" size="48213"/>
</pipeline>
```

* The whole resource is service-owned. `Check` refuses any change to a
  `pipelines/` file: `pipeline runs are read-only (use gfs tail to watch one)`.
* `state`: `pending`, `in_progress`, `paused`, `halted`, `completed`
  (Bitbucket `state.name`, or `state.stage.name` while in progress,
  lower-cased). Unknown values pass through lower-cased.
* `result`: `successful`, `failed`, `error`, `stopped`, `expired`; absent
  until completed.
* `trigger@type`: `push`, `manual`, `schedule` (`trigger.type` lower-cased,
  `_TRIGGER` suffix dropped). The person is the run's `creator`.
* `target@type`: `branch`, `tag`, `commit`, `pullrequest`;
  `target@pipeline` is the selector: `default`, `branches:<pattern>`,
  `tags:<pattern>`, `custom:<name>`, `pull-requests:<pattern>`.
* `target@pr` is the PR number. There is no link to the PR file: a
  completed run is never fetched again, so a path would go stale when the
  PR's title (and file name) changes.
* `step` is a Sub (id `id`) ordered by `n` (Bitbucket's step order, numeric
  compare). `parallel` names the parallel group, `stage` the stage, when
  Bitbucket reports them.
* Logs: a step's log of at most `pipelineLimitSize` bytes is its `<log>`
  child: `\r\n` and lone `\r` become `\n`, ANSI escape sequences are removed,
  other characters XML cannot hold and invalid UTF-8 become U+FFFD (the size
  limit applies to the raw bytes). `gfs get` and `gfs tail` give raw bytes. A bigger log
  is a read-only attachment `<steplog step="<step id>" name="<n>-<slug>.log"
  size="…"/>` whose bytes `gfs get` writes to `<number>.files/<name>`.
  `log-size` is on every step that has a log.
* A step that never ran (skipped, not reached) has no `log-size`.

## 6. Pull

### 6.1 Repositories

`GET /2.0/repositories/<ws>?pagelen=100&fields=…` (or the one repository),
narrowed by `filter`/`exclude`. Each listing call starts with this; it costs
one request per 100 repositories.

### 6.2 Listings are stubs

`List` returns stubs (id, path, version; no content). The engine fetches
each resource it does not have or whose version moved, one by one, saving
the index as it goes: a clone stopped after hours resumes with `gfs pull`
and fetches nothing twice. The PR and run JSON read while listing is kept
for the session, so `Fetch` right after `List` does not read it again.

* A PR stub's version is its `updated_on`; an **open** PR's stub has no
  version, so it is fetched on every pull and the engine compares content.
  That is how status-only and comment-only changes are caught.
* A completed run's stub version is `completed:<result>:<completed_on>`
  (the same as `Fetch` gives); a run not completed has none.

### 6.3 Full listing of a repository

Clone, `pull --full`, or a repository new to the selection:

1. PRs: `GET …/pullrequests?state=OPEN&state=MERGED&state=DECLINED&state=SUPERSEDED&sort=-updated_on&pagelen=50[&q=updated_on >= "<since>"]`,
   following `next`, stopping at `limit`.
2. Runs: `GET …/pipelines/?sort=-created_on&pagelen=100`, stopping at
   `limit` or a run created before `since`.
3. The repository folder goes in `Listing.FullDirs`.

`Fetch` of a PR reads `…/pullrequests/<id>/comments?pagelen=100` (paged)
and `…/statuses?pagelen=100` (plus the PR when it was not just listed). Of
a run: `…/pipelines/<uuid>/steps/?pagelen=100` and, per step that has
started, `…/steps/<uuid>/log` with `Range: bytes=0-<limit>` (§8.2).

### 6.4 Incremental listing

Per repository with a cursor:

* PRs: every open PR (`state=OPEN`, paged), then every PR with
  `q=updated_on > "<cursor.prs - 10 min>"` sorted ascending.
* Runs: newest first until a `build_number` at or below the cursor's
  highest; then `GET …/pipelines/<uuid>` for every run the cursor recorded
  as not completed, as a stub with its current version.
* A pull with nothing new: per repository one open-PR listing, one updated
  listing, one run listing, one request per unfinished run; then the
  engine fetches each open PR (2 requests).

### 6.5 Cursor

`Listing.Cursor` is JSON:
`{"<repo>": {"prs": <time listing started>, "run": <highest build_number>, "running": {"<uuid>": <build_number>}}}`.
A repository missing from it gets a full listing. A failed request fails
the listing (the engine keeps the old cursor); a stopped clone resumes as
§6.2 describes.

### 6.6 Rate limits and duration

Bitbucket allows 1,000 repository-data requests per hour for a user API
token (up to 10,000 with workspace access tokens on large paid plans,
out of scope). A full clone costs about 2 requests per PR and
`2 + steps` per run: a repository with 2,000 PRs and 5,000 runs takes about
24,000 requests, roughly a day. This is accepted (the user chose full
detail). Mitigations:

* 429 waits are honoured by `atlassian.Client` (Retry-After, capped
  backoff) and shown as the `wait` progress phase with the time left.
* The progress bar's ETA counts requests, not resources.
* `since` / `limit` narrow a clone; `gfs help bitbucket` shows the
  arithmetic and recommends them for large workspaces.
* Requests are sequential (one in flight); parallelism would only reach the
  limit sooner.

### 6.7 Selection changes and deletions

* A repository leaving the selection (config edited, deleted, access lost)
  is put in `Listing.FullDirs` with no resources: the engine removes its
  files (locally changed ones become conflicts).
* PRs cannot be deleted in Bitbucket; runs removed by retention are still
  listed (*(verify)*); a 404 on `Fetch` returns `adapter.ErrNotFound`.

### 6.8 `Fetch`

PR: `GET …/pullrequests/<id>` + comments + statuses. Run: `GET
…/pipelines/<uuid>` + steps + logs.

## 7. Commit

Only PR files have actions. A changed or new file under `pipelines/` is
refused in Check (§5.5).

### 7.1 Lock

Bitbucket's PR update takes no expected version. The engine fetches the PR
before `Apply` and merges or refuses (`--no-merge`) if it moved, as for
Jira §7.1. The race between that fetch and the write is accepted.

### 7.2 Actions and order

One file's actions run in this order:

1. `create` (new file): `POST …/pullrequests` with title, description,
   source branch, destination branch, reviewers, close-source-branch,
   draft. The id is written back and the file renamed.
2. `update`: every changed group among title, description, destination,
   reviewer, close-source-branch, draft, sent together in one
   `PUT …/pullrequests/<id>` (reviewers sent in full as `account_id`s).
3. `comment` create (in document order; `parent` must name a comment that
   already exists: Check refuses a reply to a comment created in the same
   commit, `reply to a comment that exists; commit the parent first`), update
   (`PUT …/comments/<id>`), resolve/reopen
   (`POST`/`DELETE …/comments/<id>/resolve`), delete
   (`DELETE …/comments/<id>`).
4. `review`: `my-review` changed: `none`→`approved`: `POST …/approve`;
   →`changes_requested`: `POST …/request-changes`; →`none`: `DELETE` the
   current one; `approved`↔`changes_requested`: `DELETE` the old, `POST`
   the new.
5. `merge` or `decline` (state changed), last, because it closes the PR.

A failed create skips the rest of the file. A failed update does not skip
later actions, except that a failed step before `merge` skips the merge
(a merge must not run on top of a half-applied edit). Earlier successful
actions are not undone.

### 7.3 Create

* Needs `title`, `source@branch`, `destination@branch`; missing ones are
  errors in Check. `destination` empty means the repository's main branch.
* `state`, if present, must be `open`. Comments in a new file are created
  after the PR. `my-review` must be `none`.
* Check confirms both branches exist (`GET …/refs/branches/<name>`).

### 7.4 Merge and decline

* `state` `open` → `merged`: `POST …/pullrequests/<id>/merge` with
  `merge_strategy` (from `state@strategy`, else omitted), `message`
  (`state@message`, else omitted), `close_source_branch` (the file's
  value). A `202` with a task URL is polled
  (`GET …/merge/task-status/<task>`, every 2 s, up to 5 min) *(verify)*.
* `state@strategy` must be one of the destination's
  `merge_strategies` (in the PR JSON); Check refuses others, naming the
  allowed ones.
* `open` → `declined`: `POST …/decline`.
* Any other change of `state` is refused:
  `a merged PR cannot be reopened`, `superseded is set by Bitbucket`.
* Merge failing on merge checks (missing approvals, failed builds):
  Bitbucket's message is the action's error, written into the file.

### 7.5 Policy classes

| action | class | default |
|---|---|---|
| create PR | `create` | allow |
| update fields | `update` | allow |
| comment create / update / resolve | `comment` | allow |
| comment delete | `delete` | ask |
| approve / request changes / unapprove | `review` | allow |
| merge | `merge` | ask |
| decline | `decline` | ask |

### 7.6 Describe

* `create PR feat/rate-limit → main in api-gateway`
* `update title, reviewers`
* `comment on src/limit.go:42`, `reply to comment 9013`, `resolve comment 9013`
* `approve`, `request changes`, `withdraw approval`
* `merge feat/rate-limit → main (squash), close source branch`
* `decline PR 142`

### 7.7 `gfs actions <pr-file>`

`Available` returns:

* `State`: `open, 2 approvals, builds: 1 failed` (or `merged`, …).
* Items: `merge` with the allowed strategies (default marked), `decline`,
  `approve` / `request changes` / `withdraw` depending on `my-review`.
  None for a closed PR.
* `Note`: when the local `<state>` is `merged`, what the merge will send.

### 7.8 Errors

Bitbucket errors are `{"type":"error","error":{"message","detail","fields"}}`.
`APIError.Message` returns `message` and `detail`, joined by `: `, plus
`fields` as `field: message`. They land in the file as for other adapters.

## 8. Logs

### 8.1 `gfs get`

`Download(resourceID, step id)` streams `GET …/steps/<uuid>/log` to the
sidecar file `<number>.files/<n>-<step slug>.log`. Attachment version is
`-` (logs have no versions); the size is checked against `size`. A log of
a run that is still running is downloaded as it is now, with a warning.

### 8.2 Range reads

`atlassian.Client.Range(ctx, path, from, to int64, w) (total int64, err)`:
sends `Range: bytes=<from>-[<to>]`.

* `206`: body written, `total` from `Content-Range: bytes a-b/<total>`
  (`*` → `-1`, unknown while the step runs *(verify)*).
* `200`: the server ignored Range; the body is read up to `to` and
  `total` = `Content-Length`.
* `416`: nothing at or past `from`; `total` from `Content-Range: */<total>`.
* `404` before a step has started: not an error (`total` = `-1`, nothing
  written).
* Redirects to object storage are followed; `Authorization` is dropped
  across hosts by Go, `Range` is kept.

During listing one `Range: bytes=0-<limit>` request per step decides
inline vs attachment and returns the inline bytes in the same call; when
`total` is unknown and exactly `limit+1` bytes came back, the log is
treated as big.

## 9. `gfs tail`

### 9.1 Usage

```shell
gfs tail <run-file>          # that run
gfs tail <pr-file>           # latest run on the PR's current source commit
```

| flag | meaning | default |
|------|---------|---------|
| `--wait <d>` | PR file: how long to wait for a run to start on the source commit | `2m` |
| `--interval <d>` | log poll interval (the step list is polled every 3 intervals) | `5s` |
| `--timeout <d>` | give up after d | none |

### 9.2 Output

stdout, no colour, the same bytes whether or not it is a terminal:

```text
── #1873 pull-requests:** feat/rate-limit a1b2c3d ──
── Build (golang:1.25) ──
go build ./...
── Build: successful in 2m02s ──
[Lint] golangci-lint run
[Test] ok  ./internal/limit 0.41s
── Lint: failed in 31s ──
── Test: successful in 44s ──
── #1873 failed in 7m31s ──
```

* Every step is printed from its first byte. A step header is printed when
  a step starts, a footer when it ends.
* While more than one step is running, each log line is prefixed with
  `[<step name>] `; a partial last line is held until its newline or the
  step's end.
* A run that has already finished prints all logs and exits at once, so
  `gfs tail` is also "show me the logs".
* stderr: `wait` messages (rate limit, waiting for a run to start).

### 9.3 Exit and write-back

| code | when |
|------|------|
| 0 | run `successful` |
| 1 | run `failed`, `error`, `stopped`, `expired` |
| 2 | gfs could not watch: bad path, adapter cannot tail, auth, network after retries, `--wait` or `--timeout` elapsed |
| 130 | Ctrl-C (watching stops; the pipeline does not) |

On every exit after the run was found (Ctrl-C included) the run's file is
written from its last state through the normal pull path for that one
resource (it creates the file when the run is new to the tree). For a PR
file, the PR is pulled the same way, so its `<build>` elements update and
local edits are merged, never clobbered.

### 9.4 Mechanics

* `adapter.Tailer` (optional, on the session):
  ```go
  type Tailer interface {
      Tail(ctx context.Context, id string, opts TailOpts, out io.Writer) (TailResult, error)
  }
  type TailOpts struct{ Wait, Interval time.Duration }
  type TailResult struct {
      RunID  string // the run that was watched (a PR id resolves to one)
      Result string // successful, failed, …; "" when ctx ended first
  }
  ```
  The CLI resolves the path to an id through the index, refuses sessions
  without `Tailer` (`jira cannot tail`), runs `Tail` with a context that
  ends on Ctrl-C or `--timeout`, then pulls `RunID` (and the PR id) and
  maps `Result` to the exit code.
* PR id: the adapter fetches the PR's current source commit, then lists
  `…/pipelines/?target.commit.hash=<hash>&sort=-created_on&pagelen=1`
  *(verify filter)*; none → poll every interval until `Wait`.
* Loop: every `Interval`, for each started and unfinished step, `Range`
  from its offset. Every third tick, and whenever a step ended, re-read the
  steps and the run. Stop when the run is `completed` and every step's log
  is read to its final size.
* Back-off: when a whole minute brings no new bytes the interval doubles
  up to 30 s; new bytes reset it. `X-RateLimit-NearLimit: true` sets it to
  30 s for the rest of the run.
* Cost: an 8-minute run with two steps at the defaults is about 130
  requests.
* If the real-site check shows a running step's log is not readable until
  the step ends, tail prints each step's log when the step ends (the header
  says `(log available when the step ends)`); output and exit codes are
  unchanged.

## 10. Errors

| case | message |
|---|---|
| edit to a run file | `pipeline runs are read-only (use gfs tail to watch one)` |
| new file outside `<repo>/pr/` | `new files go in <repo>/pr/; <path> is not a PR folder` |
| reviewer not found / ambiguous | `reviewer "Bo": no workspace member matches` / `… matches 2: Bo Smith (712020:bb…), Bo Li (…)` |
| state change not allowed | §7.4 |
| strategy not allowed | `merge strategy "rebase" not allowed on main; allowed: merge_commit, squash (default)` |
| `gfs tail` on a non-Bitbucket file | `<adapter> cannot tail` |
| `gfs tail` on another Bitbucket file | `gfs tail takes a pipeline run or a pull request file` |

## 11. Testing

### 11.1 Fake server (`bbtest`)

In-memory workspace with repositories, members, PRs (comments, statuses,
participants, merge strategies), pipelines (steps, logs). Implements: paged
listings with `next`, `q=updated_on>…` and `state=` filters, `sort`,
`fields` ignored, the PR write endpoints, merge returning `202` + task once,
Range on logs (206/200/416/404, `*` total while running), a run that
advances one step per N polls, 429 with Retry-After on demand,
`X-RateLimit-NearLimit` on demand, Bitbucket's error shape. Records every
request for assertions.

### 11.2 Unit tests

* `pr.go`, `run.go`: JSON → XML golden files; XML → API payloads; version
  digest changes when only a status changes.
* `url.go`: URL forms, pasted URLs, sizes, normalisation, default folder.
* `apply.go`: every row of §7.2 and §7.5 against the fake; read-only
  refusals; reply to a not-yet-created parent refused; merge skipped after a
  failed update; strategy check.
* `people.go`: resolution order, ambiguity, cache refresh.
* Logs: inline at exactly `limit`, attachment at `limit+1`, unknown total.
* `tail.go`: parallel prefixes, partial lines, back-off, NearLimit, PR
  waiting for a run, finished run dump, context cancel.
* Cursor: resume after a failed repository; runs not refetched once
  completed; open PRs' statuses refreshed.

### 11.3 End-to-end (`internal/cli/bitbucket_e2e_test.go`)

Clone a two-repository workspace from the fake; edit a PR (title, reviewer,
comment, reply, inline comment), status/diff/commit; pull with a remote
change merged; `my-review` approve; merge with `--allow merge`, refused
without it on a non-terminal; decline; create a PR from a new file; `gfs
get` a big log; `gfs tail` a run that ends failed (exit 1, file written);
`gfs tail` a PR waiting for its run; `gfs tail` on a Jira file (exit 2);
`gfs actions` on a PR.

### 11.4 Real-site checks (`realsite_test.go`, build tag as for Jira)

* Read: clone one named repository with `limit=20`, round-trip every file
  through canon, `gfs get` one big log.
* Measure and record in this spec: whether a comment and an approval bump
  `updated_on` (§6.4: if not, closed PRs miss comment-only changes until pull --full); `Content-Range` total while a step runs; whether a
  running step's log is readable (§9.4); merge `202` behaviour; the
  pipelines `target.commit.hash` filter; scoped-token scope names; whether
  retention-expired runs are still listed.
* Write, only on a scratch repository named in the test environment
  (never inferred): push a throwaway branch, create a PR, edit, comment,
  reply, inline comment, resolve, approve, request changes, decline; a
  second throwaway PR is merged with `squash`; a pipeline is triggered by
  the push and tailed. Every created branch and PR is cleaned up; failed
  cleanup is reported.

## 12. Docs

* `docs/bitbucket.md` (`gfs help bitbucket`): URL, credentials and scopes,
  layout, PR file, run file, logs, `gfs tail`, policy, rate-limit
  arithmetic.
* `start.md`: Bitbucket in the service list and typical session; `gfs tail`
  in the command table; exit codes of `tail`.
* `gfs schema bitbucket`; `gfs example bitbucket` with a PR and a run.
* `Readme.md`: Bitbucket in the service list.
