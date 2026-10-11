# Moving Jira issues between projects

Status: design approved in conversation 2026-10-11. API behaviour confirmed
by a throwaway spike against kb-test-3 the same day (§3).

## 1. Purpose

Triage: an issue lands in the wrong project (GEN instead of SUP) and the
user moves it, one at a time, usually soon after it was created. Both
projects are normally in the tree.

The gesture is the git one: move the file to the other project folder and
commit. gfs works out the target type, status and field values, shows what
will not survive the move, and asks before losing anything.

### Behaviour before this change

- The Jira adapter is `Flat`: a file moved to another project folder is a
  no-op and `status` warns `rename ignored: name is derived`
  (`internal/changes/changes.go`).
- A move made in Jira already arrives on pull: the id is stable, the key
  changes, the file moves to the new folder.

### Non-goals

- Moving into a project outside the selection.
- Moving a sub-task on its own, or giving it a new parent.
- Bulk reorganisation (many issues, epics with their children).
- Setting the Request Type of an issue moved into a service project.
- Customer mode (`jira+customer://`): customers cannot move requests; it
  stays `Flat`.

## 2. What the user sees

- **Gesture.** `git mv "gen/GEN-760 Migrate auth.xml" sup/` (or any file
  manager), then `gfs commit`. The moved file may be edited in the same
  commit: its `<type>` and field values become the target values; an
  edited `<status>` is reached by a transition after the move (amended
  after the final review: a status set by the move skips resolution and
  post-functions). A `<transitions>` block on a moved file is refused. A rename inside the same folder is still a no-op with the same
  warning.
- **Status.** `R  gen/GEN-760 Migrate auth.xml -> sup/GEN-760 Migrate
  auth.xml`, action `move  … move to SUP`.
- **Dry run and the ask.** gfs resolves the move before deciding:
  `move … would run  move to SUP as Task, status Open`, followed by
  `⚠ type Story -> Task (SUP has no Story); status In Progress -> SUP's
  default; drops component billing, <field Story Points>`.
- **Policy.** A move that loses nothing is class `move` (default allow, as
  for Confluence page moves). A move that cannot keep the file's type or
  status, or drops any value, is class `delete` (default ask), like a
  customer participant change that may remove people. `move = ask` in
  `[policy]` asks for every move.
- **Required values.** A field the target requires, with no default and no
  value in the file, refuses the move: `SUP requires <field
  id="customfield_10050"> (Customer) for Task issues; add it to the file`.
- **After the move.** The file is written back as `sup/SUP-4100 Migrate
  auth.xml`, the issue as SUP now holds it; the attachment folder follows
  as for any rename. Comments, worklogs, links and attachments move with
  the issue in Jira.
