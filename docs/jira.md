# gfs and Jira Cloud

`gfs` mirrors Jira Cloud two ways. Agent mode (`jira://`) holds every project
you can see, one XML file per issue. Customer mode (`jira+customer://`) holds
the requests you raised on someone else's service desk. General `gfs` usage:
[start.md](../start.md).

## Remote URL

```text
jira://[<email>@]<site>[?filter=K1,K2&exclude=K3&since=<date>&limit=<n>]
jira://[<email>@]<site>/<KEY>                   one project
jira:https://<site>/browse/GEN-123              a pasted browser URL: its project
jira+customer://[<email>@]<site>[?desk=34,12&ownership=owned|all&status=open|all]
jira+customer:https://<site>/servicedesk/customer/portal/34
```

| parameter | meaning | default |
|-----------|---------|---------|
| `filter` | exactly these projects | every project you see |
| `exclude` | removed from the selection | none |
| `since` | only issues updated on or after: `2023-01-01`, `-90d`, `-2w`, `-6m` | none |
| `limit` | at most the n most recently updated issues per project: `5000`, `5k` | none |
| `desk` | customer mode: these service desks (portal ids) | every desk you see |
| `ownership` | customer mode: `owned` (raised by you) or `all` (also shared with you) | `all` |
| `status` | customer mode: `open` or `all` | `all` |

* Browser URLs that name a project: `/browse/KEY-1`, `/browse/KEY`,
  `/projects/KEY/…`, `/jira/{software,servicedesk,core}/[c/]projects/KEY/…`.
  Any other `/jira/…` page means the whole site.
* `since` and `limit` apply when a project is listed in full (clone, a
  project new to the selection, `pull --full`). An issue updated later joins
  the tree on the next pull, so the tree grows past `limit`.
* Changing the selection: edit `url` in `.gfs/config`. A project that left
  the selection has its files removed on the next pull; one that joined is
  listed in full.
* Default folder: the project key lower-cased for one project, else the
  site's first host label.

`support.atlassian.com` is not supported: its portal only accepts browser
sessions, not API tokens.

## Credentials

One Atlassian API token works for Jira, Confluence and customer portals:

```shell
gfs auth set me@example.com --host acme.atlassian.net
```

The email and token are resolved as for Confluence (see `gfs help start`);
`GFS_JIRA_EMAIL` and `GFS_JIRA_TOKEN` override them.

## Layout

```text
acme/
├── .gfs/cache/jira/           metadata, people and workflows kept between runs
├── .people.xml                everyone seen in the tree (read-only)
├── .workflows.xml             statuses and transitions (read-only)
├── gen/                       project GEN
│   ├── GEN-759 Q4 platform epic.xml
│   ├── GEN-760 Migrate auth.xml
│   └── GEN-760 Migrate auth.files/     attachments, fetched with gfs get
└── sup/
    └── SUP-4017 License not activating.xml
```

* The file name is `<KEY> <summary>.xml`. Editing `<summary>` renames the file
  on commit; renaming the file yourself does nothing.
* Hierarchy is `<parent>`, never folders. Sub-tasks sit next to their parent.
* A new file in a project folder creates an issue there. A folder that is not
  a selected project is refused. Moving a file to another project folder does
  nothing (`status` warns "rename ignored"): Jira's move needs a field mapping.
* An issue moved to another project upstream moves to that folder on pull,
  with its new key.

## Issue file

```xml
<issue id="103922" key="SUP-4057" created="…" updated="…" resolved="…">
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
  <description type="application/vnd.atlassian.adf+xml"><paragraph>…</paragraph></description>
  <link id="10231" type="blocks">SUP-3990</link>
  <attachment id="10500" name="log.txt" size="1204" mime="text/plain" created="…" author="Adam Lipiński"/>
  <comment id="10044" account="…" author="Adam Lipiński" created="…" updated="…" internal="true"><paragraph>…</paragraph></comment>
  <worklog id="4412" account="…" author="…" created="…" updated="…">
    <started>2026-09-29T09:00:00.000+0200</started>
    <spent>2h</spent>
    <comment type="application/vnd.atlassian.adf+xml"><paragraph>…</paragraph></comment>
  </worklog>
</issue>
```

