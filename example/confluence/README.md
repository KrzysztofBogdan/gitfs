# confluence: space ENG

Page tree as folders. A page is `Title.xml`; its children live in `Title/`.
Paths are meaningful, so `mv` is a move in the tree and renaming the file
renames the page. The body is Confluence storage format, untouched: `ac:`
macros, `ri:` references, layouts, inline comment markers all survive.

```shell
$ gfs clone confluence://instance/ENG confluence
$ tree confluence/eng
eng
├── Home.xml
└── Home
    ├── Architecture.xml
    ├── Runbooks.xml
    └── Runbooks
        └── Rollback.xml        (new, uncommitted)
```

## Update a page

Edit inside `<body>`. Confluence requires a version number on every update;
gfs sends the `version` attribute as the optimistic lock and bumps it on
write-back. Editing `version` yourself is a commit error.

```shell
$ vim eng/Home/Runbooks.xml
$ gfs commit
update  eng/Home/Runbooks.xml   v2 -> v3   ok
```

## Create and move

```shell
$ cat > eng/Home/Runbooks/Rollback.xml <<'XML'   # bare root, envelope added on commit
<page>
  <title>Rollback</title>
  <labels><label>runbook</label><label>release</label></labels>
  <body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">
    <ol><li>...</li></ol>
  </body>
</page>
XML
$ gfs status
  A  eng/Home/Runbooks/Rollback.xml     create page under "Runbooks"
$ gfs commit
create  eng/Home/Runbooks/Rollback.xml   ok   id=98871
```

## A conflict

`eng/Home/Architecture.xml` is frozen mid-conflict. Locally the worker
paragraph was rewritten and a label added; bob edited the same paragraph and
changed the labels on the server (v8). Commit fetched v8, tried a three-way
merge, and stopped:

```shell
$ gfs commit
  C  eng/Home/Architecture.xml   conflict with remote v8 by bob 2026-09-22T14:03, 1 element, 1 hunk
0 actions, 1 conflict
```

The envelope now has a `<conflict remote-version="8" by="bob" …/>` element and `<content>` has git-style markers with
the base section, once in `<labels>` and once in `<body>`. It is not
well-formed XML while conflicted, deliberately. Resolve by editing, remove the
`<conflict/>` element, commit again. Or pick a side:

```shell
$ gfs resolve --theirs eng/Home/Architecture.xml
$ gfs commit
update  eng/Home/Architecture.xml   v8 -> v9   ok
```

Commit re-checks the remote after resolution, so if bob edits again in the
meantime you get a fresh conflict, never a silent overwrite.

## Comments

Page comments are `<comment>` children after `<body>`. A `<comment>` without
`id` is new, same as Jira.
