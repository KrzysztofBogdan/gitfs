# Bitbucket adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bitbucket://` adapter mirroring Bitbucket Cloud pull requests (read/write) and pipeline runs (read-only, logs inline or on demand), plus `gfs tail` to stream a running pipeline.

**Architecture:** A new `internal/adapter/bitbucket` package on the shared `atlassian.Client`, one merged schema covering the `<pullrequest>` and `<pipeline>` roots (the engine is single-schema; `schema.Schema.Roots` lets validation accept the second root), a fake API in `bbtest`, and a new optional `adapter.Tailer` interface driven by `internal/cli/tail.go`, which writes the watched run back through a new `engine.PullOpts.IDs`.

**Tech Stack:** Go 1.25, cobra, `net/http/httptest`, existing `xmltree`, `schema`, `atlassian`, `engine` packages.

**Spec:** `docs/superpowers/specs/2026-10-06-bitbucket-design.md`

## Global Constraints

- Bitbucket Cloud only; API base `https://api.bitbucket.org/2.0`; Basic auth email + scoped API token.
- Credentials via `atlassian.Creds(..., "GFS_BITBUCKET")`, host `bitbucket.org`.
- URL params exactly: `filter`, `exclude`, `since`, `limit`, `pipelineLimitSize` (plus test-only `base`).
- `pipelineLimitSize` default `1kb` = 1024 bytes; a log of ≤ limit bytes is inline.
- Policy defaults gain `merge = ask`, `decline = ask`; comment delete uses existing `delete = ask`.
- Tail defaults: `--interval 5s`, step list every 3 intervals, `--wait 2m`, back-off to 30 s, `X-RateLimit-NearLimit` → 30 s.
- Tail exit codes: 0 successful, 1 failed/error/stopped/expired, 2 cannot watch, 130 Ctrl-C.
- Resource ids: `<repo>/pr/<n>` and `<repo>/pipelines/<uuid-without-braces>`.
- Paths: `<repo>/pr/<n> <title>.xml` (Jira `issuePath` sanitising), `<repo>/pipelines/<build_number>.xml`.
- Requests sequential; no parallel fetching.
- Match surrounding code style: terse comments citing spec sections, table tests, `mustContain`/`mustRun` in CLI tests.

---

## File map

| file | responsibility |
|---|---|
| `internal/schema/schema.go` | `Roots []string` field |
| `internal/validate/validate.go` | accept `s.Roots` |
| `internal/policy/policy.go` | `merge`, `decline` defaults |
| `internal/adapter/adapter.go` | `Tailer`, `TailOpts`, `TailResult` |
| `internal/engine/pull.go` | `PullOpts.IDs` |
| `internal/adapter/atlassian/client.go` | Bitbucket error shape, `Range`, `NearLimit` |
| `internal/adapter/bitbucket/url.go` | URL forms, selection, sizes, target |
| `internal/adapter/bitbucket/adapter.go` | registration, Describe, Open, Login |
| `internal/adapter/bitbucket/schema.go`, `relaxng.go` | schemas, merged schema, grammar |
| `internal/adapter/bitbucket/api.go` | JSON types, paging helper |
| `internal/adapter/bitbucket/pr.go` | PR JSON ↔ XML (pure) |
| `internal/adapter/bitbucket/run.go` | pipeline JSON → XML (pure) |
| `internal/adapter/bitbucket/session.go` | List, Fetch, Download, cursor |
| `internal/adapter/bitbucket/people.go` | members cache, resolution |
| `internal/adapter/bitbucket/apply.go` | Check, Apply |
| `internal/adapter/bitbucket/advise.go` | Available |
| `internal/adapter/bitbucket/tail.go` | Tail |
| `internal/adapter/bitbucket/bbtest/*.go` | fake API |
| `internal/cli/tail.go` | `gfs tail` |
| `internal/cli/root.go`, `docs.go` | registration, help topic, schema |
| `embed.go`, `docs/bitbucket.md`, `start.md`, `Readme.md` | docs |
| `internal/cli/bitbucket_e2e_test.go` | end-to-end |
| `internal/adapter/bitbucket/realsite_test.go` | gated real-site checks |

---

### Task 1: Engine groundwork

**Files:**
- Modify: `internal/schema/schema.go`, `internal/validate/validate.go`, `internal/policy/policy.go`, `internal/adapter/adapter.go`, `internal/engine/pull.go`
- Test: `internal/validate/validate_test.go`, `internal/policy/policy_test.go`, `internal/engine/pull_ids_test.go` (new)

**Interfaces:**
- Produces: `schema.Schema.Roots []string`; `adapter.Tailer{ Tail(ctx, id string, o TailOpts, out io.Writer) (TailResult, error) }`, `adapter.TailOpts{Wait, Interval time.Duration}`, `adapter.TailResult{RunID, Result string}`; `engine.PullOpts.IDs []string`.

- [ ] **Step 1: failing tests**

```go
// validate_test.go
func TestAlternateRoot(t *testing.T) {
	s := &schema.Schema{Root: "a", Roots: []string{"b"}, Elems: []schema.Elem{{Name: "x", Kind: schema.Field}}}
	if err := Resource(p(t, `<b><x>1</x></b>`), nil, s); err != nil {
		t.Fatal(err)
	}
	if err := Resource(p(t, `<c/>`), nil, s); err == nil || !strings.Contains(err.Error(), "want <a>") {
		t.Fatalf("%v", err)
	}
}
// policy_test.go
func TestMergeDeclineAsk(t *testing.T) {
	p, _ := FromConfig(nil)
	for _, c := range []string{"merge", "decline"} {
		if p.Level(c) != Ask { t.Fatal(c) }
	}
	if p.Level("review") != Allow { t.Fatal("review") }
}
```