- **Sub-tasks.** Jira moves them with their parent. Commit refreshes their
  files right after the parent's move (`refresh gen/GEN-761 … -> sup/SUP-4101
  …   ok  moved with gen/GEN-760 …`). A sub-task file with local changes is
  left alone with a warning; the next pull moves and merges it. Moving a
  sub-task file on its own is refused: `sub-tasks move with their parent:
  move GEN-760 instead`, unless the issue is already in the target project
  (its parent moved earlier in the same commit, or upstream), in which case
  the move is a no-op and the file's other changes apply.
- **Other edits in the same commit** run after the move, against the target
  project. If the move fails, they are held back and the file keeps
  `<errors>` at its new path; the next commit retries.
- **Notifications.** gfs sends `sendBulkNotification: false`: no bulk
  email. Jira's own "issue moved" event still reaches watchers.
- **Service projects.** A move into a service project warns `will not
  appear in the customer portal: Request Type is empty`.

## 3. Jira facts (spike 2026-10-11, kb-test-3)

- `POST /rest/api/3/bulk/issues/move` works with an API token. Body:

  ```json
  {"sendBulkNotification": false,
   "targetToSourcesMapping": {
     "<targetProjectId>,<targetTypeId>": {
       "inferClassificationDefaults": true,
       "inferFieldDefaults": true,
       "inferStatusDefaults": false,
       "inferSubtaskTypeDefault": true,
       "targetStatus": [{"statuses": {"<targetStatusId>": ["<sourceStatusId>"]}}],
       "issueIdsOrKeys": ["GEN-760"]}}}
  ```

  Answer `201 {"taskId":"80702"}`. `GET /rest/api/3/bulk/queue/{taskId}`
  reports `status` (`ENQUEUED`, `RUNNING`, `COMPLETE`, `FAILED`, `CANCELLED`,
  `DEAD`), `processedAccessibleIssues` (ids, the parent and its sub-tasks)
  and `failedAccessibleIssues` (id -> messages). Tasks finished in 1–2 s.
- Validation errors are synchronous `400 {"errors":[{"message":…}]}`:
  an unknown target status, a lone sub-task (`Parent issue should also be
  moved or target Parent issue should be specified`), missing mappings when
  inference is off.
- Sub-tasks move with the parent under `inferSubtaskTypeDefault`, keeping
  their status where the target workflow has it.
- `inferFieldDefaults` drops values the target cannot hold without warning:
  a component was dropped even between two projects with the same scheme.
  The changelog records the drop.
- `inferStatusDefaults` picks the target workflow's default (In Progress ->
  Open in GFSSD). An explicit `targetStatus` sets any status of the target
  workflow, with no transition.
- The id is stable; every old key redirects, also after several moves.
- A move into a service project works; Request Type stays null.

## 4. Engine

### 4.1 Resolving an action before the ask

`internal/adapter/adapter.go`:

```go
type Action struct {
	…
	// Preview: commit runs Check before deciding this action, and takes
	// the class, detail and warning Check resolved (a Jira move learns
	// what it would lose only from the target's metadata).
	Preview bool
}

type Result struct {
	…
	Class   string // Check: the action's policy class, when it differs from Describe's
	Warning string // Check: printed before the ask and in a dry run
	// Refresh: ids of other resources the action changed (the sub-tasks a
	// move took along); the engine re-reads them after the file is written.
	Refresh []string
}
```

`commitFile`: when any action has `Preview` and the file has local
content, it calls `Session.Check` once before the policy loop. For each
`Preview` action:

- a Check error fails the file without asking (same output as a failed
  Apply: `FAIL`, `<errors>` written into the file);
- otherwise a non-empty `Class`, `Detail` and `Warning` from the result
  replace the action's own, both for the ask and for what Apply receives.

Actions without `Preview` are unaffected; adapters that never set it see
no change.

`dryRunFile` already calls Check; it now also takes a non-empty
`Class` (for the `[ask]`/`[deny]` mark) and prints a non-empty `Warning`
like `Action.Warning`.

### 4.2 Refreshing resources an action changed

After `finish` has written the file, the engine walks the `Refresh` ids of
its successful results. For each id:

- not in the index: skip;
- part of this commit's change set and not committed yet: skip; its own
  change writes it back (a file committed earlier in the run is refreshed
  like any other);
- local file differs from its base (canonical content) or is missing:
  print `warning: <path>: changed on the remote by this commit; run gfs
  pull` and skip;
- otherwise `Fetch` and `Store(res, entry.Path)`, which writes the new path
  and removes the old one, and report `refresh  <old> -> <new>   ok  moved
  with <fc.Path>` (log line too). A fetch error is reported `FAIL` and
  counted, but does not stop the commit.

The change set's ids are kept on `Env` for the duration of `Commit`, as
`renamed` and `creates` are.

## 5. Jira adapter

### 5.1 Path model and description

- `Adapter.PathModel()` becomes `adapter.DirTree`. `changes` then emits
  `move{From, To}` for a file whose folder changed, and keeps the
  `rename ignored` note for a same-folder rename. `CustomerAdapter` stays
  `Flat`.
- `Describe` for `move`: `Class = "move"`, `Preview = true`,
  `Detail = "move to " + KEY` where KEY is the upper-cased top folder of
  `a.To`.

### 5.2 Planning a move (`move.go`)

`planMove(ctx, req) (*movePlan, error)` reads, writes nothing:

1. **Projects.** Source: `projectFor(req.Base.Path)`. Target:
   `projectFor(req.Local.Path)`; its error already names the selection.
2. **The issue.** `GET /issue/{id}?fields=project,issuetype,status,parent`.
   - Already in the target project: `noop` plan (detail `already in SUP`),
     class `move`, no warning.
   - A sub-task: `sub-tasks move with their parent: move <parent key>
     instead`.
3. **Type.** Name from the local `<type>`.
   - The target has a standard (non-sub-task) type of that name: use it.
   - Else, if the local `<type>` equals the base `<type>`: fall back to the
     target's `Task`, else its standard type with the lowest id; record
     `type Story -> Task (SUP has no Story)`.
   - Else (the user asked for a type the target lacks): error `SUP has no
     issue type "X" (types: …)`.
4. **Target fields.** `GET /issue/createmeta/{KEY}/issuetypes/{typeId}`,
   parsed with `allowedValues` and `hasDefaultValue` (not cached: values
   change, and a move is rare).
5. **Status.** `GET /project/{KEY}/statuses`, the chosen type's list.
   - The local `<status>` is in it: explicit mapping, current status id ->
     that status id; `inferStatusDefaults: false`.
   - Else, if the local `<status>` equals the base `<status>`: infer;
     record `status In Progress -> SUP's default`.
   - Else: error `SUP's Task workflow has no status "X" (statuses: …)`.
