# gfs and Bitbucket Cloud

gfs mirrors the pull requests and pipeline runs of a Bitbucket Cloud
workspace, one XML file each. Pull requests are changed by editing their
files: title, description, reviewers, comments, your review, merge,
decline, new PRs. Pipeline runs are read-only; `gfs tail` streams a running
one. General `gfs` usage: [start.md](../start.md).

```shell
gfs auth login bitbucket://bitbucket.org/acme     # once: a scoped API token
gfs clone bitbucket://bitbucket.org/acme          # every repository -> acme/
gfs clone bitbucket://bitbucket.org/acme/api      # one repository -> api/
gfs tail "api/pr/142 Add rate limiting.xml"       # watch the PR's pipeline
```

## Remote URL

```text
bitbucket://[<email>@]bitbucket.org/<workspace>[?filter=r1,r2&exclude=r3&since=<date>&limit=<n>&pipelineLimitSize=<size>]
bitbucket://[<email>@]bitbucket.org/<workspace>/<repo>       one repository
bitbucket:https://bitbucket.org/<workspace>/<repo>/…         a pasted browser URL: its repository
```

| parameter | meaning | default |
|-----------|---------|---------|
| `filter` | exactly these repository slugs | every repository you can read |
| `exclude` | removed from the selection | none |
| `since` | PRs updated / runs created on or after: `2026-01-01`, `-90d`, `-2w`, `-6m` | none |
| `limit` | at most n newest PRs and n newest runs per repository: `500`, `5k` | none |
| `pipelineLimitSize` | step logs up to this size are inline: `0`, `512`, `1kb`, `64kb`, `1mb` | `1kb` |

`since` and `limit` apply when a repository is listed in full (clone,
`pull --full`, a repository new to the selection). Default folder: the
repository for one repository, else the workspace.

### Clone time

Bitbucket allows a user token 1,000 requests an hour. A clone costs about
2 requests per pull request and 2 plus one per step for each pipeline run,
so a repository with 2,000 PRs and 5,000 runs needs about a day. gfs waits
when Bitbucket says to (the progress bar shows the wait). An interrupted
clone keeps what it fetched: `gfs pull` in the folder finishes it. For large
workspaces, start with `since=-90d` or `limit=500`.

## Credentials

Bitbucket takes an Atlassian API token **with Bitbucket scopes**, not the
token Jira and Confluence use. `gfs auth login bitbucket://bitbucket.org/<ws>`
says where to make one, which scopes to tick (`read:user:bitbucket`,
`read:workspace:bitbucket`, `read:repository:bitbucket`,
`read:pullrequest:bitbucket`, `write:pullrequest:bitbucket`,
`read:pipeline:bitbucket`), checks it and stores it in the keyring as
`bitbucket:<email>`, apart from your Atlassian token. `gfs auth rm
bitbucket://<email>` deletes it. In CI: `GFS_BITBUCKET_EMAIL` and
`GFS_BITBUCKET_TOKEN`.

## Layout

```text
acme/
├── .gfs/cache/bitbucket/          workspace members
├── api/                           repository api
│   ├── pr/
│   │   └── 142 Add rate limiting.xml
│   └── pipelines/
│       ├── 1873.xml
│       └── 1873.files/            big step logs, fetched with gfs get
│           └── 1-build.log
└── web/
```

* PR file name: `<id> <title>.xml`. Editing `<title>` renames the file on
  commit; renaming the file yourself does nothing.
* A new file in `<repo>/pr/` opens a PR there. New files anywhere else are
  refused.

## Pull request file

```xml
<pullrequest id="api/pr/142" number="142" created="…" updated="…" version="…">
  <title>Add rate limiting</title>
  <state>open</state>
  <draft>false</draft>
  <source branch="feat/rate-limit" commit="a1b2c3d4e5f6"/>
  <destination branch="main" commit="0f9e8d7c6b5a"/>
  <close-source-branch>true</close-source-branch>
  <author account="712020:aa…">Ana Nowak</author>
  <reviewer account="712020:bb…" state="approved">Bo Smith</reviewer>
  <participant account="712020:dd…" state="approved">Dee Park</participant>
  <my-review>none</my-review>
  <description>Markdown, as Bitbucket stores it.</description>
  <build key="…" state="FAILED" url="…" run="../pipelines/1873.xml">Pipeline #1873</build>
  <comment id="9012" account="…" author="Bo Smith" created="…" updated="…" resolved="false">General remark</comment>
  <comment id="9013" … path="src/limit.go" line="42" side="new" resolved="false">Off by one?</comment>
  <comment id="9020" … parent="9013">Fixed.</comment>
</pullrequest>
```