`pull_ids_test.go`: with the engine's fake adapter (`internal/adapter/fake`, used by `engine_test.go` helpers), clone two resources, change both on the fake remote, `Pull(ctx, env, PullOpts{IDs: []string{"<id1>"}})`; assert only id1's file changed, `Index.Cursor` unchanged, and a third remote-only resource requested by id is added.

- [ ] **Step 2:** `go test ./internal/validate/ ./internal/policy/ ./internal/engine/ -run 'Alternate|MergeDecline|PullIDs'` → FAIL.

- [ ] **Step 3: implement**

```go
// schema.go, in Schema:
	Roots     []string // other root element names whose elements are also in Elems
// validate.go
	if root.Name != s.Root && !slices.Contains(s.Roots, root.Name) {
// policy.go
var defaults = map[string]string{"send": Ask, "delete": Ask, "publish": Ask, "reply": Ask, "approve": Ask, "dns": Ask, "merge": Ask, "decline": Ask}
// adapter.go
// Tailer is implemented by sessions that can stream a running job's output
// (bitbucket spec §9.4). id is a resource id from the index.
type Tailer interface {
	Tail(ctx context.Context, id string, o TailOpts, out io.Writer) (TailResult, error)
}
type TailOpts struct{ Wait, Interval time.Duration }
type TailResult struct {
	RunID  string // the run watched; a PR id resolves to one
	Result string // successful, failed, error, stopped, expired; "" when ctx ended first
}
// pull.go, PullOpts:
	IDs    []string // only these resources, fetched one by one; the cursor is kept
// pull.go, replacing the List call:
	var l adapter.Listing
	if len(o.IDs) > 0 {
		for _, id := range o.IDs {
			res, err := e.Session.Fetch(ctx, id)
			if errors.Is(err, adapter.ErrNotFound) { continue }
			if err != nil { e.report(adapter.Progress{Phase: "done"}); return r, err }
			l.Resources = append(l.Resources, *res)
		}
	} else if l, err = e.Session.List(ctx, cursor); err != nil { … }
	keepCursor := o.Filter != nil || len(o.IDs) > 0
```

- [ ] **Step 4:** run the three packages' tests → PASS; `go test ./...` still green.
- [ ] **Step 5:** commit `engine: alternate schema roots, merge/decline ask, Tailer, pull by id`.

---

### Task 2: atlassian client additions

**Files:** Modify `internal/adapter/atlassian/client.go`; Test `internal/adapter/atlassian/client_test.go`.

**Interfaces:**
- Produces: `(*Client).Range(ctx, path string, from, to int64, w io.Writer) (total int64, err error)` (to < 0 = open end; total -1 unknown); `(*Client).NearLimit() bool`; `APIError.Message` handles `{"error":{"message","detail","fields"}}`.

- [ ] **Step 1: failing tests** (httptest servers):
  - error body `{"type":"error","error":{"message":"Bad","detail":"why","fields":{"title":["required"]}}}` → `Message()` == `Bad: why; title: required`.
  - Range: server answering 206 with `Content-Range: bytes 0-3/10` → writes 4 bytes, total 10; 206 with `/*` → -1; 200 ignoring Range with 10-byte body and to=3 → writes 4, total 10; 416 with `Content-Range: bytes */10` → 0 bytes, total 10; 404 → total -1, nil error.
  - NearLimit: response header `X-RateLimit-NearLimit: true` → `NearLimit()` true after a `Do`.
- [ ] **Step 2:** `go test ./internal/adapter/atlassian/` → FAIL.
- [ ] **Step 3: implement.** In `Message`, decode also `Error struct{Message, Detail string; Fields map[string][]string}` under `"error"`; join message and detail with `: `, then sorted fields `k: v1, v2`. Add `nearLimit atomic.Bool` set from every response in `sendRead` (`resp.Header.Get("X-RateLimit-NearLimit") == "true"`). `Range` builds a GET with `Range: bytes=<from>-[<to>]`, `Accept: */*`, sends via `c.xfer`; switch on status as listed; parse `Content-Range` with `regexp(`^bytes (?:\d+-\d+|\*)/(\d+|\*)$`)`; for 200 copy `io.LimitReader(body, to-from+1)` when to ≥ 0 and use `resp.ContentLength`.
- [ ] **Step 4:** tests PASS.
- [ ] **Step 5:** commit `atlassian: Bitbucket error shape, Range reads, rate-limit hint`.

---

### Task 3: URL, target and adapter skeleton

**Files:** Create `internal/adapter/bitbucket/url.go`, `adapter.go`; Test `url_test.go`; Modify `internal/cli/root.go` (blank import).