6. **Losses**, comparing the local file with the target fields:
   - each system element with a value whose field id is not on the target
     screen: `<due>` dropped (summary, type, status, reporter, creator,
     parent are exempt: the move sets or keeps them);
   - `<components>`, `<fixVersions>`, `<affectsVersions>`: each name not in
     `allowedValues` (by name, case-insensitive): `component billing`,
     `version 2026.10`;
   - `<priority>` not in `allowedValues`: `priority Lowest`;
   - each `<field id>` not on the target screen: `<field Story Points>`;
     each `<option>` of an option field not in `allowedValues` (by id, else
     value): `option 1-10 of <field Number of users>`.
7. **Required.** Each target field with `required && !hasDefaultValue`
   whose element is absent or empty in the local file, other than
   project, issuetype, summary and reporter: error naming the element.
8. **Adjusted file.** A copy of the local root with the chosen type name,
   the mapped status (left as is when inferred), and every lost element or
   option removed. Section 5.4 applies it after the move.

The plan's class is `delete` when step 3 or 5 recorded a change or step 6
found a loss, else `move`. Detail: `move to SUP as Task, status Open`
(`status: SUP's default` when inferred). Warning: the recorded type and
status changes, then `drops …` listing the losses, then the service-project
note (§2) when the target is a service project.

### 5.3 Check

`Check` for a file whose actions include `move`: `planMove`; its error goes
on the move action; its class, detail and warning on the move's result.
The file's other actions are not checked against Jira (they run after the
move, against the target); their results stay empty.

### 5.4 Apply

`Apply` with a `move` action:

1. `planMove`. An error fails every action of the file.
2. `noop` plan: skip to step 5.
3. `POST /rest/api/3/bulk/issues/move` with the plan's mapping,
   `inferFieldDefaults`, `inferClassificationDefaults` and
   `inferSubtaskTypeDefault` true, `sendBulkNotification` false.
4. Poll `GET /bulk/queue/{taskId}` every 500 ms (1 s after the first 5 s),
   reporting `Progress{Phase: "wait", Item: "moving GEN-760 to SUP"}`, up
   to 2 minutes.
   - `COMPLETE` with the issue in `processedAccessibleIssues`: go on; the
     other processed ids become the move result's `Refresh`.
   - `COMPLETE` with the issue in `failedAccessibleIssues`, or `FAILED`,
     `CANCELLED`, `DEAD`: fail every action with Jira's messages.
   - Timeout: fail every action with `move still running in Jira (task
     N); run gfs pull before committing again`.
