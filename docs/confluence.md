# gfs and Confluence Cloud

One working tree mirrors a Confluence site: every selected space is a folder,
every page is one XML file, the page tree is the folder tree. General `gfs`
usage: [start.md](../start.md).

## Remote URL

```text
confluence://[<email>@]<site>.atlassian.net[?filter=K1,K2&type=<type>|all&exclude=K1,K2]
confluence://[<email>@]<site>.atlassian.net/<SPACEKEY>
confluence:https://<site>.atlassian.net/wiki/spaces/<SPACEKEY>/…
```

| parameter | meaning | default |
|-----------|---------|---------|
| `filter=K1,K2` | exactly these spaces, any type, archived included | all spaces |
| `type=<type>` | only spaces of this Confluence type (`global`, `collaboration`, `knowledge_base`, `personal`), or `all`; only without `filter` | every type except `personal` |
| `exclude=K1,K2` | leave these out | none |

* No `filter`: every current space the account can see, personal spaces
  left out unless `type` asks for them. Archived spaces are skipped.
* `confluence://<site>/HF` is short for `?filter=HF`. The key is the one in
  the space URL: `https://warsaw-dynamics.atlassian.net/wiki/spaces/HF` → `HF`.
* The third form is a URL copied from the browser, prefixed with
  `confluence:`. Any page of the space works; it always means the whole
  space.
* `type` and `filter` cannot be combined.
* The email is optional and URL-encoded (`@` becomes `%40`):
  `confluence://me%40example.com@acme.atlassian.net/ENG`.
* `?base=<url>` overrides the API base URL (tests only).

```shell
gfs clone confluence://acme.atlassian.net                       # every non-personal space, into acme/
gfs clone confluence://acme.atlassian.net/ENG                   # one space, into eng/
gfs clone "confluence:https://acme.atlassian.net/wiki/spaces/ENG/overview"
gfs clone "confluence://acme.atlassian.net?type=all&exclude=ARCHIVE"
```

`clone` records the URL in its canonical form (`confluence://<site>?filter=ENG`)
as `[remote] url` in `.gfs/config`. To change which spaces the tree holds,
edit that line; the next `pull` adds the spaces now selected and removes the
files of spaces no longer selected.

## Credentials