**Interfaces:**
- Produces:
```go
type selection struct {
	workspace     string
	repos, exclude []string
	since         string
	limit         int
	logLimit      int64 // pipelineLimitSize in bytes
}
type target struct {
	base, email, token string
	sel                selection
}
func normalize(u *url.URL) (*url.URL, error)
func parseSelection(n *url.URL) (selection, error)
func parseSize(s string) (int64, error)          // "0", "512", "1kb", "64kb", "1mb"
func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error)
func defaultDir(u *url.URL) string
func (s selection) wants(repo string) bool
func sinceTime(since string, now time.Time) time.Time // zero when since == ""
```

- [ ] **Step 1: failing tests** (table):

| in | normalised |
|---|---|
| `bitbucket://bitbucket.org/acme` | `bitbucket://bitbucket.org/acme` |
| `bitbucket://bitbucket.org/acme/api` | `bitbucket://bitbucket.org/acme?filter=api` |
| `bitbucket://me%40x.com@bitbucket.org/acme?limit=5k&since=-90d` | `bitbucket://me%40x.com@bitbucket.org/acme?since=-90d&limit=5k` |
| `bitbucket:https://bitbucket.org/acme/api/pull-requests/142` | `bitbucket://bitbucket.org/acme?filter=api` |
| `bitbucket:https://bitbucket.org/acme` | `bitbucket://bitbucket.org/acme` |
| `bitbucket://bitbucket.org/` | error `want bitbucket://bitbucket.org/<workspace>` |
| `bitbucket://example.com/acme` | error (host must be bitbucket.org) |
| `bitbucket://bitbucket.org/acme?pipelineLimitSize=2gb` | error `pipelineLimitSize=2gb: want 0, 512, 1kb, 64kb, 1mb` |
| `bitbucket://bitbucket.org/acme?colour=red` | error `unknown parameter "colour"` |

  Query order: `base, filter, exclude, since, limit, pipelineLimitSize`. `parseSize`: `1kb`→1024, `1mb`→1048576, `0`→0, default (absent) → 1024. `defaultDir`: `api` for one repo, else `acme`. `parseTarget` with `GFS_BITBUCKET_EMAIL`/`_TOKEN` set → email/token; base default `https://api.bitbucket.org/2.0`, overridden by `?base=`. `sinceTime("-2w", now)` = now−14d; `"-6m"` = AddDate(0,-6,0); `"2026-01-01"` parsed.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: implement.** Model on `jira/url.go` (`split`, `encodeQuery`, `sinceRe`, `limitRe` copied locally; repo slugs match `^[a-z0-9][a-z0-9_.-]*$` lower-cased). `adapter.go`:

```go
func init() { adapter.Register(&Adapter{}) }
type Adapter struct{}
func (*Adapter) Name() string                 { return "bitbucket" }
func (*Adapter) Schemes() []string            { return []string{"bitbucket"} }
func (*Adapter) Schema() *schema.Schema       { return mergedSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }
func (*Adapter) Normalize(u *url.URL) (string, error) { n, err := normalize(u); if err != nil { return "", err }; return n.String(), nil }
func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.Store{Dirs: creds.DefaultDirs()})
	if err != nil { return nil, err }
	return newSession(t), nil
}
```
  (`Describe`, `Login` land in Tasks 9 and 12; stub `Describe` sets `a.Class = a.Verb` now. `newSession` stub in session.go returning a session whose methods return `errors.New("not implemented")` until Task 7 — keep compile green.)
- [ ] **Step 4:** PASS. **Step 5:** commit `bitbucket: remote URLs, selection and credentials`.

---

### Task 4: Schemas and RELAX NG

**Files:** Create `schema.go`, `relaxng.go`; Test `schema_test.go`.

**Interfaces:** Produces `prSchema`, `runSchema`, `mergedSchema *schema.Schema`; `RelaxNG() string`.

- [ ] **Step 1: failing tests**: `mergedSchema.Root == "pullrequest"`, `Roots == []string{"pipeline"}`; no element name declared twice with different Kind; `mergedSchema.Attachment().Name == "steplog"` with no Ops; `validate.Resource` accepts the §5.1 and §5.5 examples (copied as test fixtures) and rejects a changed read-only `number`; `RelaxNG()` is well-formed XML (`xmltree.ParseString`) and names both `pullrequest` and `pipeline`; `canon.Normalize` on a pipeline example orders `step` by `n` numerically (`n="10"` after `n="9"`).
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: implement.**