* Attributes belong to Jira and are read-only, with two exceptions you write
  on new elements only: `internal`/`public` on a comment, `type` on a link.
* Read-only elements: `<type>`, `<creator>`, and any `<field type="raw">`.
* A file shows the fields of the issue type's create screen that have a
  value, plus `status`, `resolution`, `reporter`, `creator`, `timetracking`.
  Computed noise (Rank, Development, SLA, `[CHART]` fields) never appears.
  To set an empty field, add its element: `<field id="customfield_10016">3</field>`
  (the `name` is optional and rewritten).
* To clear a field, remove its element (an empty element is the same as none).

### Custom fields

| custom type | file | writing |
|-------------|------|---------|
| text, URL, epic name | text | as written |
| paragraph (textarea) | `type="adf"` with rich text | as written |
| number, story points | number | as written |
| date / date-time | `2026-09-30` / as Jira writes it | as written |
| select, radio, epic status | `<option id="…">value</option>` | `<option>value</option>` picks by value |
| multi-select, checkboxes | several `<option>` | same |
| user picker(s), request participants | `<user account="…">name</user>` | see People |
| labels | `<label>` | as written |
| epic link | an issue key | as written |
| sprint | `<sprint id="42">Sprint 17</sprint>` | by `id` only; the last `<sprint>` wins |
| anything else (teams, app fields, cascading selects) | `type="raw"`, JSON | read-only |

### Rich text (ADF)

Descriptions, environments, paragraph fields, comments and worklog comments
are Atlassian Document Format written as XML, one element per node:

```xml
<paragraph>Bursts above <strong>200 rps</strong> are dropped; see <link href="https://…">the runbook</link>.</paragraph>
<heading level="2">Steps</heading>
<orderedList order="1"><listItem><paragraph>Run the load test</paragraph></listItem></orderedList>
<codeBlock language="shell"><![CDATA[hey -z 10s -q 300 http://localhost:8080/api]]></codeBlock>
<panel panelType="warning"><paragraph>Prod only.</paragraph></panel>
<paragraph><mention id="712020:de26…" text="@Adam Lipiński"/><text> </text></paragraph>
```

* Marks (`strong`, `em`, `underline`, `strike`, `code`, `link`, `textColor`,
  …) wrap the text they apply to; block marks (`alignment`, `indentation`,
  `breakout`) wrap the block.
* `<text>` holds text explicitly; gfs writes it for a space next to an inline
  node, where it would otherwise be lost.
* A node gfs does not know is kept as `<adf-raw>` with its JSON; it round-trips
  unchanged and cannot be edited.
* Existing images (`media`) round-trip; adding a new one is not supported.
* Text directly in a body, outside a block, is refused: wrap it in
  `<paragraph>`.
* `gfs schema jira` prints a RELAX NG grammar for `xmllint --relaxng`.

## People

A person is `<element account="<accountId>">Display Name</element>`. The
accountId is what counts: most accounts hide their email, and display names
are not unique.

* To reassign, replace the element with one without `account`, holding an
  email or an exact display name: `<assignee>bea@example.com</assignee>`.
  Commit resolves it; a name shared by several accounts is refused with the
  candidates and their accountIds.
* Or copy an account from `.people.xml`: `<assignee account="712020:…">Bea</assignee>`.
* Editing only the text of an element that keeps its `account` is refused.
* Remove `<assignee>` to unassign.
* Mentions are ADF: `<mention id="<accountId>" text="@Name"/>`.

`.people.xml` lists everyone seen in the tree (fields, authors, mentions),
with their email when Jira shows it. `pull --full` rebuilds it.

## Status and transitions

Changing `<status>` runs one workflow transition.

* The text is matched against the target status of the transitions available
  to you now, then against transition names. When two transitions lead to the
  same status, write the transition name: `<status>Resolve this issue</status>`;
  write-back shows the status it led to.
* Fields on the transition's screen that you changed go with the transition
  (`<resolution>`, for example). A required screen field you did not change is
  sent from the file; if the file lacks it, the commit says
  `transition "Resolve this issue" requires <resolution>; add it`.