Confluence Cloud takes an email plus an Atlassian API token
(<https://id.atlassian.com/manage-profile/security/api-tokens>). One token
belongs to the account, so it works on every site that account can reach.

```shell
gfs auth set me@example.com --host acme.atlassian.net
```

This verifies the token against `https://acme.atlassian.net/wiki/rest/api/user/current`,
stores it in the system keyring (service `gfs`, entry `atlassian:me@example.com`),
and makes `me@example.com` the default for that site in `~/.config/gfs/config`.
Tokens stored by `alogin` are used as well, without copying them.

Which email is used, first match wins: `GFS_CONFLUENCE_EMAIL`, the URL user,
`[remote] email` in `.gfs/config`, the site default, the only stored identity.
The token comes from `GFS_CONFLUENCE_TOKEN`, else the keyring entry for that
email. `clone` writes the email it used to `[remote] email`, so a working tree
stays on one account even when you have several.

## Layout

```text
acme/                           the working tree
├── eng/                        space ENG, key lower-cased
│   ├── Home.xml                a page
│   ├── Home.files/             its attachments (downloaded with gfs get)
│   │   └── logo.svg
│   └── Home/                   its child pages
│       ├── Architecture.xml
│       ├── Runbooks.xml
│       └── Runbooks/
│           └── Rollback.xml
└── ~jan/                       personal space ~jan
    └── Jan's Home.xml
```

* The first folder is the space. A new top-level folder does not create a
  space; commit refuses it.

* `Title.xml` is a page; `Title/` holds its children; `Title.files/` holds its
  attachments.
* File names come from page titles: `/` and `\` become `-`, control
  characters become spaces, a leading `.` becomes `_`, a name ending in
  `.files` gets `_` appended, an empty title becomes `untitled`.
* Two sibling pages whose names collide (case-insensitively) get ` (2)`,
  ` (3)` in page-id order.

## Page file

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <page id="98120" version="7" parent="98001" created="2026-01-01T10:00:00.000Z" updated="2026-09-22T14:03:00.000Z">
      <title>Architecture</title>
      <labels>
        <label>design</label>
        <label>backend</label>
      </labels>
      <body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">
        <p>The system has three parts.</p>
      </body>
      <comment id="77" author="bob" created="2026-09-20T09:00:00.000Z" version="1"><p>Looks good.</p></comment>
      <attachment id="att98231" name="diagram.png" type="image/png" size="7421" version="2" created="..." author="kbogdan"/>
    </page>
  </content>
</gfs>
```

| part | editable | notes |
|------|----------|-------|
| `id`, `version`, `parent`, `created`, `updated` | no | owned by Confluence; `version` is the optimistic lock and gfs bumps it |
| `<title>` | yes | changing it renames the page |
| `<labels>` | yes | one `<label>` per label; order does not matter |
| `<body>` | yes | Confluence storage format, passed through untouched |
| `<comment>` | yes | footer comments; without `id` = new |
| `<attachment>` | remove only | metadata listing; bytes live in `Title.files/` |

### Body

The body is Confluence **storage format**: XHTML plus `ac:` macros and `ri:`
references. gfs never interprets it, so anything the editor produces survives
a round trip: macros, layouts, task lists, panels, inline comment markers.

Useful building blocks:

```xml
<h2>Heading</h2>
<p>Text with <strong>bold</strong>, <em>italic</em>, <code>code</code> and a <a href="https://example.com">link</a>.</p>
<ul><li>bullet</li></ul>
<ol><li>step</li></ol>
<table><tbody><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></tbody></table>

<ac:structured-macro ac:name="code" ac:schema-version="1">
  <ac:parameter ac:name="language">bash</ac:parameter>
  <ac:plain-text-body><![CDATA[./deploy.sh --tag v2.3.1]]></ac:plain-text-body>
</ac:structured-macro>

<ac:structured-macro ac:name="info" ac:schema-version="1">
  <ac:rich-text-body><p>A note panel.</p></ac:rich-text-body>
</ac:structured-macro>

<ac:link><ri:page ri:content-title="Runbooks"/></ac:link>
<ac:image><ri:attachment ri:filename="diagram.png"/></ac:image>
```

If the body uses `ac:` or `ri:`, keep the `xmlns:ac` and `xmlns:ri`
declarations on `<body>` as shown above.

### Storage reference

Well-formed storage is not enough: Confluence silently drops forms it does not
accept. gfs carries a reference of forms verified against Confluence Cloud:

```shell
gfs help confluence-storage             # every node and variant, with status
gfs example confluence                  # list nodes and variants
gfs example confluence panel            # all panel variants
gfs example confluence panel/info       # one fragment, ready to paste
gfs schema confluence > storage.rng     # RELAX NG for a whole page file
xmllint --noout --relaxng storage.rng "eng/Home/Architecture.xml"
```

`gfs status` and `gfs commit` warn when an edit introduces something outside
the verified set, and say so explicitly when Confluence is known to drop it:

```text
  M  eng/Home.xml                             update body
        warning: <span data-highlight-colour>: Confluence drops this; for a text highlight write <span style="background-color: rgb(…)"> (see marks/background-color)
```

Warnings never block a commit. Forms already on the page before your edit do
not warn.

## What each change does

| you do | status | commit does |
|--------|--------|-------------|
| create `Parent/New.xml` | `A  create page under "Parent"` | creates the page as a child of `Parent` |
| create `eng/New.xml` | `A  create top-level page` | Confluence Cloud puts it under the space homepage; the file moves to `eng/<Home>/New.xml` |
| edit `<body>` | `M  update body` | new page version |
| edit `<title>` | `M  rename page to "…"` | renames the page; the file is renamed on write-back |
| edit `<labels>` | `M  update labels` | adds and removes labels |
| add `<comment>` without `id` | `add comment` | posts a footer comment |
| edit / remove a `<comment>` | `edit comment 77` / `delete comment 77` | edits or deletes it |
| rename `Title.xml` in place | `R  rename to "…"` | renames the page to the new file name |
| move `Title.xml` to another folder | `R  move under "Other"` | re-parents the page; `Title/` and `Title.files/` move with it |
| delete `Title.xml` | `D  delete page  [ask]` | deletes the page (asks first) |

Moving a page to the space root (`eng/Title.xml` from somewhere deeper) is not
supported; move it under a page.

## Create a page

A new file can be just the `<page>` element; gfs adds the envelope, ids and
version when it commits and rewrites the file:

```shell
mkdir -p "hf/Handoff Home"
cat > "hf/Handoff Home/Release notes.xml" <<'XML'
<page>
  <title>Release notes</title>
  <labels><label>release</label></labels>
  <body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">
    <h2>2026-09</h2>
    <ul><li>Keyring support in gfs.</li></ul>
  </body>
</page>
XML
gfs status
#   A  hf/Handoff Home/Release notes.xml   create page under "Handoff Home"
gfs commit --dry-run
gfs commit
#   create  hf/Handoff Home/Release notes.xml   ok   id=…
```

The parent is the page whose folder the file sits in, and that parent must
already exist on Confluence (commit it first). Without `<title>` the file name
becomes the title. After commit the file is written back under the name
derived from the title.

## Pull

`pull` lists every page's version (one request per 250 pages) and searches
for comments and attachments changed since the last pull. It downloads only
pages whose version changed or that those searches name, so a pull with
nothing new downloads no pages.

Not seen by that search: label-only changes, and deleted comments or
attachments. They arrive the next time the page is edited, or with

```shell
gfs pull --full      # download every page
```

## Attachments

`clone` and `pull` list attachments in each page but never download bytes.

```shell
gfs get eng/Home/Runbooks.xml                    # every attachment of a page
gfs get eng/Home/Runbooks.files/diagram.png      # one attachment
```

Once fetched, an attachment syncs both ways:

| you do | commit does |
|--------|-------------|
| drop a new file into `Page.files/` | uploads it |
| edit a fetched file | uploads a new version |
| remove the `<attachment>` element | deletes it on Confluence (`ask`) |
| delete only the local bytes | nothing; `gfs get` brings it back |
| rename a file inside `Page.files/` | nothing, with a warning (not supported) |

If a fetched attachment changed both locally and on Confluence, pull keeps
yours and writes the remote copy next to it as `name.remote-vN.ext`. Pick one
with `gfs resolve --ours|--theirs Page.files/name.ext`.

## Conflicts

Confluence rejects an update whose version is not the current one. gfs
fetches the newer version, merges it with your change, and commits the
result. If both sides changed the same lines, the file is marked `C`:

```shell
gfs commit
#   C  eng/Home/Architecture.xml   conflict with remote v8 by bob 2026-09-22T14:03, 1 element, 1 hunk
gfs resolve --theirs eng/Home/Architecture.xml    # or edit, remove <conflict/>, commit
gfs commit
```

## Not supported yet

* Inline comments as editable elements (their markers in the body survive).
* Page restrictions, watchers, blog posts, whiteboards, databases.
* Moving a page to the space root.
* Creating a space (a new top-level folder).
* Moving a page between spaces.
* Renaming an attachment.
* Wide or full-width code blocks and expands, and a table's numbered first
  column: Confluence drops these settings when they come through storage
  format (set them in the editor; gfs keeps whatever Confluence stores).