```go
func ro(names ...string) []schema.Attr { … }            // as jira/schema.go
func user(name string) schema.Elem { return schema.Elem{Name: name, Kind: schema.Field, Attrs: []schema.Attr{{Name: "account"}}} }

var prElems = []schema.Elem{
	{Name: "title", Kind: schema.Field},
	{Name: "state", Kind: schema.Field, Attrs: []schema.Attr{{Name: "strategy"}, {Name: "message"}}},
	{Name: "draft", Kind: schema.Field},
	{Name: "source", Kind: schema.Field, Attrs: []schema.Attr{{Name: "branch"}, {Name: "commit", ReadOnly: true}}},
	{Name: "destination", Kind: schema.Field, Attrs: []schema.Attr{{Name: "branch"}, {Name: "commit", ReadOnly: true}}},
	{Name: "close-source-branch", Kind: schema.Field},
	user("author"),
	{Name: "reviewer", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: []schema.Attr{{Name: "account"}, {Name: "state", ReadOnly: true}}},
	{Name: "participant", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: ro("account", "state")},
	{Name: "my-review", Kind: schema.Field},
	{Name: "merge-commit", Kind: schema.Field},
	{Name: "description", Kind: schema.Field},
	{Name: "build", Kind: schema.Field, Repeated: true, SortKey: "key", Attrs: ro("key", "state", "url", "run")},
	{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
		Attrs: append(ro("id", "account", "author", "created", "updated"),
			schema.Attr{Name: "parent"}, schema.Attr{Name: "path"}, schema.Attr{Name: "line"}, schema.Attr{Name: "side"}, schema.Attr{Name: "resolved"})},
}
var runElems = []schema.Elem{
	{Name: "state", …same decl as above…},
	{Name: "result", Kind: schema.Field},
	{Name: "trigger", Kind: schema.Field, Attrs: ro("type", "account")},
	{Name: "target", Kind: schema.Field, Attrs: ro("type", "ref", "commit", "pipeline", "pr")},
	{Name: "pr", Kind: schema.Field, Attrs: ro("file")},
	{Name: "duration", Kind: schema.Field, Attrs: ro("build-seconds")},
	{Name: "step", Kind: schema.Sub, ID: "id", Less: stepLess,
		Attrs: ro("id", "n", "name", "image", "parallel", "stage", "state", "result", "started", "completed", "duration", "log-size"),
		Children: []schema.Elem{{Name: "log", Kind: schema.Field}}},
	{Name: "steplog", Kind: schema.Attachment, ID: "step", SortKey: "name", NameAttr: "name", Attrs: ro("step", "name", "size")},
}
var rootAttrs = ro("id", "number", "created", "updated", "completed", "version")
var prSchema = &schema.Schema{Root: "pullrequest", ID: "id", Version: "version", RootAttrs: rootAttrs, Elems: prElems}
var runSchema = &schema.Schema{Root: "pipeline", ID: "id", Version: "version", RootAttrs: rootAttrs, Elems: runElems}
// mergedSchema is what the engine sees: both roots' elements in one canonical
// order (state is shared). Check keeps each file to its own root's elements.
var mergedSchema = merge(prSchema, runSchema)
func merge(a, b *schema.Schema) *schema.Schema // order: a's elems, then b's not already present, inserted after their predecessor in b
func stepLess(x, y *xmltree.Node) bool { return atoi(attr(x, "n")) < atoi(attr(y, "n")) }
```
  `relaxng.go`: a grammar with `start` = choice of envelope / `pullrequest` / `pipeline`, each root an `interleave` of `zeroOrMore` elements with any attributes (same shape as `dnsx.RelaxNG`, written for two roots).
- [ ] **Step 4:** PASS. **Step 5:** commit `bitbucket: pull request and pipeline schemas`.

---

### Task 5: Fake Bitbucket API (`bbtest`)

**Files:** Create `internal/adapter/bitbucket/bbtest/server.go`, `read.go`, `write.go`, `pipelines.go`; Test `server_test.go`.

**Interfaces:** Produces
```go
type Server struct{ URL string /* + unexported */ }
func New() *Server; func (s *Server) Close()
const Me = "712020:me"                      // account of the token
func (s *Server) AddMember(account, name, nickname string)
func (s *Server) AddRepo(slug string)
func (s *Server) AddPR(repo string, pr PR) int          // returns id
func (s *Server) AddComment(repo string, pr int, c Comment) int
func (s *Server) SetStatus(repo, commit string, st Status)
func (s *Server) AddRun(repo string, r Run) string      // returns uuid "{…}"
func (s *Server) Advance(repo string, number int)       // next step starts/finishes, appends log
func (s *Server) PR(repo string, id int) *PR
func (s *Server) Calls() []string                        // "GET /2.0/…", writes and reads
func (s *Server) RateLimit(n int)                        // next n requests answer 429 Retry-After: 1
func (s *Server) NearLimit(on bool)
type PR struct{ ID int; Title, Description, State, Source, Dest, SourceCommit, DestCommit, Author string; Draft, CloseSource bool;
	Reviewers []Participant; Participants []Participant; Strategies []string; DefaultStrategy, MergeCommit string; Created, Updated time.Time }
type Participant struct{ Account, Role, State string } // role REVIEWER|PARTICIPANT, state approved|changes_requested|""
type Comment struct{ ID, Parent int; Account, Raw, Path, Side string; Line int; Resolved, Deleted bool; Created, Updated time.Time }
type Status struct{ Key, State, Name, URL string }
type Run struct{ Number int; UUID, State, Result, Trigger, Creator, RefType, RefName, Commit, Selector string; PR int;
	Steps []Step; Created, Completed time.Time }
type Step struct{ UUID, Name, Image, Parallel, State, Result string; Log []byte; Started, Completed time.Time }
```
Routes (Go 1.22 mux patterns), all under `/2.0`:
- `GET /user`, `GET /workspaces/{ws}/members`, `GET /repositories/{ws}`, `GET /repositories/{ws}/{repo}/refs/branches/{name...}`
- `GET|POST /repositories/{ws}/{repo}/pullrequests` (state=, q=`updated_on > "…"`/`>=`, sort=±updated_on, pagelen, page→`next`)
- `GET|PUT /…/pullrequests/{id}`, `POST|DELETE /…/{id}/approve`, `POST|DELETE /…/{id}/request-changes`, `POST /…/{id}/merge` (first call per PR answers 202 `{"task_status_url"…}` when `MergeAsync` set), `GET /…/{id}/merge/task-status/{task}`, `POST /…/{id}/decline`
- `GET|POST /…/{id}/comments`, `PUT|DELETE /…/{id}/comments/{cid}`, `POST|DELETE /…/{id}/comments/{cid}/resolve`
- `GET /…/{id}/statuses`
- `GET /…/pipelines/` (sort=-created_on, `target.commit.hash=`), `GET /…/pipelines/{uuid}`, `GET /…/pipelines/{uuid}/steps/`, `GET /…/pipelines/{uuid}/steps/{step}/log` (Range: 206/416/404 before start; `Content-Range` total `*` while the step is in progress)
- Every write bumps the PR's `Updated` (comment writes too: the fake follows the documented behaviour; a knob `CommentsBumpUpdated bool` default true lets session tests cover the other branch).
- Errors in Bitbucket shape; author-as-reviewer → 400 `"… is the author and cannot be included as a reviewer."`; merge on non-open → 400.