5. Re-read the issue (`getIssue` + `resource`): the post-move remote, at
   its new path.
6. Finish the adjusted file: it takes the post-move `key` attribute, and
   an inferred status takes the post-move status. Compute its actions against the post-move remote with
   `changes.ResolveActions`, append the original attachment actions, and
   run them through the ordinary edit path (`issueCtx` on the new path,
   `planEdit`, `applyEdit`, `sub`, attachments). The no-move `Apply` body
   becomes `applyIssue(ctx, req)` for this.
7. Results for the original actions: the move succeeds (`Detail` = the
   new key). Every other original action takes the error of the
   recomputed action with the same verb, target, group and file, or
   succeeds when none is left: it was covered by the move (type, status)
   or dropped as a loss the user was shown.

The engine then writes back as for any change: `finish` fetches by id and
writes `remote.Path`, i.e. `sup/SUP-4100 Migrate auth.xml`.

## 6. jtest

- `Field` gains `Default bool`; `fieldJSON` emits `hasDefaultValue` and
  `allowedValues` from `Options` (`{"id", "value", "name"}`, value and
  name both the option's `Value`).
- `Issue` keeps its former keys; `getIssue` resolves them.
- `POST /rest/api/3/bulk/issues/move`: validates the mapping key, the
  target status (400 on an unknown id), a lone sub-task (400); moves the
  issue and its sub-tasks: new key from the target counter, new type,
  status from the mapping, else kept by name, else the workflow's first
  status; drops fields not on the target type's screen and options not in
  its `Options`. Answers `{"taskId"}`.
- `GET /rest/api/3/bulk/queue/{id}`: `COMPLETE` with
  `processedAccessibleIssues`. `Server.MoveRunning int` answers `RUNNING`
  that many times first; `Server.MoveFail string` makes the task `FAILED`
  with that message.

## 7. Tests

Adapter (`move_test.go`, jtest):

- lossless move between twin projects: class `move`, one bulk request
  with an explicit status mapping, file written at the new key;
- lossy move: class `delete`, warning lists type, status, component,
  field and option losses;
- edited `<type>` / `<status>` in the moved file used as the mapping; an
  unknown one is an error naming the choices;
- required field without value refuses; with a value in the file passes;
- sub-task file moved alone refused; already-moved issue is a no-op that
  still applies the file's other edits;
- move plus a new comment and a field edit: the comment and edit run after
  the move against the target;
- task `FAILED`, timeout (`MoveRunning` beyond the limit, with a short
  limit in the test), Jira 400: every action fails, nothing else sent;
- `Refresh` carries the sub-task ids;
- target outside the selection refused; service-project warning.

Engine (fake adapter):

- `Preview` runs Check before the ask; Check's class and warning are used;
  a Check error fails the file without asking; no Check without `Preview`;
- dry run uses Check's class and prints its warning;
- `Refresh`: unchanged file moved and base updated; locally changed file
  warned and kept; a file edited in the same commit is committed, then
  follows the move.

Changes: a Jira-like `DirTree` adapter emits `move` for a folder change and
the note for a same-folder rename (existing `IsMove` cases cover the rest).

Real site (`realsite_test.go`, `GFS_JIRA_SCRATCH` + a second scratch
project `GFS_JIRA_SCRATCH_MOVE`): create a Story with a sub-task and a
component, move it to the second project through the session, check new
keys, the sub-task id in `Refresh`, and the dropped component in the
warning; then move it to `GFS_JIRA_SCRATCH_JSM` and delete it there.

## 8. Docs

`docs/jira.md`: the Layout bullet on moving files describes the move; a
"Moving an issue" section (gesture, what is inferred, the warning, policy,
sub-tasks, notifications, service projects); a row in "What each change
does": `file moved to another project folder | move the issue (and its
sub-tasks), then the file's other changes | move, or delete when
something is lost`.
