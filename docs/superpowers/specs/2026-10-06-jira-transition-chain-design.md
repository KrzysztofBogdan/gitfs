# Jira: several transitions in one commit

Status: design approved in conversation 2026-10-06.

## 1. Purpose

A workflow may not allow `To Do → Done` directly; the issue has to go
`To Do → In Progress → Done`. Today a `<status>` change runs exactly one
transition and refuses a status two steps away (docs/jira.md: "gfs never
runs a chain of transitions for you"). This change lets the file list the
steps, each with the field values its transition screen takes. gfs still
never chooses a path: it runs the steps written.

Scope: agent mode (`jira://`), existing and new issues. Customer portal
requests (`jira+customer://`) keep their single transition.

### Non-goals

- Finding a path through the workflow automatically.
- Step fields that are not on the step's transition screen (refused).
- Visiting the same status twice in one block.
- Checking steps after the first in a dry run.

## 2. File format

```xml
<issue key="GEN-7" …>
  <status>Done</status>
  <transitions>
    <transition to="In Progress"><assignee>dev@x.com</assignee></transition>
    <transition to="In Review"/>
    <transition to="Done" name="Resolve this issue"><resolution>Fixed</resolution></transition>
  </transitions>
  <assignee>owner@x.com</assignee>
</issue>
```

- `<transitions>` holds one or more `<transition>` steps, run in order.
- `to` (required) is the **target status name** of the step.
- `name` (optional) is the transition name, for when several transitions
  lead to `to`. The chosen transition must lead to `to`.
- A step's children are issue elements its transition screen accepts:
  system elements (`<resolution>`, `<assignee>`, `<priority>`, …) and
  `<field id=…>`. `<comment>` is not allowed in a step.
- `<status>` must equal the last step's `to` (case-insensitive).
- The rest of the file is the end state: body changes are sent after the
  last step.
- The block is a local instruction: Jira never returns it, so after a
  successful commit the written-back file has the reached `<status>` and no
  `<transitions>`.

Without a block, `<status>` works exactly as today (one transition, target
status or transition name, changed screen fields travel with it).

### Validation (before anything is sent)

| Problem | Message (on the `<status>` action) |
|---|---|
| `<status>` differs from the last `to` | `<status> Done does not match the last <transition to="In Review">` |
| a step without `to` | `<transition> needs to="<status>"` |
| a status twice among the steps | `"In Progress" appears twice in <transitions>; split the round trip over two commits` |
| `<comment>` in a step | `<comment> cannot go in a <transition>; add it to the issue` |
| a step element that is not an issue field | `<transition to="X">: <foo> is not an issue field` |
| a step value that does not resolve (person, option, …) | the encoder's message, prefixed `<transition to="X">: ` |
| `<transitions>` changed but `<status>` did not | `<transitions> needs a <status> change` (on the transitions action) |

## 3. Actions and policy

- Schema: `transitions` is a grouped element of `<issue>` (kind Field,
  like `timetracking`), so a change to it is an `update transitions`
  action.
- Describe: the `status` action with a block in the file is class
  `transition`, detail `transition via In Progress, In Review to Done`.
  The `transitions` action is class `update`, detail `steps of the
  transition`: it is never sent on its own and so asks nothing extra.
  One chain is one policy decision.
- Both actions get the same outcome; the `status` action's line carries
  the detail `To Do -> In Progress -> In Review -> Done`.

## 4. Running the steps

### 4.1 Resume

Start at the step after the one whose `to` equals the issue's current
status; if no step's `to` does, start at step 1. Steps are distinct
(§2), so at most one matches. On a first run normally none does; a block
whose step targets the status the issue already has simply treats that
step and those before it as done. After a stop at step k, the next commit
finds the issue at step k-1's status and resumes at step k. When the
issue is already at `<status>`, nothing runs.

### 4.2 Each step

1. Fetch the transitions available now
   (`GET …/transitions?expand=transitions.fields`).
2. Choose: with `name`, the transition of that name, which must lead to
   `to` (`transition "Start" leads to "In Progress", not "Done"`);
   without, the one transition to `to` (today's `chooseTransition`
   messages).
3. Step fields: each must be on the transition's screen, else
   `<assignee> is not on the screen of transition "Start"`.
4. Required screen fields the step does not set: from the file body
   (today's `currentValue`), else
   `transition "Resolve this issue" requires <resolution>; add it`.
5. `POST …/transitions` with the step's fields.

### 4.3 Body changes

With a block, changed body fields never travel with a transition. They
are one PUT after the last step and must be on the edit screen; a
`<resolution>` changed only in the body is refused with
`<resolution> is not on the edit screen; put it in the <transition> that sets it`.
If the chain stops, the PUT is not sent and each body action fails with
`not sent: the transitions stopped at "In Review"`, so it stays local.

Without a block: unchanged (PUT first, then the transition).

### 4.4 Failure

A failure at step k (or in its planning) fails the `status` and
`transitions` actions with
`stopped at "In Review" (step 2 of 3, to "Done"): <cause>`. When at least
one step ran, the result is `Partial`: the engine re-reads the issue, the
base takes the reached status, and the local `<status>` and block are
put back with `<errors>` (existing `keepLocal`). The next commit resumes
(§4.1).

A failure while planning step 1 sends nothing.

### 4.5 New issues

A new file may carry a block. gfs creates the issue (body fields on the
create screen go with the create), reads its initial status, then runs
the steps from there (§4.1, §4.2). Step fields follow the same screen
rule. A failure is an extra result on the `status` and the `transitions`
groups, as today's failed `createStatus`; write-back gives the file its
key and the reached status, and keeps the local `<status>` and block, so
the next commit resumes. The chain is part of the `create` action's
policy, as today's single transition is.

## 5. Dry run

- Existing issue: all of §2's validation; every step's fields are
  encoded (people, options); step `start` (§4.1) is planned fully against
  the live transitions (§4.2 steps 1–4). Detail:
  `transition To Do -> In Progress -> Done (step 1 checked; the rest when run)`,
  or today's detail when one step remains.
- New issue: §2's validation and step field encoding; transitions are
  checked when run.

## 6. Docs

`docs/jira.md` "Status and transitions": replace "gfs never runs a chain
of transitions for you" with the block, its rules (§2), resume and
failure (§4.1, §4.4), and an example.

## 7. Testing

- Unit (`transition_test.go`, `supportFlow`: Open → In Progress
  (Start) → Closed (Resolve this issue, requires resolution / Close as
  duplicate)):
  - parsing and validation: each row of §2's table;
  - resume index;
  - chain Open → In Progress → Closed with `name="Resolve this issue"`
    and `<resolution>` in the last step plus a body `<priority>` change:
    two transition POSTs, PUT after them, detail
    `Open -> In Progress -> Closed`;
  - stop: last step without resolution (body has none) → issue at
    In Progress, `Partial`, `stopped at "In Progress" (step 2 of 2, to "Closed"): …`,
    body priority `not sent: …`; then a retry from In Progress runs only
    step 2;
  - off-screen step field refused before anything is sent;
  - `name` leading elsewhere refused;
  - create with a block: created, then two transitions.
- e2e (`jira_e2e_test.go`, a three-status workflow without
  `To Do → Done`): dry run detail; commit; file written back without the
  block; a stop leaves the block and `<errors>` in the file; fixing the
  step and committing resumes and ends clean.