- [ ] **Step 1:** `server_test.go`: paging (`pagelen=1` gives `next` until exhausted), q filter, Range 206/416/404, Advance moves state, 429 then success.
- [ ] **Step 2:** FAIL → **Step 3:** implement → **Step 4:** PASS.
- [ ] **Step 5:** commit `bitbucket: fake API`.

---

### Task 6: PR and run codecs (pure)

**Files:** Create `api.go` (JSON types), `pr.go`, `run.go`; Test `pr_test.go`, `run_test.go`, fixtures in `testdata/`.

**Interfaces:** Produces
```go
type apiUser struct{ AccountID string `json:"account_id"`; DisplayName string `json:"display_name"`; Nickname string `json:"nickname"`; UUID string `json:"uuid"` }
type apiPR struct{ … mirrors Bitbucket 2.0 pullrequest … }
type apiComment struct{ … id, parent{id}, user, content{raw}, inline{path, from, to}, deleted, resolution, created_on, updated_on … }
type apiStatus struct{ Key, State, Name, URL string }
type apiPipeline struct{ … uuid, build_number, state{name, stage{name}, result{name}}, target{type, ref_type, ref_name, commit{hash}, selector{type, pattern}, pullrequest{id}}, trigger{type}, creator, created_on, completed_on, duration_in_seconds, build_seconds_used … }
type apiStep struct{ UUID, Name string; Image struct{ Name string }; State …; StartedOn, CompletedOn string; DurationInSeconds int; … }

type prInput struct{ Repo string; PR apiPR; Comments []apiComment; Statuses []apiStatus; Me string; RunFile func(statusKey, url string) string }
func prRoot(in prInput) *xmltree.Node
func prVersion(in prInput) string                         // updated_on + "+" + 8 hex of sha256(statuses, comment ids+updated)
func prPath(repo string, id int, title string) string     // "<repo>/pr/<id> <title>.xml"
func prID(repo string, id int) string                     // "<repo>/pr/<id>"
func parsePRID(id string) (repo string, n int, ok bool)

type stepLog struct{ Data []byte; Total int64 } // Total -1 unknown
type runInput struct{ Repo string; Run apiPipeline; Steps []apiStep; Logs map[string]stepLog; Limit int64; PRFile string }
func runRoot(in runInput) *xmltree.Node
func runVersion(p apiPipeline, steps []apiStep) string     // state[:result]:completed_on, or state + 8 hex of step states while running
func runPath(repo string, number int) string               // "<repo>/pipelines/<n>.xml"
func runID(repo, uuid string) string                       // braces stripped
func parseRunID(id string) (repo, uuid string, ok bool)
func logName(n int, stepName string) string               // "1-build.log"
func isBig(l stepLog, limit int64) bool                    // Total > limit, or Total == -1 && len(Data) > limit
```

- [ ] **Step 1: failing tests.**
  - `prRoot` on `testdata/pr142.json` + comments + statuses → equals `testdata/pr142.xml` (the §5.1 example with real values) after `canon.Normalize(…, mergedSchema)`.
  - `my-review`: `approved` when the Me participant has `state: approved`; `changes_requested`; else `none`.
  - Deleted comments omitted; reply has `parent`; inline `to` → `side="new" line`, `from` only → `side="old"`; `resolution` present → `resolved="true"`, top-level without → `resolved="false"`, replies have no `resolved`.
  - `prVersion` changes when only a status state changes; unchanged when nothing changes.
  - `runRoot`: inline log at exactly `Limit` bytes, `steplog` at `Limit+1`; `Total: -1` with `Limit+1` bytes → big; steps that never started have no `log-size`; `trigger@type` `push` from `PUSH`/`pipeline_trigger_push`; selector `custom:deploy`; invalid UTF-8 replaced.
  - `prPath("api", 142, "Add: rate/limit")` matches Jira's `sanitize` output.
