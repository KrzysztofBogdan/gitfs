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
├── Home.files                  (attachments of Home, fetched on demand)
│   ├── logo.svg                (conflict: yours)
│   └── logo.remote-v4.svg      (conflict: remote v4)
└── Home
    ├── Architecture.xml
    ├── Runbooks.xml
    ├── Runbooks.files
    │   ├── rollback-flow.png   (fetched, edited locally)
    │   └── oncall-rota.csv     (new, uncommitted)
    └── Runbooks
        └── Rollback.xml        (new, uncommitted)
```

`X.files/` next to `X.xml` holds that page's attachments; `X/` holds its
child pages.

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

## Attachments

Clone and pull download pages, not attachment bytes. Every attachment is
listed in its page as an `<attachment>` element with service-owned
attributes, fetched or not:

```xml
<attachment id="att98231" name="rollback-flow.png" type="image/png" size="74" version="2" created="…" author="kbogdan"/>
<attachment id="att98232" name="runbook-template.pdf" type="application/pdf" size="88312" version="1" created="…" author="alice"/>
```

Fetch bytes when you need them, for one attachment or for all of a page:

```shell
$ gfs get eng/Home/Runbooks.files/rollback-flow.png
  +  eng/Home/Runbooks.files/rollback-flow.png   74 B   v2
$ gfs get eng/Home/Runbooks.xml             # every attachment of the page
```

A fetched attachment is tracked in `.gfs/attachments` (page id, attachment
id, version, sha256 as fetched, path). From then on it syncs both ways: `pull`
refreshes it when the remote version moves, `commit` uploads it when you
change it. Attachments you never fetched are never touched locally.

```shell
$ cp ~/Downloads/rota.csv eng/Home/Runbooks.files/oncall-rota.csv
$ gfs status
  M  eng/Home/Runbooks.files/rollback-flow.png   upload new version of attachment (v2)
  A  eng/Home/Runbooks.files/oncall-rota.csv     attach to "Runbooks"
  C  eng/Home.files/logo.svg                     changed locally and on remote v4 by bob; remote copy: logo.remote-v4.svg
$ gfs commit eng/Home/Runbooks.files
update  eng/Home/Runbooks.files/rollback-flow.png   v2 -> v3   ok
create  eng/Home/Runbooks.files/oncall-rota.csv     ok   id=att98240
2 actions, 0 failed
```

The page's XML is rewritten with the new `<attachment>` element and version.

| you do                                   | commit does                     |
|------------------------------------------|---------------------------------|
| drop a new file into `Page.files/`       | upload (create)                 |
| edit a fetched file                      | upload a new version            |
| remove the `<attachment>` element        | delete on remote (`ask`)        |
| delete only the local bytes              | nothing: the copy is evicted, `gfs get` brings it back |
| rename a file inside `Page.files/`       | nothing, warning (not supported yet) |
| move or rename `Page.xml`                | moves `Page.files/` with it     |

`eng/Home.files/logo.svg` is frozen mid-conflict: it was fetched at v3 and
edited locally, and bob uploaded v4. Binaries do not merge, so pull kept your
file and wrote the remote one next to it. Commit refuses the attachment until
you pick:

```shell
$ gfs resolve --theirs eng/Home.files/logo.svg   # take v4, drop your edit
$ gfs resolve --ours   eng/Home.files/logo.svg   # keep yours; next commit uploads it as v5
```

Either way the `.remote-v4` copy is removed.
