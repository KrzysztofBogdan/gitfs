# Confluence storage reference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Storage-format examples verified by a live round trip, a RELAX NG schema kept honest by tests, and all of it printed by `gfs` from the binary.

**Architecture:** Example fragments live under `docs/confluence/examples/<node>/<variant>.xml`. A live test in the Confluence adapter pushes one page per node, splits the result at `<h6>gfs:<variant></h6>` markers, and records `same`/`changed` in `roundtrip.json`; `-update` also writes remote forms and regenerates the catalogue `storage.md`. An offline test keeps the catalogue fresh and checks the schema with `xmllint`. A root package embeds the docs; cobra help topics and two commands print them.

**Tech Stack:** Go 1.25, `embed`, cobra, `xmllint` (tests only).

**Spec:** `docs/superpowers/specs/2026-09-24-confluence-storage-reference-design.md`

## Global Constraints

- Run every `go` command with `GOTOOLCHAIN=go1.25.0`. `gofmt -l .` prints nothing and `go vet ./...` passes before every commit.
- No offline test talks to the network. The live test runs only with `GFS_EXAMPLES_URL` set.
- Marker between variants: `<h6>gfs:<variant></h6>`. Parent page title `gfs storage examples`; node page title `gfs example: <node>`.
- Attributes ignored when comparing: `ac:macro-id`, `ac:local-id`, `local-id`, `data-local-id`.
- Variant file: body fragment, `ac:`/`ri:` prefixes, no namespace declarations, optional leading `<!-- description -->`.

## File structure

```
docs/confluence/examples/<node>/<variant>.xml     (Task 1)
docs/confluence/examples/_rejected/*.xml          (Task 1)
internal/adapter/confluence/examples.go           example loading, splitting, canonical compare, catalogue render (Task 2)
internal/adapter/confluence/examples_test.go      TestStorageCatalogue (offline), TestStorageExamples (live) (Task 2)
docs/confluence/roundtrip.json, storage.md, *.remote.xml   (Task 2, generated)
docs/confluence/storage.rng                       (Task 3)
internal/adapter/confluence/schema_test.go        TestStorageSchema (Task 3)
embed.go (package gitfs)                          (Task 4)
internal/cli/docs.go, internal/cli/docs_test.go   help topics, schema, example (Task 4)
```

Examples code lives in the confluence package (not `_test.go`) only where Task 4 needs it: loading examples from an `fs.FS` and listing nodes/variants. Push/fetch code stays in the test.

### Task 1: Examples from the node test page

- [ ] Split `test/confluence/hf/Handoff Home/test.xml`'s source content (sections 1-16 as originally written) into one file per variant, grouped by ADF node. Each file starts with a description comment.
- [ ] Add `_rejected/` with the three forms Confluence dropped: `highlight-span.xml` (`<span data-highlight-colour>`), `code-breakout.xml` (`ac:breakout-mode` on a code macro), `table-number-column.xml` (`data-number-column`).
- [ ] `xmllint --noout` every file wrapped in a root with the `ac`/`ri` namespaces.
- [ ] Commit: `docs: confluence storage examples`.

### Task 2: Round trip and catalogue

- [ ] `examples.go`: `type Example struct{ Node, Variant, Comment, Body, Remote string }`, `LoadExamples(fsys fs.FS) ([]Example, error)` (sorted by node, then variant; skips `_rejected`; fills `Remote` from `.remote.xml`), `Nodes(ex []Example) []string`, `RenderCatalogue(ex []Example, status map[string]string) string`, and unexported `canonicalFragment(storage string) (string, error)` (parse with the storage parser, drop ignored attributes, print) and `splitMarkers(storage string) (map[string]string, error)`.
- [ ] Offline tests: `canonicalFragment` drops `ac:macro-id` and is stable; `splitMarkers` splits a two-variant body; `TestStorageCatalogue` renders from `docs/confluence` and compares with `storage.md` (rewrites with `-update`).
- [ ] Live `TestStorageExamples` per spec §2.1, with `-update` writing `roundtrip.json`, `.remote.xml` files (removing stale ones) and `storage.md`.
- [ ] Run live against `confluence://warsaw-dynamics.atlassian.net/HF` with `-update`; inspect the changed variants.
- [ ] Commit code and generated files.

### Task 3: Schema

- [ ] `docs/confluence/storage.rng` covering the envelope, page fields and the body vocabulary seen in `same` variants and remote forms.
- [ ] `TestStorageSchema`: every `same` variant and remote form validates when wrapped in `<page><title>t</title><body type="application/xhtml+xml" xmlns:ac=... xmlns:ri=...>…</body></page>`; every `_rejected` file fails; also `example/confluence/**/*.xml` pages without conflict markers validate. Skip when `xmllint` is missing.
- [ ] Commit.

### Task 4: Docs in the binary

- [ ] `embed.go` at the repo root: `package gitfs`, `//go:embed start.md docs/confluence.md docs/confluence/storage.md docs/confluence/storage.rng docs/confluence/roundtrip.json docs/confluence/examples` into `var Docs embed.FS`.
- [ ] `internal/cli/docs.go`: help topics `start`, `confluence`, `confluence-storage` (commands with `Long` = file content and no `Run`); `gfs schema confluence`; `gfs example confluence [<node>[/<variant>]]` using `confluence.LoadExamples(fs.Sub(gitfs.Docs, "docs/confluence/examples"))` and `roundtrip.json`. Unknown adapter, node or variant: usage error listing valid names.
- [ ] Tests: `gfs help start` contains `# gfs: getting started`; `gfs schema confluence` starts with `<?xml` or `<grammar`; `gfs example confluence` lists `panel`; `gfs example confluence panel/info` prints the fragment; unknown node exits 2.
- [ ] Commit.

### Task 5: Docs pointers

- [ ] `docs/confluence.md`: "Storage reference" section pointing at `gfs help confluence-storage`, `gfs example confluence`, `gfs schema confluence` and `xmllint --relaxng`.
- [ ] `start.md`: list the help topics and the two commands.
- [ ] Reinstall, run `gfs help confluence-storage | head`, full test suite, commit.