- [ ] **Step 2:** FAIL. **Step 3:** implement (reuse a local copy of Jira's `sanitize`/`maxName` logic; `el(name, attrs...)` helper as in cloudns). **Step 4:** PASS.
- [ ] **Step 5:** commit `bitbucket: pull request and pipeline files`.

---

### Task 7: Session: List, Fetch, Download, cursor

**Files:** Create `session.go`, `client.go`; Test `session_test.go`.

**Interfaces:**
- Consumes: Task 2 client, Task 5 fake, Task 6 codecs.
- Produces:
```go
type session struct{ c *atlassian.Client; t target; me string; report func(adapter.Progress); cacheDir string; now func() time.Time; people *people }
func newSession(t target) *session
func (s *session) get(ctx context.Context, path string, q url.Values, out any) error
func pages[T any](ctx context.Context, s *session, path string, q url.Values, each func(T) (more bool, err error)) error // follows "next"
type cursor map[string]repoCursor
type repoCursor struct{ PRs time.Time `json:"prs"`; Run int `json:"run"` }
func (s *session) fetchPR(ctx context.Context, repo string, id int) (*adapter.Resource, error)
func (s *session) fetchRun(ctx context.Context, repo, uuid string) (*adapter.Resource, error)
```
  Implements `adapter.Session`, `adapter.Reporter`, `adapter.Cacher`, `adapter.Identified`.

- [ ] **Step 1: failing tests** against bbtest:
  - Clone (empty cursor) of 2 repos × (2 PRs, 2 runs): 8 resources with `Root`, `FullDirs == ["api","web"]`, paths per §4, cursor decodes to both repos.
  - `limit=1`: one PR and one run per repo. `since=2026-10-01`: older PRs absent (q contains `updated_on >= "2026-10-01T00:00:00Z"`), older runs absent.
  - Incremental: after clone, change one PR title on the fake, finish one in-progress run, add a run, set a new status on an open PR → listing holds exactly: changed PR, the status-changed PR (new version), the finished run, the new run; `Full == false`; no request for completed runs' steps (assert `Calls()`).
  - `CommentsBumpUpdated=false`: open PRs re-fetch comments each pull (assert their comment GET appears).
  - A repo removed from `filter` → every id recorded for it in the cursor is in `Deleted`. `repoCursor` carries `IDs []string` (every PR and run id listed for that repo), plus `Open []int` (open PR numbers) and `Running []string` (uuids of runs not completed) which drive the incremental re-fetches of §6.3.
  - A failed request on repo 2 (fake 500 after retries) → error; repo 1's progress is not lost because List returns an error and the engine keeps the old cursor (engine behaviour) — assert only that `List` returns the error.
  - `Fetch("api/pr/1")` and `Fetch("api/pipelines/<uuid>")` versions equal List's; unknown → `ErrNotFound`.
  - `Download("api/pipelines/<uuid>", "<step uuid>")` streams the full log; `AttachmentInfo{Version: "-", Size: n}`.
  - 429 once → a `wait` progress report, then success.
- [ ] **Step 2:** FAIL. **Step 3:** implement per spec §6. `s.me` from `GET /user` (`account_id`) on first need. **Step 4:** PASS.
- [ ] **Step 5:** commit `bitbucket: clone and pull pull requests and pipeline runs`.

---

### Task 8: People

**Files:** Create `people.go`; Test `people_test.go`.

**Interfaces:** Produces
```go
type member struct{ Account, Name, Nick string }
type people struct{ cachePath string; byAccount map[string]member; list []member; fresh bool }
func (s *session) resolveUser(ctx context.Context, text string) (member, error)
```
- [ ] **Step 1: failing tests:** exact account id; unique nickname; unique display name; two `Bo …` display names → error listing both with accounts; unknown → refresh once from `GET /workspaces/<ws>/members` then error `no workspace member matches`; cache written to `<cacheDir>/bitbucket/people.json` and reused without a request next session.
- [ ] **Step 2-4:** FAIL → implement → PASS.
- [ ] **Step 5:** commit `bitbucket: workspace members and reviewer resolution`.

---

### Task 9: Describe, Check and Apply

**Files:** Create `apply.go`; Modify `adapter.go` (Describe); Test `apply_test.go`.

**Interfaces:**
- Consumes: engine actions (`update` with `Group`, Sub `create/update/delete` with `Target` `comment[n]`/`comment[id=…]`, `create`/`delete` with empty target).
- Produces: `func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource)`, `func (s *session) Check(ctx, req) []adapter.Result`, `func (s *session) Apply(ctx, req) []adapter.Result`.

Describe re-classes actions:

| engine action | Verb / Class | Detail |
|---|---|---|
| `create` | create / create | `create PR <src> → <dst> in <repo>` |
| `delete` (file) | delete / delete | refused in Check: `pull requests cannot be deleted; set <state>declined</state>` |
| `update` Group `state` | merge or decline / same | §7.6 |
| `update` Group `my-review` | review / review | `approve` / `request changes` / `withdraw approval` |
| `update` other groups | update / update | `update <group>` |
| comment create | comment / comment | `comment on path:line` / `reply to comment N` / `comment` |
| comment update | comment / comment, or `resolve`/`reopen` detail when only `resolved` changed | |
| comment delete | delete / delete | `delete comment N` |

Check (no writes) refuses with an error on that action's Result:
- any action on a run file (id has `/pipelines/` or path contains `/pipelines/`): `pipeline runs are read-only (use gfs tail to watch one)`;
- new file not directly under `<repo>/pr/` or repo not selected; missing title/branches; branch not found (`GET …/refs/branches/<name>`);
- pipeline-only elements in a PR file (`step`, `result`, …): `<step> belongs in a pipeline file`;
- state transitions other than open→merged/declined; strategy not in `merge_strategies` (read from base fetch: keep strategies in the session after `fetchPR`, keyed by id);
- edit of `author`, `participant`, other's comment text, fixed comment attrs (`parent`,`path`,`line`,`side`), reviewer text with `account` kept;
- new reviewer names resolve (Task 8).

Apply runs §7.2 order regardless of engine order: create → one PUT for all update groups → comments (creates in document order; a new comment whose `parent` refers to a comment created earlier in this Apply by local index `parent="new:1"` is out of scope — `parent` must be an existing id) → review → merge/decline (skipped with `skipped: an earlier action failed` if any prior result failed). Merge 202 → poll task status every 2 s up to 5 min using `s.sleep`.

- [ ] **Step 1: failing tests** (fake + session, building `ApplyRequest` from fixture XML):
  - title + reviewer add → one PUT whose body has `title` and `reviewers:[{account_id…}]`.
  - new top-level, reply, inline (`side="old"` → `inline.from`) comments → three POSTs with the right JSON.
  - own comment edit → PUT; other's comment edit → Check error.
  - `resolved` false→true → POST resolve.
  - `my-review` none→approved → POST approve; approved→changes_requested → DELETE approve then POST request-changes.
  - state merged with `strategy="squash"` → POST merge `{"merge_strategy":"squash","close_source_branch":true}`; with async 202 → task polled; strategy `rebase` → Check error naming allowed ones.
  - failed PUT then merge → merge Result error `skipped: an earlier action failed`, merge not called.
  - declined → POST decline. merged→open → Check error `a merged PR cannot be reopened`.
  - create from new file → POST with source/dest, Result.ID = `api/pr/<n>`.
  - run file change → Check error.
  - Describe table above.
- [ ] **Step 2-4:** FAIL → implement → PASS.
- [ ] **Step 5:** commit `bitbucket: commit pull request changes`.

---

### Task 10: `gfs actions` (Advisor)

**Files:** Create `advise.go`; Test `advise_test.go`.
- [ ] **Step 1: failing tests:** open PR with 2 approvals and a failed build → `State == "open, 2 approvals, builds: 1 failed"`; items `merge` (Name `squash (default), merge_commit`), `decline`, `approve`, `request changes`; after `my-review` approved → `withdraw` instead of `approve`; merged PR → no items; local `<state strategy="squash">merged</state>` → `Note == "commit will merge with squash"`.
- [ ] **Step 2-4** → **Step 5:** commit `bitbucket: gfs actions on a pull request`.

---

### Task 11: Tail (adapter) and `gfs tail` (CLI)

**Files:** Create `internal/adapter/bitbucket/tail.go`, `internal/cli/tail.go`; Modify `internal/cli/root.go` (add `newTail()`); Test `tail_test.go`, `internal/cli/tail_test.go`.

**Interfaces:**
- Consumes: `adapter.Tailer` (Task 1), `Client.Range`/`NearLimit` (Task 2), `engine.PullOpts.IDs` (Task 1).
- Produces: `func (s *session) Tail(ctx, id, o, out) (adapter.TailResult, error)`; `func newTail() *cobra.Command`.

Adapter loop (`s.sleep` injectable for tests):
```go
type tailStep struct{ uuid, name, image string; off int64; started, ended bool; partial []byte }
// header/footers per spec §9.2; prefix "[name] " when >1 step running; partial lines held.
```
CLI:
```go
func newTail() *cobra.Command {
	var wait, interval, timeout time.Duration
	cmd := &cobra.Command{Use: "tail <run-or-pr-file>", Short: "Stream a running pipeline's logs; exit with its result", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, true)
			…resolve rel → index entry → id (usage error when untracked)…
			tl, ok := env.Session.(adapter.Tailer)
			if !ok { return usage("%s cannot tail", env.Adapter.Name()) }
			ctx := cmd.Context(); if timeout > 0 { ctx, cancel = context.WithTimeout(ctx, timeout) }
			res, terr := tl.Tail(ctx, id, adapter.TailOpts{Wait: wait, Interval: interval}, cmd.OutOrStdout())
			if res.RunID != "" { ids := []string{res.RunID}; if res.RunID != id { ids = append(ids, id) }
				engine.Pull(context.WithoutCancel(cmd.Context()), env, engine.PullOpts{IDs: ids}) // output to stderr
			}
			switch { case cmd.Context().Err() != nil: return &ExitError{Code: 130}
			case terr != nil: return terr // exit 2
			case res.Result == "successful": return nil
			case res.Result == "": return &ExitError{Code: 2, Err: errors.New("timed out")}
			default: return &ExitError{Code: 1} }
		}}
	cmd.Flags().DurationVar(&wait, "wait", 2*time.Minute, "…"); interval 5s; timeout 0
	return cmd
}
```
  `engine.Pull` prints to `env.Out`; for tail set `env.Out = cmd.ErrOrStderr()` before the pull so stdout carries only log bytes.

- [ ] **Step 1: failing tests:**
  - Adapter: a fake run with two parallel steps advanced by a fake sleep → output equals a golden transcript (header, prefixed lines, footers, final line); result `failed`.
  - Completed run → full dump, one pass, no sleeps.
  - PR id with no run yet → polls `target.commit.hash`; run appears after 2 ticks → tails it; `Wait` elapsed → error `no pipeline started for <hash>`.
  - Back-off: 12 ticks without bytes at 5 s → interval 10 s; `NearLimit` → 30 s.
  - ctx cancelled mid-run → returns `RunID` set, `Result == ""`.
  - CLI: `gfs tail` on a Jira-like fake-adapter file → exit 2 `cannot tail` (use `internal/adapter/fake`).
- [ ] **Step 2-4** → **Step 5:** commit `gfs tail: stream a running pipeline and exit with its result`.

---

### Task 12: Login, CLI wiring, docs, end-to-end

**Files:** Modify `adapter.go` (Login), `internal/cli/docs.go` (help topic `bitbucket`, `schema bitbucket`), `embed.go` (`docs/bitbucket.md`), `start.md`, `Readme.md`; Create `docs/bitbucket.md`, `internal/cli/bitbucket_e2e_test.go`.

- [ ] **Step 1: failing e2e test** (`bitbucket_e2e_test.go`), fake with repos `api`, `web`:
  1. `clone bitbucket://bitbucket.org/acme?base=<srv>/2.0` → `Cloned N resources`; files `api/pr/1 Add rate limiting.xml`, `api/pipelines/7.xml`.
  2. Edit title, add reviewer `<reviewer>Bo Smith</reviewer>`, new comment, reply, inline comment → `status` shows each detail; `commit` → ok; file renamed to the new title; comments carry ids.
  3. Remote title change + local description change → `pull` merges.
  4. `<my-review>approved</my-review>` → commit → fake shows approval.
  5. `<state strategy="squash">merged</state>` → `commit` exit 1 `needs confirmation: rerun with --allow merge`; `commit --allow merge` → merged.
  6. Second PR declined with `--allow decline`.
  7. New file `api/pr/new.xml` with title/source/destination → created, renamed to `api/pr/3 ….xml`.
  8. `gfs get api/pipelines/7.xml` → `api/pipelines/7.files/1-build.log` exists with the full log.
  9. Save `api/pipelines/7.xml`, edit it → `commit api/pipelines/7.xml` exit 1 with `pipeline runs are read-only`; write the saved bytes back.
  10. Running run 8: `gfs tail api/pipelines/8.xml` with `--interval 1ms` (fake Advance on each log request) → exit 1, stdout has step headers, `api/pipelines/8.xml` now `<state>completed</state>`.
  11. `gfs actions "api/pr/2 ….xml"` lists merge strategies.
  12. `gfs schema bitbucket` is well-formed; `gfs help bitbucket` prints the doc.
- [ ] **Step 2:** FAIL. **Step 3:** implement Login:
```go
// Login guides the user to a scoped Bitbucket API token (bitbucket spec §3.1).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error
```
  printing the token page and scopes, `ReadLine("Atlassian account email: ")`, `ReadSecret("API token: ")`, verifying with `GET /user` and `GET /repositories/<ws>?pagelen=1` (base overridable by `io.Flags["base"]`), refusing with `&adapter.LoginRefused{…}` on 401/403, storing via `creds.Store{…}.Set(email, token)` + host default as `atlassian.Login` does (read `atlassian/login.go` and mirror its storage calls). Write `docs/bitbucket.md` covering spec §12 items, add help topic + schema case + embed entry, start.md rows (`bitbucket://` in the session example, `gfs tail` in the command table, a `gfs tail` exit-code note), Readme service list.
- [ ] **Step 4:** `go test ./...` PASS; `go vet ./...` clean; `gofmt -l .` empty.
- [ ] **Step 5:** commit `bitbucket: end to end, gfs auth login, docs`.

---

### Task 13: Real-site checks (gated)

**Files:** Create `internal/adapter/bitbucket/realsite_test.go`.

```go
// Real-site checks, skipped unless the environment names a workspace:
//
//	GFS_BITBUCKET_SITE=bitbucket://bitbucket.org/acme/api go test ./internal/adapter/bitbucket/ -run TestReal -v
//
// Writes also need a scratch repository, named twice:
//
//	GFS_BITBUCKET_SCRATCH=acme/scratch GFS_BITBUCKET_WRITE_CONFIRM=acme/scratch
```
- [ ] `TestRealRead`: list with `limit=20`, each resource round-trips through `canon.Normalize` and `validate.Resource`; one big log downloads; logs the measurements of spec §11.4 (`t.Logf`) for comments bumping `updated_on` (read-only: compare a PR's `updated_on` with its newest comment's `created_on`), Content-Range on a running step if one exists.
- [ ] `TestRealWrite` (skipped unless both scratch vars match): uses the Bitbucket API to create a branch from main via `POST /repositories/<ws>/<repo>/refs/branches`, creates a PR, edits, comments, replies, inline-comments, resolves, approves (needs a second account: skip approve when the PR author is me — Bitbucket refuses self-approval), requests changes, declines; second branch + PR merged with squash; deletes both branches; reports failed cleanup.
- [ ] Run without env → both SKIP. Commit `bitbucket: real-site checks`.

---

## Self-review notes

- Spec §6.6 deletion of a dropped repo: covered by `repoCursor.IDs` (Task 7).
- Spec §7.2 reply to a comment created in the same commit: the spec says "resolves parent to the id just written back"; the engine has no id write-back for subs within one Apply, so Task 9 requires `parent` to be an existing id and Check refuses a `parent` that is not on the base (`reply to a comment that exists; commit the parent first`). Spec §7.2 updated accordingly in Task 12's doc pass.
- `gfs tail` stdout carries only log bytes; pull output goes to stderr (Task 11).
