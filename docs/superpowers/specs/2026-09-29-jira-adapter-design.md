# Jira adapter design

Status: draft for review. Date: 2026-09-29.
Adds a `jira` adapter (agent mode, `jira://`) and a customer mode
(`jira+customer://`) for Jira Cloud and Jira Service Management. Builds on
`2026-09-23-gfs-cli-design.md`, `2026-09-23-gfs-attachments-design.md`,
`2026-09-24-gfs-credentials-design.md` and follows the site-wide selection model
of `2026-09-29-confluence-site-clone-design.md`. Supersedes the file shapes in
`example/jira/abc`, which are updated to this spec.

Numbers quoted below were measured on a real site (about 36,000 issues,
26 projects, 116 fields of which 72 custom) and a real customer portal (33
service desks) on 2026-09-29.

## 1. Purpose

Mirror Jira as files, both ways: read and grep issues, and change them by
editing files (fields, status, comments, worklogs, links, attachments, new
issues). Also mirror the requests a user raised as a customer on someone
else's service desk, and let them reply.

### Goals

* `gfs clone jira://<site>` clones every project the account sees, narrowed
  by `filter`, `exclude`, `since`, `limit`.
* Lossless round trip of rich text (ADF).
* Status changes are workflow transitions, including transition screens.
* A pull with nothing new is one search request.
* Customer mode: `gfs clone jira+customer://<site>` mirrors your requests on
  a service desk portal; comment, transition, approve, add participants and
  attachments as a customer.
* Nothing that emails an external customer happens without an explicit
  policy grant.

### Non-goals

