# Confluence storage reference design

Status: approved in chat. Date: 2026-09-24.

## 1. Purpose

Agents editing Confluence pages through gfs write the page body in storage
format (XHTML with `ac:`/`ri:`). Well-formed storage is not enough: Confluence
silently rewrites or drops forms it does not accept (a highlight on
`<span data-highlight-colour>`, `ac:breakout-mode` on a code macro). Agents
need a reference that says what survives, checked against the real service,
and they need it wherever a working tree is, not only in this repository.

Three parts, each keeping the next honest:

1. **Examples** checked by a live round trip.
2. **A RELAX NG schema** that accepts what survives and rejects what is known
   to be dropped.
3. **Docs in the binary**: help topics, schema and examples printed by `gfs`.

Non-goals: ADF JSON, marketplace macros, inline comments, writing an
`AGENTS.md` into working trees, validating bodies inside `gfs commit`.

## 2. Examples

```
docs/confluence/examples/
  <node>/<variant>.xml          body fragment, the form we write
  <node>/<variant>.remote.xml   what Confluence returned, only when it differs
  _rejected/<name>.xml          forms known to be dropped or rewritten
docs/confluence/roundtrip.json  {"<node>/<variant>": "same" | "changed", ...}
docs/confluence/storage.md      generated catalogue
```

* A variant file holds one or more sibling block nodes: the body fragment
  exactly as it would sit inside `<body>`, with `ac:`/`ri:` prefixes and no
  namespace declarations. Its first line may be an XML comment describing it
  (`<!-- info panel -->`); the comment is stripped before pushing.
* `<node>` names follow ADF (`panel`, `codeBlock`, `table`, `mediaSingle`, ...)
  or `macro-<name>` for built-in macros.

### 2.1 Live round trip

`TestStorageExamples` in `internal/adapter/confluence`, skipped unless
`GFS_EXAMPLES_URL=confluence://<host>/<SPACE>` is set. Credentials resolve as
for any remote (env, URL user, host default, keyring).

1. Find or create the page `gfs storage examples` under the space homepage.
2. For each node directory, build one page titled `gfs example: <node>`: every
   variant in file-name order, each preceded by `<h6>gfs:<variant></h6>`.
   Create it under the parent, or update it if a page with that title exists.
3. Fetch the page back (`body-format=storage`), split the body at the `<h6>`
   markers, and compare each segment with its source in canonical form.
4. Canonical form for comparison: parsed with the storage parser, printed by
   `xmltree.Print`, with these service-owned attributes removed everywhere:
   `ac:macro-id`, `ac:local-id`, `local-id`, `data-local-id`, `ri:version-at-save`;
   attributes sorted; `ac:parameter` children of a macro sorted by name
   (Confluence reorders them). `ac:schema-version` is compared: Confluence
   upgrades old versions, and the example should use the current one. The
   rules live in one place (`canonicalFragment`).
5. Report each variant as `same` or `changed` (`t.Log`). A changed variant is
   not a test failure unless it is recorded as `same` in `roundtrip.json`
   (regression).
6. With `-update`: rewrite `roundtrip.json`, write `<variant>.remote.xml` for
   changed variants (and delete stale ones), and regenerate `storage.md`.

`storage.md` is also regenerated offline by `TestStorageCatalogue -update`
(no network) from the examples and `roundtrip.json`, so edits to comments do
not need a live run. The offline test fails when the committed `storage.md`
is stale.

### 2.2 Catalogue format

```markdown
## panel

### info — same
<!-- info panel -->
```xml
<ac:structured-macro ac:name="info" ...>...</ac:structured-macro>
```

### error — changed
We write: (fragment)  Confluence stores: (remote fragment)
```

## 3. Schema

`docs/confluence/storage.rng`, RELAX NG XML syntax (checked with
`xmllint --relaxng`). It validates a whole gfs page file: optional `<gfs>`
envelope with `<content>`, then `<page>` with `title`, `labels`, `body`,
`comment`, `attachment`, and the body vocabulary.

* Built by hand from the examples; it allows only element/attribute forms
  seen in `same` variants (plus their remote forms, which are by definition
  what Confluence stores).
* Offline test `TestStorageSchema` (skipped without `xmllint`):
  every `same` variant and every `.remote.xml`, wrapped in a page, validates;
  every `_rejected/*.xml`, wrapped in a page, fails; `test/`-independent.
* Style values (`style="..."`) are checked only as strings; colour and
  alignment vocabularies are documented in the catalogue, not enforced.

## 4. Docs in the binary

A root package `github.com/KrzysztofBogdan/gitfs` (file `embed.go`) embeds
`start.md`, `docs/confluence.md`, `docs/confluence/storage.md`,
`docs/confluence/storage.rng`, and `docs/confluence/examples/**`.

| command | prints |
|---------|--------|
| `gfs help start` | `start.md` |
| `gfs help confluence` | `docs/confluence.md` |
| `gfs help confluence-storage` | `docs/confluence/storage.md` |
| `gfs schema confluence` | `storage.rng` |
| `gfs example confluence` | node list with variant counts |
| `gfs example confluence <node>` | every variant of the node, each with its status |
| `gfs example confluence <node>/<variant>` | the fragment only (pipe-friendly) |

Help topics are cobra commands without `Run`, so they show under
"Additional help topics" in `gfs --help`. Unknown node or variant: exit 2
listing the valid names.