* `author`, `participant`, `build`, `merge-commit` and the root attributes
  are Bitbucket's; editing them is refused.
* People are `account` (the identity) plus display name: Bitbucket hides
  emails. Add a reviewer as `<reviewer>Bo Smith</reviewer>` (display name,
  nickname or account_id); commit resolves it against the workspace members.
  Remove one by deleting its element.
* `<my-review>`: your own review, `none`, `approved` or `changes_requested`.
* Comments are flat: a reply carries `parent`. A new comment has no `id`; an
  inline one has `path` and `line` (`side="old"` for the left side of the
  diff). A reply must answer a comment that already exists. Flip `resolved`
  to resolve or reopen a thread. You can edit and delete only your own.
* `<build run="…">` points at the run file when the status is a pipeline.

## What each change does

| change | sent | policy class |
|---|---|---|
| new file in `<repo>/pr/` | create the PR (then its comments) | `create` |
| title, description, destination, reviewers, close-source-branch, draft | one update of the PR | `update` |
| new comment, reply, inline comment; edit; resolve or reopen | comment API | `comment` |
| comment removed | delete it | `delete` (ask) |
| `<my-review>` changed | approve, request changes, or withdraw | `review` |
| `<state>merged</state>` | merge, last | `merge` (ask) |
| `<state>declined</state>` | decline, last | `decline` (ask) |
| file removed | refused: decline instead | |

* Merge: `<state strategy="squash" message="…">merged</state>`; the
  strategy must be one the destination branch allows (`gfs actions <file>`
  lists them, default marked); without it Bitbucket uses the default.
  `close-source-branch` goes with the merge. A merge Bitbucket finishes in
  the background is followed for up to 5 minutes.
* A merge never runs after another action of the same commit failed.
* A merged or declined PR cannot be reopened.

## Pipeline run file (read-only)

```xml
<pipeline id="api/pipelines/0b1c…" number="1873" created="…" completed="…" version="completed:failed:…">
  <state>completed</state>
  <result>failed</result>
  <trigger type="push" account="…">Ana Nowak</trigger>
  <target type="pullrequest" ref="feat/rate-limit" commit="a1b2c3d4e5f6" pipeline="pull-requests:**" pr="142"/>
  <duration build-seconds="402">451</duration>
  <step id="…" n="1" name="Build" image="golang:1.25" state="completed" result="successful" duration="122" log-size="48213"/>
  <step id="…" n="2" name="Lint" state="completed" result="failed" duration="31" log-size="612">
    <log>golangci-lint run
src/limit.go:42: …</log>
  </step>
  <steplog step="…" name="1-build.log" size="48213"/>
</pipeline>
```

* `state`: `pending`, `in_progress`, `paused`, `halted`, `completed`;
  `result` once completed: `successful`, `failed`, `error`, `stopped`,
  `expired`. `target@pipeline` is the selector that ran (`default`,
  `branches:<p>`, `tags:<p>`, `custom:<name>`, `pull-requests:<p>`).
* A log up to `pipelineLimitSize` is inline (line endings as `\n`, colour
  codes removed). A bigger one is a `<steplog>`: `gfs get <run file>` writes
  its raw bytes to `<number>.files/`.
* Any edit to a run file is refused at commit.
* A completed run never changes, so pull fetches it once; runs still going
  are fetched again on every pull.

## gfs tail

```shell
gfs tail api/pipelines/1873.xml                 # that run
gfs tail "api/pr/142 Add rate limiting.xml"     # newest run on the PR's source commit
```

Prints every step's log from the start, with a header when a step starts and
a footer when it ends; while steps run in parallel, each line starts with
`[<step>]`. A finished run prints its logs and exits, so `gfs tail` also
reads a log. Output on stdout is the same for a terminal and a pipe.

| flag | meaning | default |
|------|---------|---------|
| `--wait <d>` | PR file: how long to wait for a run to start | `2m` |
| `--interval <d>` | log poll interval (steps every 3rd) | `5s` |
| `--timeout <d>` | give up after this long | none |

Exit code: 0 successful, 1 failed / error / stopped / expired, 2 when gfs
could not watch (or `--wait`/`--timeout` ran out), 130 on Ctrl-C (the
pipeline keeps running). On exit the run's file is written back (and the
PR's, merging any local edits). Polling slows down after a quiet minute, up
to 30 s, and stays at 30 s once Bitbucket says the hourly limit is near.

## Pull

* Every pull lists each repository's open PRs and fetches them again
  (their builds and comments can change without the PR's update time), plus
  any PR updated since the last pull, plus new and unfinished runs.
* A repository leaving the selection (edit `url` in `.gfs/config`) has its
  files removed on the next pull; one joining is listed in full.
* `gfs pull --full` lists everything again.