* `support.atlassian.com` (Atlassian's own portal). Measured: the portal
  reads requests through the GraphQL gateway (`/gateway/api/graphql`,
  `customerSupport.me.requests`), which refuses API tokens
  (`API_TOKEN is not allowed in service csp`, allowed: `SESSION`); the JSM
  site behind it (`atlassiansupport.atlassian.net`) is IP-allowlisted; and
  `getsupport.atlassian.com/rest/servicedeskapi` redirects to `/contact`.
  Access would need the browser session cookie and an undocumented schema;
  a possible later, separate adapter.
* Moving issues between projects or changing issue type (Jira "move").
* Multi-hop transitions (pathfinding through the workflow).
* Embedding new images in rich text (needs the Media API). Existing
  embeds round-trip.
* Watchers, votes, rank, development summary, SLA values, changelog,
  remote (web) links.
* Creating projects or service desks.
* A `jql=` URL parameter (possible later).
* Jira Server / Data Center.

## 2. Packages

| package | role |
|---------|------|
| `internal/adapter/atlassian` (new, extracted from confluence) | site and base URL, email and token resolution, HTTP client (`Do`, `APIError`, `Upload`, `Download`), 429/503 retry honouring `Retry-After` with capped exponential backoff, `VerifyToken` |
| `internal/adapter/confluence` | moves onto `atlassian`; behaviour unchanged except it gains retries |
| `internal/adapter/jira` (new) | both modes (files listed in §2.1) |
| `internal/adapter/jira/jtest` (new) | fake Jira and JSM server |
| `internal/adapter` | new optional interface `Advisor` (§8) |
| `internal/cli` | registers the adapter; `gfs actions <path>...` (§8); `pull --full` is shared with the Confluence site-clone spec |
| `internal/engine` | no change expected: flat path model, partial listings with `Deleted`, `PullOpts.Full` and the attachments engine exist |

### 2.1 Files in `internal/adapter/jira`

| file | knows about |
|------|-------------|
| `adapter.go` | registration, `Name`, `Schemes`, `PathModel` (`Flat`), `DefaultDir`, `Describe`, `Open` for both modes |
| `url.go` | URL forms, selection parameters, normalisation (§3) |
| `session.go` | agent session: open, `List`, `Fetch`, `Download` |
| `search.go` | JQL building, `search/jql` paging with `nextPageToken`, cursor codec |
| `fields.go` | field catalogue, createmeta cache, field set, value codecs (pure: JSON and a catalogue entry in, XML out, and back) |
| `adf.go` | ADF JSON ↔ XML (knows nothing about issues) |
| `issue.go` | issue JSON ↔ `<issue>` resource |
| `apply.go` | agent `Check` and `Apply` |
| `transition.go` | transition resolver shared by `Apply`, `Check` and `Available` |
| `people.go` | people cache, user resolution, `.people.xml` |
| `workflows.go` | `.workflows.xml` |
| `attachments.go` | attachment list, upload, download, delete |
| `customer.go` | customer session: `List`, `Fetch`, `Check`, `Apply`, `Available` |
| `schema.go`, `relaxng.go` | `<issue>`, `<request>`, `<people>`, `<workflows>` schemas; RELAX NG generated from the bundled ADF JSON schema |

`fields.go`, `adf.go` and `transition.go` are pure and tested without HTTP;
`apply.go` and `customer.go` reach the server only through
`atlassian.Client`.

## 3. Remote URL and selection

### 3.1 Agent mode

| input | normalised |
|-------|------------|
| `jira://[<email>@]<site>[?filter=…&exclude=…&since=…&limit=…]` | unchanged |
| `jira://[<email>@]<site>/<KEY>` | `jira://[<email>@]<site>?filter=<KEY>` |
| `jira:https://<site>/browse/<KEY>-<n>` | `jira://<site>?filter=<KEY>` |
| `jira:https://<site>/jira/{software,servicedesk,core}/projects/<KEY>[/…]` | `jira://<site>?filter=<KEY>` |
| `jira:https://<site>/projects/<KEY>[/…]` | `jira://<site>?filter=<KEY>` |
| `jira:https://<site>/jira[/…]` without a project | `jira://<site>` |

| parameter | meaning | default |
|-----------|---------|---------|
| `filter=K1,K2` | exactly these projects | every project the account sees |
| `exclude=K1,K2` | removed from the result, applied last | none |
| `since=<date>` | only issues with `updated` on or after the date: `YYYY-MM-DD`, or relative `-<n>d`, `-<n>w`, `-<n>m` (months) | none |
| `limit=<n>` | at most the n most recently updated issues per project; `5000` or `5k` | none |

* A path key together with `filter` is an error. Any other path is an error
  listing the accepted forms. `?base=<url>` overrides the API base (tests
  only).
* The selection is resolved with `GET /rest/api/3/project/search` (paged).
  A `filter` key that is missing or not visible fails the command and names
  it. Unknown `exclude` keys are ignored.
* `since` and `limit` are clone-time windows, re-applied by `pull --full`
  (§6.6). Incremental pull adds any issue updated after the cursor, so the
  tree grows past `limit`, and old issues that are touched join it.
* `DefaultDir`: the lower-cased key when `filter` names one project, else
  the site's first host label.

### 3.2 Customer mode

| input | normalised |
|-------|------------|
| `jira+customer://[<email>@]<site>[?desk=…&ownership=…&status=…]` | unchanged |
| `jira+customer:https://<site>/servicedesk/customer/user/requests[?…]` | `jira+customer://<site>` |
| `jira+customer:https://<site>/servicedesk/customer/portal/<id>[/…]` | `jira+customer://<site>?desk=<id>` |

| parameter | meaning | default |
|-----------|---------|---------|
| `desk=34,12` | only these service desks (portal ids) | every desk the account sees |
| `ownership=owned\|all` | `owned`: requests you reported; `all`: also those you participate in or that are shared with your organisation | `all` |
| `status=open\|all` | which requests to include | `all` |

* Desks are resolved with `GET /rest/servicedeskapi/servicedesk` (paged). An
  unknown `desk` id fails and names it.
* `DefaultDir`: the first host label (a desk's key is unknown before the
  API answers).

### 3.3 Credentials

Both modes use the `atlassian` credential realm (credentials spec): the
same `gfs auth set <email> --host <site>` token works for Confluence, Jira
and customer mode. The email is resolved as today (env, URL user,
`[remote] email`, site default, sole identity). Environment overrides:
`GFS_JIRA_EMAIL`, `GFS_JIRA_TOKEN`. `VerifyToken` for a Jira host calls
`GET /rest/api/3/myself`, falling back to
`GET /rest/servicedeskapi/info` for customer-only accounts.

## 4. Layout

```text
warsaw-dynamics/
├── .gfs/
│   └── jira/meta.json                   field catalogue and createmeta cache
├── .people.xml                          read-only
├── .workflows.xml                       read-only
├── gen/                                 project GEN, key lower-cased
│   ├── GEN-759 Q4 platform epic.xml
│   ├── GEN-760 Migrate auth.xml
│   └── GEN-760 Migrate auth.files/      attachments (gfs get)
└── sup/
    └── SUP-4017 License not activating.xml
```

Customer mode: `<desk project key, lower-cased>/<KEY> <summary>.xml`, plus
`.people.xml`; no `.workflows.xml`.

* Path model `Flat`: the file name is `<KEY> <summary>.xml`, derived, cut to
  200 bytes at a character boundary (file systems allow 255). Editing
  `<summary>` renames the file on commit and pull. A local rename is a no-op
  with a warning.
* Name sanitising and collision suffixes follow the shared rules (`/`, `\`
  → `-`, control characters → space, leading `.` → `_`, empty summary →
  `untitled`). Keys are unique, so collisions cannot occur in practice.
* Hierarchy is `<parent>`, never folders.
* A new file under a folder that is not a selected project (or desk) is
  refused: `no project "new" in this tree; gfs does not create projects`.
* Moving a file to another project folder does nothing: the path model is
  flat, so `status` warns `rename ignored: name is derived` (Jira's move needs
  a field mapping).
* `.people.xml` and `.workflows.xml` are resources the adapter lists (ids
  `people` and `workflows`, version = SHA-256 of the canonical content). Any
  local change is refused by `Check`.

## 5. File format

### 5.1 Issue (agent mode)

```xml
<issue id="103922" key="SUP-4057" created="2026-09-29T15:22:28.934+0200" updated="2026-09-29T15:23:31.800+0200" resolved="2026-09-30T10:00:00.000+0200">
  <summary>Promo code request</summary>
  <type>Support</type>
  <status>Waiting for support</status>
  <resolution>Done</resolution>
  <priority>Lowest</priority>
  <assignee account="712020:de26…">Adam Lipiński</assignee>
  <reporter account="qm:718e…">hadas@example.com</reporter>
  <creator account="qm:718e…">hadas@example.com</creator>
  <parent>SUP-4000</parent>
  <labels><label>renewal</label></labels>
  <components><component>billing</component></components>
  <fixVersions><version>2026.10</version></fixVersions>
  <affectsVersions><version>2026.09</version></affectsVersions>
  <due>2026-09-30</due>
  <timetracking spent="1d 2h"><original>2d</original><remaining>4h</remaining></timetracking>
  <field id="customfield_10016" name="Story Points">5</field>
  <field id="customfield_10040" name="Number of users"><option id="10022">1-10</option></field>
  <field id="customfield_10226" name="Marketplace License" type="adf"><paragraph>…</paragraph></field>
  <environment type="application/vnd.atlassian.adf+xml">…</environment>
  <description type="application/vnd.atlassian.adf+xml">…</description>
  <link id="10231" type="blocks">SUP-3990</link>
  <attachment id="10500" name="log.txt" size="1204" mime="text/plain" created="…" author="Adam Lipiński"/>
  <comment id="10044" account="712020:de26…" author="Adam Lipiński" created="…" updated="…" internal="true">…ADF…</comment>
  <worklog id="4412" account="…" author="…" created="…" updated="…">
    <started>2026-09-29T09:00:00.000+0200</started>
    <spent>2h</spent>
    <comment>…ADF…</comment>
  </worklog>
</issue>
```

* Child order is fixed as shown. `field` elements sort by `id`; `link` by
  `id` (new links last); `attachment`, `comment`, `worklog` by `created`
  (new last). Empty elements are omitted.
* Attributes are service-owned and a local change is refused, with two
  declared exceptions that the user writes on **new** elements only:
  `internal`/`public` on `<comment>` (§5.5) and `type` on `<link>` (§5.6).
  `type` on body elements is fixed by the schema.
* Read-only elements: `<type>`, `<creator>`, and any `<field type="raw">`.
* Keys (`<parent>`, links, epic-link fields) are issue keys in the file,
  resolved to ids on commit and written back as keys.
* Durations are Jira's own strings (`2h`, `1d 4h`), sent back as strings;
  `spent` is computed by Jira from worklogs.
* `resolved` is absent while the issue is unresolved.

### 5.2 Which fields appear

The **field set** of an issue is, for its (project, type):

`createmeta fields` ∪ {`environment`} − {`comment`, `attachment`, `issuelinks`, `worklog`, `project`}

plus a fixed set that is always requested (`status`, `resolution`,
`reporter`, `creator`, `created`, `updated`, `resolutiondate`,
`timetracking`) and the separately mapped `comment`, `worklog`,
`attachment`, `issuelinks`. Which of them are writable is decided per
issue by editmeta and transition screens (§7.3).

* createmeta is `GET /rest/api/3/issue/createmeta/{project}/issuetypes/{typeId}`,
  one request per (project, type), cached in `.gfs/cache/jira/meta.json`
  (the engine hands sessions `.gfs/cache` through `adapter.Cacher`). The
  cache is refreshed when pull meets a (project, type) not in it, and on
  `pull --full`.
* Measured: createmeta and a sample issue's editmeta differ by 1–3 fields;
  every field with a value outside this set was computed or noise (Rank,
  Development, `[CHART]` fields, SLA, request type, request language). They
  never enter files.
* A file shows the fields of its set that have a value. To set an empty
  field, add its element; the element name for a custom field is
  `<field id="…">` with any `name` (it is ignored on write and rewritten).

### 5.3 Value codecs

System fields map to the named elements in §5.1. Custom fields in the field
set, by `schema.custom`:

| custom type | file | write |
|-------------|------|-------|
| `textfield`, `url`, `gh-epic-label` | text | set |
| `textarea` | `type="adf"`, ADF children (§5.4) | set |
| `float`, `jsw-story-points` | number | set |
| `datepicker` / `datetime` | `YYYY-MM-DD` / ISO 8601 as Jira returns it | set |
| `select`, `radiobuttons`, `gh-epic-status` | `<option id="…">value</option>` | an `<option>` without `id` is matched by value against editmeta `allowedValues` |
| `multiselect`, `multicheckboxes` | several `<option>` | same |
| `userpicker`, `multiuserpicker`, `sd-request-participants` | one or more `<user account="…">name</user>` | §5.7 |
| `labels` | `<label>` children | set |
| `gh-epic-link` | issue key | set |
| `gh-sprint` | `<sprint id="42">Sprint 17</sprint>` | by `id` only |
| anything else | `type="raw"`, the JSON value in CDATA | read-only; a change is refused and names the field |

System field values: `priority`, `resolution`, `components`, versions and
`type` by name (resolved against editmeta `allowedValues` or the project's
lists on commit); `labels` as strings.

### 5.4 Rich text: ADF as XML

Body elements (`description`, `environment`, `textarea` fields, comment and
worklog comment bodies) have type `application/vnd.atlassian.adf+xml` and
contain the content of the ADF `doc` node. `doc` and `version` are implicit.

Rules, both directions, driven by the bundled ADF JSON schema:

1. A node is an element named `node.type`; its `content` are the child
   elements.
2. `attrs` are attributes. String values verbatim. Numbers and booleans in
   their JSON form, restored to their schema type on the way back. Object
   and array values as JSON text.
3. A `text` node is character data. Its marks become wrapper elements named
   by mark type, with mark `attrs` as attributes, outermost first in the
   schema's mark order: `<strong><link href="…">site</link></strong>`.
4. Marks on non-text nodes (block marks) wrap the node the same way:
   `<alignment align="center"><paragraph>…</paragraph></alignment>`. ADF
   mark and node type names do not overlap; decoding tells them apart by
   the schema.
5. Adjacent text nodes with identical marks are merged when decoding, so
   the remote splitting text differently never shows as a change.
6. A node type (or mark) not in the bundled schema becomes
   `<adf-raw>` with the node's JSON in CDATA. It round-trips unchanged; a
   change inside it is refused.
7. `hardBreak` is `<hardBreak/>`; whitespace in mixed content is kept as in
   the canonical printer rules.
8. `media` nodes keep their Media Services ids. Existing embeds round-trip;
   a new `media` node is refused.

A RELAX NG grammar for the body vocabulary is generated from the same
schema (`gfs schema jira`), as for Confluence.

### 5.5 Comments

* Attributes `id`, `account`, `author`, `created`, `updated` are
  service-owned. The body is ADF.
* In a JSM (service desk) project every comment carries `internal="true"`
  or `public="true"`. On a new comment the user must write one of them;
  neither gives the error
  `SUP is a service project: mark the comment internal="true" or public="true"`.
  Outside JSM projects both attributes are refused.
* Editing or deleting another user's comment fails with Jira's error.

### 5.6 Worklogs and links

* Worklog: `started`, `spent` and `comment` are elements (user data); a new
  worklog has no `id`. Attributes as for comments.
* Link: `<link type="<phrase>">KEY</link>`. `type` is a link type's outward
  or inward phrase (`blocks`, `is blocked by`), written by the user on new
  links. Links cannot be edited: delete the element and add a new one.

### 5.7 People

* A user is `<element account="<accountId>">Display Name</element>`. The
  accountId is the identity. Most accounts hide their email (measured: 5 of
  9 staff accounts), and display names are not unique.
* To change one, replace the element with one without `account` whose text
  is an email or a display name. Commit resolves it through
  `GET /rest/api/3/user/assignable/search` (assignee) or
  `GET /rest/api/3/user/search` (other pickers): exact email first, then a
  unique exact display name. Ambiguous or not found: an error listing the
  candidates with their accountIds.
* Removing `<assignee>` unassigns (canon drops empty elements, so
  `<assignee/>` is the same as none). Editing the text while keeping
  `account` is refused.
* Mentions inside ADF are native: `<mention id="<accountId>" text="@Name"/>`.

### 5.8 `.people.xml`

```xml
<people id="people">
  <person account="712020:de26…" type="atlassian" active="true">Adam Lipiński</person>
  <person account="qm:718e…" type="customer" active="true" email="hadas@example.com">hadas@example.com</person>
</people>
```

Every user seen in the tree (people fields, comment and worklog authors,
mentions). `email` only when Jira exposes it. Sorted by display name, then
account. Incremental pull only adds and updates people; `pull --full`
rebuilds the file.

### 5.9 `.workflows.xml`

```xml
<workflows id="workflows">
  <workflow name="Agile Workflow - Story">
    <scheme project="GEN" type="Task"/>
    <scheme project="GEN" type="Bug"/>
    <status name="To Do" category="new"/>
    <status name="In Progress" category="indeterminate"/>
    <status name="Done" category="done"/>
    <transition name="Start working" to="In Progress"/>
    <transition name="Resolve this issue" to="Closed"><from>In Progress</from><from>Waiting for support</from></transition>
  </workflow>
</workflows>
```

* With the Administer Jira permission: built from `GET /rest/api/3/workflow/search`
  (per workflow, with `expand=transitions,statuses`), `GET /rest/api/3/status`
  and `GET /rest/api/3/workflowscheme/project` for the selected projects. A `transition` without
  `from` is available from any status.
* Without it: from `GET /rest/api/3/project/{key}/statuses`, giving
  `scheme` and `status` elements only, and the file starts with
  `<note>transitions need the Administer Jira permission; run gfs actions with an issue file to see what you can do to it</note>`
  (an element: canon drops XML comments).
* Refreshed on clone, `pull --full`, and when pull meets a (project, type)
  not in the file.
* Informational only. Conditions and validators are user- and
  issue-dependent; commit and `gfs actions` ask Jira live (§7.3, §8).

### 5.10 Request (customer mode)

```xml
<request key="ECOHELP-164494" desk="34" type="4180" created="2026-09-12T20:39:43+0200" status-date="2026-09-20T20:30:08+0200">
  <summary>App listing rejected</summary>
  <requestType>Marketplace listing</requestType>
  <status category="done">Resolved</status>
  <reporter account="…">Krzysztof Bogdan</reporter>
  <participant account="…">Adam Lipiński</participant>
  <field id="customfield_19404" name="Partner / Vendor ID">1216382</field>
  <description type="text/x-jira-wiki">…</description>
  <approval id="12" name="Legal" status="pending"/>
  <attachment id="…" name="…" size="…" mime="…" created="…" author="…"/>
  <comment id="…" account="…" author="Sherica Ocbania" created="…">…</comment>
</request>
```

* Only fields the request type's portal form shows (`requestFieldValues`),
  with the §5.3 codecs where types match, else `type="raw"`.
* Bodies are the strings the customer API returns (`text/x-jira-wiki`).
  They are read-only: the customer API cannot edit descriptions or
  comments.
* `<status>` shows the status name; to move the request, write a customer
  transition name there (customer transitions report no target status), and
  write-back shows the status the desk chose.
* Writable: `<status>` (customer transitions), `<participant>` add and
  remove, `<approval decision="approve|decline">` on a pending approval
  (`decision` is the declared user-written attribute of this mode), new
  `<comment>` (plain text), new `<attachment>`.
* Every other element and every other attribute is read-only.

## 6. Pull (agent mode)

### 6.1 Full listing

Clone, `pull --full`, and a project newly in the selection:

```text
project = K [AND updated >= "<since>"] ORDER BY updated DESC
```

per project, through `GET /rest/api/3/search/jql` with the explicit field
list (§5.2) and `maxResults=100`, following `nextPageToken`, stopping at
`limit`. Every issue is returned in full (`Root` set) with `Full: true` for
that project.

### 6.2 Incremental listing

```text
project in (K1, K2, …) AND updated >= "-<N>m" ORDER BY updated ASC
```

* `N` is the minutes from the oldest per-project cursor to now, rounded up,
  plus 10 minutes of overlap. A relative date avoids the account time-zone
  problem of absolute JQL dates.
* Changed issues are returned in full, `Full: false`. The engine skips an
  entry whose version (`updated`) and path match the index.
* New comments, worklogs, attachments, links and transitions bump the
  issue's `updated`, so no side searches are needed. (Documented behaviour;
  checked by the real-site test, §11.4.)
* A pull with nothing new: one search request.

### 6.3 Sub-resource top-ups

Search returns at most 20 worklogs inline (measured: 20 of 276). When
`worklog.total` or `comment.total` exceeds what was returned, the adapter
fetches `GET /rest/api/3/issue/{id}/worklog` or `/comment` (paged) for that
issue.

### 6.4 Cursor

`Listing.Cursor` is an opaque string encoding `{projectKey: UTC time the
listing started}`. A project missing from the map gets a full listing
(§6.1). Any failed search, top-up or catalogue request fails the pull and
the cursor is not advanced.

### 6.5 Moves, deletions, selection changes

* Moved within the selection: the issue id is stable and the key changes;
  the listing entry has the same id at a new path, and pull renames the
  file.
* Deleted, or moved to a project that is not selected: not visible
  incrementally. Caught by `pull --full`, and by `Fetch` or commit getting
  404 or an issue whose project is not selected, which returns
  `adapter.ErrNotFound` (the engine treats it as deleted on the remote).
* A project leaving the selection (config edited, archived, hidden): the
  adapter puts the ids of that project's indexed files in
  `Listing.Deleted`. Locally modified files become conflicts under the
  existing rules.
* A project joining: full listing for that project (§6.4).

### 6.6 Windows and `--full`

A full listing returns only issues inside `since`/`limit`. Files outside
the window after `pull --full` are removed as deleted on the remote;
locally modified ones become conflicts. A relative `since` moves forward
with time.

### 6.7 `.people.xml` and `.workflows.xml`

Every listing includes the `people` and `workflows` resources when their
canonical content changed (version = content hash), so the engine updates
them like any remote change.

### 6.8 `Fetch`

`GET /rest/api/3/issue/{id}?fields=<field set>` plus top-ups.

## 7. Commit (agent mode)

### 7.1 Lock

Jira's edit endpoint takes no expected version. The engine already fetches
the issue just before `Apply` and compares it with the base: if `updated`
(or any content) moved, it merges, or refuses under `--no-merge`. The race
between that fetch and the write is accepted and documented.

### 7.2 Actions and order

One file's actions run in this order; each has its own result:

1. `create` (new file)
2. `update`, one action per changed element, sent together in one
   `PUT /rest/api/3/issue/{id}` (`fields` set)
3. `transition`
4. `comment` create, update, delete
5. `worklog` create, update, delete
6. `link` create, delete
7. `attachment` create, delete (attachments engine)

A failed create skips the rest of the file. A failed update or transition
does not skip later actions. Earlier successful actions are not undone.

### 7.3 Transitions

When `<status>` changes:

1. `GET /rest/api/3/issue/{id}/transitions?expand=transitions.fields`.
2. The new text is matched, case-insensitively, first against transition
   target status names, then against transition names.
   * One match: that transition.
   * Several transitions to the same status: an error listing their names;
     write the transition name instead. Write-back replaces it with the
     target status.
   * None: an error listing the statuses reachable now.
3. Changed fields are split by the chosen transition's screen:
   * on the screen: sent in the transition request (`fields`), even if
     also on the edit screen;
   * not on the screen: sent through the `PUT` before the transition;
   * required on the screen and unchanged: the file's current value is
     sent; absent from the file: error
     `transition "Resolve this issue" requires <resolution>; add it`.
4. `POST /rest/api/3/issue/{id}/transitions`.

When `<status>` does not change, every changed field goes through `PUT`. A
field not in the issue's editmeta (`GET /issue/{id}/editmeta`, fetched once
per edited issue) is refused; if it is on some transition screen the error
says so: `resolution can only be set together with a status change on this issue`.

No multi-hop transitions. The transition screen's comment box is not used;
new `<comment>` elements go through the comment API after the transition.

### 7.4 Create

* A new file with an `<issue>` root and no `key`, in a project folder.
* `<summary>` and `<type>` are required, plus every field createmeta marks
  required for (project, type); missing ones are listed in the error.
* `POST /rest/api/3/issue`. If `<status>` differs from the initial status,
  a transition from it follows (§7.3), then comments, worklogs, links and
  attachments.
* Write-back fetches the issue, fills in `id`, `key`, all attributes and
  canonical names, and renames the file to `<KEY> <summary>.xml`.

### 7.5 Delete

A removed file is `DELETE /rest/api/3/issue/{id}`. An issue with sub-tasks
is refused: `delete or re-parent its sub-tasks first`.

### 7.6 Sub-resources

| element | create | update | delete |
|---------|--------|--------|--------|
| `comment` | `POST /issue/{id}/comment`; in JSM projects `internal="true"` adds the property `sd.public.comment` = `{"internal": true}` | `PUT /issue/{id}/comment/{cid}` | `DELETE …/comment/{cid}` |
| `worklog` | `POST /issue/{id}/worklog?adjustEstimate=auto` | `PUT …/worklog/{wid}?adjustEstimate=auto` | `DELETE …/worklog/{wid}?adjustEstimate=auto` |
| `link` | `POST /issueLink`; the phrase selects the link type and direction; an unknown phrase lists the valid ones | refused | `DELETE /issueLink/{lid}` |
| `attachment` | `POST /issue/{id}/attachments` (multipart, `X-Atlassian-Token: no-check`) | never sent: the schema allows create and delete only, so `status` marks an edited file `!` ("update of attachments is not supported") | `DELETE /attachment/{aid}` |

Attachment kind operations are `create delete`; version is `-`.

### 7.7 Policy classes

| class | actions | default |
|-------|---------|---------|
| `create` | new issue | allow |
| `update` | field changes | allow |
| `transition` | status change | allow |
| `comment` | new or edited comment, internal (JSM) or any (non-JSM) | allow |
| `reply` | new public comment in a JSM project (emails the customer) | ask |
| `worklog` | new or edited worklog | allow |
| `link` | new link | allow |
| `delete` | deleting an issue, comment, worklog, link, attachment; removing a participant | ask |
| `approve` | approval decision (customer mode) | ask |

`ask` on a non-TTY is `deny`, as today.

### 7.8 Describe

`status` prints what each action is; `commit --dry-run` also asks Jira and
prints what `Check` resolved:

```text
$ gfs status
  M  sup/SUP-4017 License not activating.xml
        update priority
        transition to Closed
        add public reply (emails the customer)  [ask]
        log 2h
  A  gen/Rate limiter drops burst traffic.xml create Bug in GEN

$ gfs commit --dry-run
update  sup/SUP-4017 License not activating.xml   would run  transition In Progress -> Closed (Resolve this issue) with <resolution>
```

`Check` resolves people, options, keys, the transition and required fields,
so `--dry-run` reports those errors without writing. Its result `Detail`
(for a transition: `transition In Progress -> Closed (Resolve this issue)
with <resolution>`) replaces the action's own detail in dry-run output.

### 7.9 Errors

Jira's `errorMessages` and `errors` (field id → message) become the
action's error. Field ids are translated to elements:
`<field id="customfield_10040">: Option id 'x' is not valid`.

## 8. `gfs actions <path>...`

`gfs actions` without paths is unchanged (the adapter's explicit verbs and
policy classes). With paths it asks the adapter, per file, what the current
user can do to that resource now:

```go
// Advisor is implemented by sessions that can list what the user may do
// to one resource now.
type Advisor interface {
	Available(ctx context.Context, id string, local *Resource) (Advice, error)
}

type Advice struct {
	State string      // shown after the path, e.g. "status: In Progress"
	Items []Available
	Note  string      // e.g. what the local <status> resolves to
}

type Available struct {
	Verb, Name, To string // "transition", "Resolve this issue", "Closed"
	Fields         []AvailableField
}

type AvailableField struct {
	Element  string   // "resolution", `field[id=customfield_10040]`
	Required bool
	Allowed  []string // shown when short
}
```

```text
$ gfs actions "sup/SUP-4017 License not activating.xml"
sup/SUP-4017 License not activating.xml    status: In Progress
  transition  Waiting for customer   -> Waiting for customer
  transition  Resolve this issue     -> Closed   requires <resolution>: Done | Won't Do | Duplicate
                                                 optional <link>
```

* Agent mode: one `GET /issue/{id}/transitions?expand=transitions.fields`
  per file; the list is for the issue's remote status. A locally edited
  `<status>` adds a `Note` from the same resolver commit uses (§7.3).
* Customer mode: customer transitions and pending approvals (§9.3).
* A new file (no id) prints `not on the remote yet`. Adapters without
  `Advisor` print `no per-file actions for <adapter>`.

## 9. Customer mode

A separate session type in the same adapter, over
`/rest/servicedeskapi`. The Jira platform API is not available to customers
(measured: `GET /rest/api/3/issue/{key}` returns 404).

### 9.1 Pull

* Every pull lists all selected requests:
  `GET /rest/servicedeskapi/request?requestOwnership=…&requestStatus=…&serviceDeskId=…&expand=participant,status,action`
  (paged, 50 per page), one listing per selected desk.
* The list does not carry comments (measured: `expand=comment` returns
  none) and has no `updated` field. Per request, `GET /request/{key}/comment`
  (paged) and `/attachment` are fetched when any of these holds:
  * the request is not in the index (new);
  * its `currentStatus.statusDate` or status history length differs from
    the base;
  * its status category is not `done` (open requests always refresh);
  * the pull is `--full`.
* The cursor is unused; every listing is `Full: true`, so requests that
  disappear are deleted.
* Known limit: a new comment on a closed request with no status change is
  picked up by `pull --full` only.

### 9.2 Commit

| change | call | policy |
|--------|------|--------|
| new file in a desk folder | `POST /rest/servicedeskapi/request` with `serviceDeskId`, `requestTypeId`, `requestFieldValues`; fields validated against `GET /servicedesk/{id}/requesttype/{typeId}/field`; `<requestType>` by name | `create` |
| new `<comment>` (plain text) | `POST /request/{key}/comment` with `public: true` | `reply` (ask) |
| `<status>` change | `GET /request/{key}/transition`, match by target or name as §7.3, `POST /request/{key}/transition` | `transition` |
| `<participant>` added / removed | `POST` / `DELETE /request/{key}/participant` | `update` / `delete` |
| `decision` on a pending `<approval>` | `POST /request/{key}/approval/{id}` | `approve` (ask) |
| new `<attachment>` | `POST /servicedesk/{id}/attachTemporaryFile`, then `POST /request/{key}/attachment` (`public: true`) | `create` |
| anything else | refused: read-only for customers | |

Lock: as in agent mode, the engine's fetch just before `Apply` catches a
request that changed.

### 9.3 Available

Customer transitions from `GET /request/{key}/transition`, plus pending
approvals where the user is an approver.

## 10. Errors

* Bad URL, unknown `filter` key or `desk` id, invalid `since` or `limit`:
  fail before any issue request, naming the problem.
* Retries by `atlassian.Client`, as Confluence already did: 429 on any
  method after `Retry-After` (seconds or HTTP date); 502/503/504 and network
  errors on GET only; backoff from 1s with up to 25% jitter, at most a
  minute per wait, 6 attempts and 5 minutes of waiting in total. Waits are
  reported as `adapter.Progress{Phase: "wait"}`.
* 401: points at `gfs auth set <email> --host <site>`.
* A search Jira rejects (JQL syntax change): the error suggests
  `gfs pull --full`.
* Partial pull: no partial listing is returned; the cursor is not advanced.

## 11. Testing

### 11.1 Fake server (`jtest`)

* Projects: a company-managed software project, a team-managed project, a
  JSM project with customers.
* `GET /field`, createmeta, editmeta with `allowedValues` and operations.
* `search/jql`: a JQL subset (`project =`, `project in`, `updated >=` with
  absolute and relative dates, `AND`, `ORDER BY updated ASC|DESC`),
  `nextPageToken`, explicit `fields`, inline comments and worklogs capped at
  20 with `total`.
* Issue create, edit, delete; transitions with screens and required
  fields, conditions per user; comments including the `sd.public.comment`
  property; worklogs; links and link types; attachments.
* User search and assignable search; users with hidden emails.
* `workflows/search` and `project/{key}/statuses`, with a switch that
  removes the admin permission.
* Customer API: service desks, requests with ownership and status filters,
  comments, transitions, participants, approvals, temporary files.
* 429 injection; a per-endpoint request counter.

### 11.2 Unit tests

* `parseTarget` for both modes: every row of §3, the error forms.
* Selection, `since` and `limit` parsing, cursor codec.
* ADF: golden round trips for every node and mark in the bundled schema,
  block marks, merge of adjacent text, `adf-raw`, attribute typing,
  refused new `media`.
* Field codecs: every row of §5.3, both directions.
* People resolution: email, unique name, ambiguous, not found, hidden
  email.
* Transition resolver: by status, by name, ambiguous, unreachable, screen
  split, required-but-missing.

### 11.3 End-to-end (`internal/cli/jira_e2e_test.go`)

* Clone the whole site; with `filter`, `exclude`, `since`, `limit`; path
  and browser URL shorthands give the same tree as `?filter=`.
* A pull with no changes makes exactly one search request (counter).
* A remote comment, worklog (including the 21st), attachment, or
  transition is picked up. A move between selected projects renames the
  file. A remote delete is removed by `pull --full`. A project excluded in
  the config has its files removed.
* Commit: create with a non-initial status; an edit plus a transition that
  requires `resolution`; ambiguous transition resolved by name; internal
  comment; public reply asked on a TTY and denied on a non-TTY; worklog;
  link add and delete; attachment add and delete; bytes change refused;
  lock conflict merged; delete of an issue with sub-tasks refused;
  reassignment by email and by name.
* `gfs actions <path>` in both modes. `.workflows.xml` with and without
  admin. `.people.xml` refuses edits.
* Customer mode: clone, a new agent comment on an open request picked up,
  a reply (policy `reply`), a customer transition, participant add, an
  approval, create a request from a request type.

### 11.4 Real-site checks

Gated by environment variables, like `confluence/corpus_test.go`:

* Read: pull a few dozen issues, push them back unchanged, assert no diff
  (ADF and codec round trip).
* Write, in a scratch project: adding a comment and a worklog bumps
  `updated`; `sd.public.comment` internal comments stay internal; the
  comment inline limit in search.
* Customer: list and fetch on a portal where the account is a customer.

## 12. Docs

* `docs/jira.md`: both modes, URLs and selection, credentials, layout, file
  format, codecs, people, transitions, `gfs actions`, policy classes
  including `reply`, incremental pull and its limits, `--full`, the not
  supported list.
* `gfs help jira` (embedded, as for Confluence); `start.md` gains the Jira
  clone example and the `reply` policy class.
* `example/jira/abc` rewritten to this format.
* `internal/cli/docs_test.go` keeps passing.