* A status two steps away is refused with the statuses reachable now; gfs
  never runs a chain of transitions for you.
* `gfs actions <file>` lists the transitions you can take on that issue now,
  their screen fields and allowed values, and what the file's `<status>`
  would resolve to. `.workflows.xml` shows the whole workflows when you have
  the Administer Jira permission, and only statuses otherwise.

## What each change does

| change | Jira | policy class |
|--------|------|--------------|
| new file in a project folder | create the issue, then its status, comments, worklogs, links, attachments | `create` |
| field edited, added or removed | one edit of all changed fields | `update` |
| `<status>` changed | transition (with its screen fields) | `transition` |
| new `<comment>` | add comment | `comment` |
| new `<comment public="true">` in a service project | add a public reply: **emails the customer** | `reply` (ask) |
| comment edited | edit (your own comments only) | `comment` |
| new `<worklog>` (`started`, `spent`, optional `comment`) | log work, adjusting the estimate | `worklog` |
| new `<link type="blocks">KEY</link>` | link issues; `type` is a link phrase such as `blocks`, `is blocked by` | `link` |
| file added in `<KEY> <summary>.files/` | upload attachment | `create` |
| comment, worklog, link, attachment removed | delete it | `delete` (ask) |
| file removed | delete the issue (refused while it has sub-tasks) | `delete` (ask) |

* In a service project every new comment needs `internal="true"` or
  `public="true"`; elsewhere neither is allowed. The visibility of an
  existing comment cannot be changed.
* Links cannot be edited: remove the `<link>` and add a new one.
* Jira attachments have no versions: an edited attachment file is refused;
  delete it and add it again.
* `gfs commit --dry-run` checks people, options, transitions, required fields,
  visibility and link types against Jira without changing anything.

## Pull

* A pull asks Jira for issues updated since the last pull (with 10 minutes of
  overlap), one search for all projects. A pull with nothing new is one
  request.
* New comments, worklogs, attachments, links and transitions all update the
  issue, so they arrive this way.
* Deleted issues, and issues moved to a project outside the tree, are not seen
  by an incremental pull; `gfs pull --full` removes them. A commit to such an
  issue fails with "deleted on remote; run gfs pull".
* `pull --full` lists every project again within `since`/`limit`; issues that
  fell outside the window are removed like deleted ones.
* Rate limits (HTTP 429) and brief outages are retried, honouring
  `Retry-After`; the wait is shown.

## Customer mode

`jira+customer://` uses only the service desk customer API: what the portal
shows a customer.

```text
ecosystem/
├── .people.xml
└── ecohelp/                                   service desk ECOHELP
    └── ECOHELP-164494 App listing rejected.xml
```

```xml
<request id="164494" key="ECOHELP-164494" desk="34" type="4180" created="…" status-date="…">
  <summary>App listing rejected</summary>
  <requestType>Marketplace listing</requestType>
  <status category="done">Resolved</status>
  <reporter account="…">Me</reporter>
  <participant account="…">Adam Lipiński</participant>
  <field id="customfield_19404" name="Partner / Vendor ID">1216382</field>
  <description type="text/x-jira-wiki">…</description>
  <approval id="12" name="Legal" status="pending"/>
  <attachment id="…" name="shot.png" size="…" mime="image/png" created="…" author="Me"/>
  <comment id="…" account="…" author="Sherica" created="…">Please fix the icon.</comment>
</request>
```

| change | effect | policy class |
|--------|--------|--------------|
| new `<comment>` (plain text) | public reply: emails the service desk | `reply` (ask) |
| `<status>` set to a transition name | customer transition (`gfs actions <file>` lists them) | `transition` |
| `<participant account="…">` added or removed | participants (accounts only: customers cannot look people up; copy them from `.people.xml`) | `update` |
| `decision="approve"` or `"decline"` on a pending `<approval>` | answer the approval | `approve` (ask) |
| file added in the `.files/` folder | attach publicly | `create` |
| new file in a desk folder with `<summary>` and `<requestType>` | raise a request (its form's required fields must be there) | `create` |

Everything else is read-only for customers. Pull refetches open requests
and requests whose status changed; a new comment on a closed request with no
status change arrives with `gfs pull --full`.
