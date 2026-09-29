# Confluence Site Clone Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One gfs working tree mirrors a whole Confluence site (or a chosen set of spaces), and `pull` fetches only what changed.

**Architecture:** The Confluence adapter's single `session` becomes multi-space: it resolves a space selection from the remote URL, loads each space's page tree on demand, and routes creates to the space named by a file's first folder. `List` with a cursor returns lightweight stubs (id, version, path) and fetches in full only pages whose comments or attachments changed, found by two CQL searches. The engine is unchanged apart from a `pull --full` flag and an optional `adapter.Normalizer` used by `clone`.

**Tech Stack:** Go 1.25, standard library, cobra (already a dependency). Tests use `internal/adapter/confluence/cftest` (in-memory Confluence) and the CLI end-to-end helpers in `internal/cli`.

**Spec:** `docs/superpowers/specs/2026-09-29-confluence-site-clone-design.md`

## Global Constraints

- No backward compatibility: single-space working trees are not migrated.
- No new dependencies.
- Remote forms: `confluence://[<email>@]<site>[?filter=…&type=…&exclude=…]`, `confluence://[<email>@]<site>/<KEY>`, `confluence:https://<site>/wiki/spaces/<KEY>[/…]`, `confluence:https://<site>/wiki[/…]`. Any other path is an error.
- `type` defaults to `global`; `type` with `filter` is an error; `filter` includes archived spaces; without `filter`, archived spaces are skipped; unknown `exclude` keys are ignored.
- Space folder = key lower-cased. Page paths inside a space follow the existing `pagePaths` rules unchanged.
- `clone` records the normalised URL as `[remote] url`.
- Cursor: UTC RFC 3339 time `List` started. Search window: `now("-<N>m")`, N = minutes since cursor rounded up (never below 0) plus 10.
- The two searches: `type = comment AND lastmodified >= now("-Nm")` and `type = attachment AND lastmodified >= now("-Nm")`, via `GET /wiki/rest/api/content/search?limit=250&expand=container&cql=…`, with ` AND space in ("K1","K2")` only when `filter` is set.
- A failed space lookup, page list or search fails the whole `pull`; the cursor is not advanced.
- Refusals: new top-level folder → `no space "<dir>" in this tree; gfs does not create spaces (spaces: …)`; move between spaces → `moving pages between spaces is not supported`.
- Run the whole suite with `go test ./...` before every commit; it must pass.

## Review Focus

1. A personal space key (`~jan`) in the URL path or in `filter` — must parse, clone into `~jan/`, and survive the round trip through `[remote] url`. Test: Task 3 `TestNormalize` rows with `~jan`; Task 8 `filter=OLD,~jan` clone.
2. A browser URL carrying its own query and fragment (`?atlOrigin=…#section`) — must be ignored, only `/spaces/<KEY>` counts. Test: Task 3 `TestNormalize` row "browser with query and fragment".
3. The same page title in two spaces — both get the same relative path in their own folder, and creating a page whose title exists only in another space succeeds. Test: Task 4 `TestListSite` (two `Home.xml`), Task 5 `TestTitleTakenPerSpace`.
4. A page moved on Confluence from one selected space to another — must show up under the new space folder (engine then sees a move of the same id), not vanish. Test: Task 4 `TestPageMovedBetweenSpaces`.
5. One space's page list failing (403) mid-pull — must fail the listing with the space named, not report that space's pages as deleted. Test: Task 6 `TestListFailsWhenASpaceFails`.

---

## File Structure

| file | change | responsibility |
|------|--------|----------------|
| `internal/adapter/confluence/cftest/server.go` | modify | fake: space types/status, multi-key paging, clock, v1 content search, request log helpers |
| `internal/adapter/confluence/cftest/server_test.go` | create | tests of the new fake endpoints |
| `internal/adapter/confluence/client.go` | modify | `paginate` follows REST v1 `_links.next` |
| `internal/adapter/confluence/url.go` | modify | `normalize`, `selection`, `parseSelection`, `target.sel` |
| `internal/adapter/confluence/url_test.go` | create | URL forms and selection tests |
| `internal/adapter/adapter.go` | modify | `Normalizer` interface |
| `internal/adapter/confluence/adapter.go` | modify | `Normalize`, `DefaultDir`, package doc |
| `internal/cli/clone.go` | modify | record the normalised URL |
| `internal/adapter/confluence/paths.go` | modify | `pageRef` gains `Space`, `Version` |
| `internal/adapter/confluence/session.go` | modify | multi-space session, per-space loading, routing |
| `internal/adapter/confluence/listing.go` | create | incremental `List`, cursor, search |
| `internal/adapter/confluence/site_test.go` | create | multi-space session tests |
| `internal/adapter/confluence/listing_test.go` | create | incremental listing tests |
| `internal/engine/pull.go`, `internal/cli/pull.go` | modify | `--full` |
| `internal/adapter/fake/fake.go` | modify | record cursors passed to `List` |
| `internal/cli/confluence_site_e2e_test.go` | create | whole-site end-to-end |
| `docs/confluence.md`, `start.md` | modify | user docs |

---

### Task 1: Fake Confluence: spaces, clock, content search

**Files:**
- Modify: `internal/adapter/confluence/cftest/server.go`
- Create: `internal/adapter/confluence/cftest/server_test.go`

**Interfaces:**
- Produces:
  - `type Space struct{ Key, ID, Type, Status string }` (Type `"global"`/`"personal"`, Status `"current"`/`"archived"`; empty means global/current)
  - `func (s *Server) PutSpace(sp Space)`; `AddSpace(key, id string)` keeps its signature (global, current)
  - `Server.Clock func() time.Time` (nil → `time.Now`); `func (s *Server) Stamp() string` (clock in `2006-01-02T15:04:05.000Z`)
  - `Comment.UpdatedAt string` (set by comment edits)
  - `func (s *Server) SetLabels(pageID string, labels ...string)` (no version bump, like Confluence)
  - `func (s *Server) TakeRequests() []string` (returns and clears `Requests`)
  - `GET /wiki/api/v2/spaces` honours `keys` (comma list), `type`, `status`, pagination
  - `GET /wiki/rest/api/content/search` serves the two CQL shapes; next links are v1-style (no `/wiki` prefix)

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/confluence/cftest/server_test.go`:

```go
package cftest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, s *Server, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.URL+path, nil)
	req.SetBasicAuth("me@x.com", "t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func keysOf(m map[string]any) string {
	var out []string
	for _, r := range m["results"].([]any) {
		out = append(out, r.(map[string]any)["key"].(string))
	}
	return strings.Join(out, ",")
}

func TestSpacesFilters(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddSpace("ENG", "100")
	s.PutSpace(Space{Key: "~jan", ID: "300", Type: "personal"})
	s.PutSpace(Space{Key: "OLD", ID: "400", Status: "archived"})
	cases := map[string]string{
		"/wiki/api/v2/spaces":                                "ENG,OLD,~jan",
		"/wiki/api/v2/spaces?status=current":                 "ENG,~jan",
		"/wiki/api/v2/spaces?status=current&type=global":     "ENG",
		"/wiki/api/v2/spaces?type=personal":                  "~jan",
		"/wiki/api/v2/spaces?keys=OLD," + url.QueryEscape("~jan"): "OLD,~jan",
	}
	for path, want := range cases {
		if _, m := get(t, s, path); keysOf(m) != want {
			t.Errorf("%s: got %s, want %s", path, keysOf(m), want)
		}
	}
	s.PageLimit = 1
	_, m := get(t, s, "/wiki/api/v2/spaces")
	if next := m["_links"].(map[string]any)["next"].(string); !strings.HasPrefix(next, "/wiki/api/v2/spaces?") {
		t.Fatalf("v2 next link: %q", next)
	}
}

func TestContentSearch(t *testing.T) {
	s := New()
	defer s.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s.Clock = func() time.Time { return now }
	s.AddSpace("ENG", "100")
	s.AddSpace("OPS", "200")
	s.AddPage(Page{ID: "1", Title: "E", SpaceID: "100"})
	s.AddPage(Page{ID: "2", Title: "O", SpaceID: "200"})
	s.AddComment(Comment{ID: "c-old", PageID: "1", CreatedAt: "2026-09-29T11:00:00.000Z"})
	s.AddComment(Comment{ID: "c-new", PageID: "1", CreatedAt: "2026-09-29T11:55:00.000Z"})
	s.AddComment(Comment{ID: "c-ops", PageID: "2", CreatedAt: "2026-09-29T11:58:00.000Z"})
	s.AddAttachment(Attachment{ID: "a1", PageID: "2", Title: "x.png", CreatedAt: "2026-09-29T11:59:00.000Z"})

	search := func(cql string) (int, map[string]any) {
		return get(t, s, "/wiki/rest/api/content/search?limit=250&expand=container&cql="+url.QueryEscape(cql))
	}
	containers := func(m map[string]any) string {
		var out []string
		for _, r := range m["results"].([]any) {
			out = append(out, r.(map[string]any)["container"].(map[string]any)["id"].(string))
		}
		return strings.Join(out, ",")
	}
	if _, m := search(`type = comment AND lastmodified >= now("-10m")`); containers(m) != "1,2" {
		t.Fatalf("comments in 10m: %v", m)
	}
	if _, m := search(`type = comment AND lastmodified >= now("-10m") AND space in ("OPS")`); containers(m) != "2" {
		t.Fatalf("scoped: %v", m)
	}
	if _, m := search(`type = attachment AND lastmodified >= now("-10m")`); containers(m) != "2" {
		t.Fatalf("attachments: %v", m)
	}
	if code, _ := search(`type = page`); code != 400 {
		t.Fatalf("unsupported CQL must be 400, got %d", code)
	}
	s.PageLimit = 1
	_, m := search(`type = comment AND lastmodified >= now("-10m")`)
	if next := m["_links"].(map[string]any)["next"].(string); !strings.HasPrefix(next, "/rest/api/content/search?") {
		t.Fatalf("v1 next link must be relative to /wiki: %q", next)
	}
}

func TestStampAndRequests(t *testing.T) {
	s := New()
	defer s.Close()
	s.Clock = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	if s.Stamp() != "2026-09-29T12:00:00.000Z" {
		t.Fatal(s.Stamp())
	}
	get(t, s, "/wiki/api/v2/spaces")
	if r := s.TakeRequests(); len(r) != 1 || r[0] != "GET /wiki/api/v2/spaces" {
		t.Fatalf("%v", r)
	}
	if r := s.TakeRequests(); len(r) != 0 {
		t.Fatalf("not cleared: %v", r)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapter/confluence/cftest/`
Expected: FAIL to compile (`PutSpace`, `Space`, `Clock`, `Stamp`, `TakeRequests` undefined).

- [ ] **Step 3: Implement**

In `server.go`:

1. Imports: add `"regexp"`, `"strings"`, `"time"`.
2. Add the `Space` type and replace the `spaces` field and `now` field:

```go
type Space struct {
	Key, ID string
	Type    string // "global" (default) or "personal"
	Status  string // "current" (default) or "archived"
}
```

In `Server`: replace `spaces map[string]string // key -> id` with `spaces map[string]*Space // by key`, delete `now string`, and add after `Accounts`:

```go
	Clock     func() time.Time  // time for Stamp and search windows; nil is time.Now
```

In `New`: `spaces: map[string]*Space{}` and remove `now: "2026-09-23T12:00:00.000Z"`. Register the route next to the other v1 routes:

```go
	mux.HandleFunc("GET /wiki/rest/api/content/search", s.search)
```

3. `Comment` gains `UpdatedAt`:

```go
type Comment struct {
	ID, PageID, Storage, AuthorID, CreatedAt, UpdatedAt string
	Version                                             int
}
```

4. Helpers (replace `AddSpace`):

```go
const stampLayout = "2006-01-02T15:04:05.000Z"

func (s *Server) clock() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// Stamp is the server's time in Confluence's format. Give content added after a
// clone this CreatedAt so the change searches find it.
func (s *Server) Stamp() string { return s.clock().UTC().Format(stampLayout) }

func (s *Server) AddSpace(key, id string) { s.PutSpace(Space{Key: key, ID: id}) }

func (s *Server) PutSpace(sp Space) {
	if sp.Type == "" {
		sp.Type = "global"
	}
	if sp.Status == "" {
		sp.Status = "current"
	}
	s.mu.Lock()
	s.spaces[sp.Key] = &sp
	s.mu.Unlock()
}

// SetLabels replaces a page's labels without a new page version, as Confluence does.
func (s *Server) SetLabels(pageID string, labels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages[pageID].Labels = labels
}

// TakeRequests returns the requests served since the last call and forgets them.
func (s *Server) TakeRequests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.Requests
	s.Requests = nil
	return r
}
```

5. Replace every `s.now` with `s.Stamp()` (`EditPage`, `createPage`, `updatePage`, `createComment`, `EditAttachment`, `createAttachment`, `updateAttachment`). `Stamp` takes no lock, so it is safe inside handlers. In `updateComment` add `c.UpdatedAt = s.Stamp()` next to the version bump.

6. Paging: replace `paged` with a shared writer and a v1 variant:

```go
// paged writes one page of items; the cursor is a plain offset.
func (s *Server) paged(w http.ResponseWriter, r *http.Request, items []any) {
	s.writePage(w, r, items, r.URL.Path)
}

// pagedV1 links the next page as REST v1 does: relative to /wiki.
func (s *Server) pagedV1(w http.ResponseWriter, r *http.Request, items []any) {
	s.writePage(w, r, items, strings.TrimPrefix(r.URL.Path, "/wiki"))
}

func (s *Server) writePage(w http.ResponseWriter, r *http.Request, items []any, link string) {
	limit := s.PageLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l < limit {
		limit = l
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(off+limit, len(items))
	resp := map[string]any{"results": items[min(off, len(items)):end], "_links": map[string]any{}}
	if end < len(items) {
		q := r.URL.Query()
		q.Set("cursor", strconv.Itoa(end))
		resp["_links"] = map[string]any{"next": link + "?" + q.Encode()}
	}
	writeJSON(w, 200, resp)
}
```

7. Replace `getSpaces`:

```go
// getSpaces: keys is a comma list; without status, every status is returned.
func (s *Server) getSpaces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var keys map[string]bool
	if k := q.Get("keys"); k != "" {
		keys = map[string]bool{}
		for _, key := range strings.Split(k, ",") {
			keys[key] = true
		}
	}
	var sps []*Space
	for _, sp := range s.spaces {
		if (keys == nil || keys[sp.Key]) && (q.Get("type") == "" || sp.Type == q.Get("type")) &&
			(q.Get("status") == "" || sp.Status == q.Get("status")) {
			sps = append(sps, sp)
		}
	}
	sort.Slice(sps, func(i, j int) bool { return sps[i].Key < sps[j].Key })
	var items []any
	for _, sp := range sps {
		home := ""
		for _, p := range s.pages {
			if p.SpaceID == sp.ID && p.ParentID == "" && (home == "" || p.ID < home) {
				home = p.ID
			}
		}
		items = append(items, map[string]any{"id": sp.ID, "key": sp.Key, "type": sp.Type, "status": sp.Status, "homepageId": home})
	}
	s.paged(w, r, items)
}
```

8. Add `search`:

```go
var cqlRe = regexp.MustCompile(`^type = (comment|attachment) AND lastmodified >= now\("-(\d+)m"\)(?: AND space in \(([^)]*)\))?$`)

// search serves the two CQL shapes gfs sends (site clone spec §5.3).
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m := cqlRe.FindStringSubmatch(q.Get("cql"))
	if m == nil || q.Get("expand") != "container" {
		fail(w, 400, "unsupported search: "+q.Get("cql"))
		return
	}
	mins, _ := strconv.Atoi(m[2])
	since := s.clock().Add(-time.Duration(mins) * time.Minute).UTC().Format(stampLayout)
	inScope := func(string) bool { return true }
	if m[3] != "" {
		ids := map[string]bool{}
		for _, k := range strings.Split(m[3], ",") {
			if sp, ok := s.spaces[strings.Trim(strings.TrimSpace(k), `"`)]; ok {
				ids[sp.ID] = true
			}
		}
		inScope = func(pageID string) bool { p, ok := s.pages[pageID]; return ok && ids[p.SpaceID] }
	}
	type hit struct{ id, page, at string }
	var hits []hit
	if m[1] == "comment" {
		for _, c := range s.comments {
			at := c.UpdatedAt
			if at == "" {
				at = c.CreatedAt
			}
			hits = append(hits, hit{c.ID, c.PageID, at})
		}
	} else {
		for _, a := range s.attachments {
			hits = append(hits, hit{a.ID, a.PageID, a.CreatedAt})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].id < hits[j].id })
	var items []any
	for _, h := range hits {
		if h.at >= since && inScope(h.page) {
			items = append(items, map[string]any{"id": h.id, "type": m[1], "container": map[string]any{"id": h.page, "type": "page"}})
		}
	}
	s.pagedV1(w, r, items)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/... ./internal/cli/`
Expected: PASS (existing tests only use `AddSpace` and default timestamps).

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence/cftest
git commit -m "cftest: space types and status, clock, content search"
```

---

### Task 2: `paginate` follows REST v1 links

**Files:**
- Modify: `internal/adapter/confluence/client.go` (`paginate`)
- Test: `internal/adapter/confluence/client_test.go`

**Interfaces:**
- Consumes: Task 1 `search` endpoint, `Server.Clock`, `PageLimit`.
- Produces: `client.paginate` works for v1 endpoints whose `_links.next` lacks `/wiki`.

- [ ] **Step 1: Write the failing test** (append to `client_test.go`; add `"net/url"` and `"time"` imports if missing)

```go
func TestPaginateV1Links(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.Clock = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
	s.PageLimit = 1
	s.AddSpace("ENG", "100")
	s.AddPage(cftest.Page{ID: "1", Title: "A", SpaceID: "100"})
	s.AddComment(cftest.Comment{ID: "c1", PageID: "1", CreatedAt: "2026-09-29T11:59:00.000Z"})
	s.AddComment(cftest.Comment{ID: "c2", PageID: "1", CreatedAt: "2026-09-29T11:59:00.000Z"})
	c := newClient(target{base: s.URL, email: "me@x.com", token: "t"})
	n := 0
	cql := url.QueryEscape(`type = comment AND lastmodified >= now("-5m")`)
	err := c.paginate(bg, "/wiki/rest/api/content/search?limit=1&expand=container&cql="+cql, func(json.RawMessage) error { n++; return nil })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/adapter/confluence -run TestPaginateV1Links`
Expected: FAIL (the second request goes to `/rest/api/…` and gets 404).

- [ ] **Step 3: Implement** in `paginate`, replace `path = resp.Links.Next` with:

```go
		path = resp.Links.Next
		if path != "" && !strings.HasPrefix(path, "/wiki/") {
			path = "/wiki" + path // REST v1 links are relative to the /wiki context
		}
```

- [ ] **Step 4: Run** `go test ./internal/adapter/confluence/` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence/client.go internal/adapter/confluence/client_test.go
git commit -m "confluence: paginate follows REST v1 next links"
```

---

### Task 3: Remote URL forms and space selection

**Files:**
- Modify: `internal/adapter/confluence/url.go`, `internal/adapter/confluence/adapter.go`, `internal/adapter/adapter.go`, `internal/cli/clone.go`, `internal/adapter/confluence/session.go` (`openSession` only), and the tests that build `target` literals: `client_test.go`, `session_test.go`, `transfer_test.go`, `examples_test.go`
- Create: `internal/adapter/confluence/url_test.go`

**Interfaces:**
- Produces:
  - `type selection struct{ keys []string; typ string; exclude []string }` — `typ` is `"global"|"personal"|"all"`, empty when `keys` is set
  - `target` loses `space`, gains `sel selection`
  - `func normalize(u *url.URL) (*url.URL, error)` — canonical `confluence://[user@]host?base=…&filter=…&type=…&exclude=…` (fixed order, commas unescaped)
  - `func parseSelection(q url.Values) (selection, error)`
  - `adapter.Normalizer` interface: `Normalize(u *url.URL) (string, error)`
  - `(*Adapter).Normalize`, `(*Adapter).DefaultDir` (single key → lower-cased key; else the host's first label)

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/confluence/url_test.go`:

```go
package confluence

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want, err string }{
		{"site", "confluence://acme.atlassian.net", "confluence://acme.atlassian.net", ""},
		{"site slash", "confluence://acme.atlassian.net/", "confluence://acme.atlassian.net", ""},
		{"path key", "confluence://acme.atlassian.net/HF", "confluence://acme.atlassian.net?filter=HF", ""},
		{"personal key", "confluence://acme.atlassian.net/~jan", "confluence://acme.atlassian.net?filter=~jan", ""},
		{"user and params", "confluence://me%40x.com@acme.atlassian.net?exclude=OLD,~jan&type=all", "confluence://me%40x.com@acme.atlassian.net?type=all&exclude=OLD,~jan", ""},
		{"filter list", "confluence://acme.atlassian.net?filter=HF,ENG", "confluence://acme.atlassian.net?filter=HF,ENG", ""},
		{"base kept", "confluence://acme.atlassian.net/HF?base=http://127.0.0.1:9", "confluence://acme.atlassian.net?base=http%3A%2F%2F127.0.0.1%3A9&filter=HF", ""},
		{"browser page", "confluence:https://acme.atlassian.net/wiki/spaces/HF/pages/123/Title", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser overview", "confluence:https://acme.atlassian.net/wiki/spaces/HF/overview", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser with query and fragment", "confluence:https://acme.atlassian.net/wiki/spaces/HF/pages/1/T?atlOrigin=abc#Section", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser personal", "confluence:https://acme.atlassian.net/wiki/spaces/~jan/overview", "confluence://acme.atlassian.net?filter=~jan", ""},
		{"browser site", "confluence:https://acme.atlassian.net/wiki/home", "confluence://acme.atlassian.net", ""},
		{"browser not wiki", "confluence:https://acme.atlassian.net/jira/x", "", "want confluence://<site>"},
		{"deep path", "confluence://acme.atlassian.net/a/b", "", "want confluence://<site>"},
		{"wiki path", "confluence://acme.atlassian.net/wiki/HF", "", "want confluence://<site>"},
		{"no host", "confluence:///HF", "", "want confluence://<site>"},
		{"key twice", "confluence://acme.atlassian.net/HF?filter=ENG", "", "not both"},
		{"unknown param", "confluence://acme.atlassian.net?filtr=HF", "", `unknown parameter "filtr"`},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := normalize(u)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%s: got %v %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestParseSelection(t *testing.T) {
	cases := []struct {
		q    string
		want selection
		err  string
	}{
		{"", selection{typ: "global"}, ""},
		{"type=personal", selection{typ: "personal"}, ""},
		{"type=all&exclude=OLD,~jan", selection{typ: "all", exclude: []string{"OLD", "~jan"}}, ""},
		{"filter=HF,ENG", selection{keys: []string{"HF", "ENG"}}, ""},
		{"filter=HF&exclude=HF", selection{keys: []string{"HF"}, exclude: []string{"HF"}}, ""},
		{"filter=HF&type=global", selection{}, "cannot be combined"},
		{"type=team", selection{}, "want global, personal or all"},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		got, err := parseSelection(q)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.q, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v %v", c.q, got, err)
		}
	}
}

func TestAdapterNormalizeAndDefaultDir(t *testing.T) {
	a := &Adapter{}
	cases := []struct{ in, norm, dir string }{
		{"confluence://acme.atlassian.net/HF", "confluence://acme.atlassian.net?filter=HF", "hf"},
		{"confluence:https://acme.atlassian.net/wiki/spaces/~jan/overview", "confluence://acme.atlassian.net?filter=~jan", "~jan"},
		{"confluence://acme.atlassian.net", "confluence://acme.atlassian.net", "acme"},
		{"confluence://acme.atlassian.net?filter=HF,ENG", "confluence://acme.atlassian.net?filter=HF,ENG", "acme"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.in)
		got, err := a.Normalize(u)
		if err != nil || got != c.norm {
			t.Errorf("%s: Normalize %q %v", c.in, got, err)
		}
		if d := a.DefaultDir(u); d != c.dir {
			t.Errorf("%s: DefaultDir %q, want %q", c.in, d, c.dir)
		}
	}
	u, _ := url.Parse("confluence://acme.atlassian.net?filter=HF&type=all")
	if _, err := a.Normalize(u); err == nil {
		t.Fatal("type with filter must fail Normalize")
	}
}
```

Update `TestParseTarget` in `client_test.go`:
- the success check `got.space != "ENG"` becomes `!reflect.DeepEqual(got.sel, selection{keys: []string{"ENG"}})` (add `"reflect"` import);
- the `"bad path"` row becomes `{"bad path", "confluence://acme.atlassian.net/a/b", nil, both, fakeLookup{}, "", "", "", "want confluence://<site>"}`;
- the `"no identity"` row's message becomes `"no identity for acme.atlassian.net: run gfs auth set <email> --host acme.atlassian.net, or put the email in the URL (confluence://me%40x.com@acme.atlassian.net)"`.

Replace `space: "ENG"` in `target{…}` literals: `client_test.go:99`, `transfer_test.go:22` drop the field; `session_test.go:26` and `:223` use `sel: selection{keys: []string{"ENG"}}`. In `examples_test.go` replace both uses of `tg.space` with a local `key`:

```go
	if len(tg.sel.keys) != 1 {
		t.Fatalf("GFS_EXAMPLES_URL must name exactly one space")
	}
	key := tg.sel.keys[0]
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/`
Expected: FAIL to compile (`normalize`, `selection`, `parseSelection`, `Normalize` undefined).

- [ ] **Step 3: Implement**

`internal/adapter/adapter.go`, after `Identified`:

```go
// Normalizer is implemented by adapters that accept several spellings of a
// remote. Clone records the canonical one as [remote] url.
type Normalizer interface {
	Normalize(u *url.URL) (string, error)
}
```

`internal/adapter/confluence/url.go` — replace `target` and the head of `parseTarget`:

```go
// selection is which spaces a working tree holds (site clone spec §2.2).
type selection struct {
	keys    []string // filter: exactly these spaces
	typ     string   // global, personal or all; "" when keys is set
	exclude []string
}

type target struct {
	base, host, email, token string
	sel                      selection
}

const urlForms = "want confluence://<site>[?filter=K1,K2&type=global|personal|all&exclude=K1,K2], confluence://<site>/<KEY>, or confluence:https://<site>/wiki/spaces/<KEY>/…"

// normalize rewrites every accepted remote form as
// confluence://[user@]<site>?<parameters> (site clone spec §2.1).
func normalize(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), urlForms) }
	out := &url.URL{Scheme: "confluence"}
	q := url.Values{}
	if u.Opaque != "" {
		// a browser URL: only /wiki/spaces/<KEY> counts; its own query (atlOrigin=…) is not ours
		in, err := url.Parse(u.Opaque)
		if err != nil || (in.Scheme != "https" && in.Scheme != "http") || in.Host == "" {
			return bad()
		}
		segs := strings.Split(strings.Trim(in.Path, "/"), "/")
		if segs[0] != "wiki" {
			return bad()
		}
		for i := 1; i+1 < len(segs); i++ {
			if segs[i] == "spaces" {
				q.Set("filter", segs[i+1])
				break
			}
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User, out.Host = in.User, in.Host
	} else {
		if u.Host == "" {
			return bad()
		}
		for k, vs := range u.Query() {
			switch k {
			case "base", "filter", "type", "exclude":
				q.Set(k, vs[len(vs)-1])
			default:
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
		}
		switch key := strings.Trim(u.Path, "/"); {
		case key == "":
		case strings.Contains(key, "/"):
			return bad()
		case q.Has("filter"):
			return nil, fmt.Errorf("bad remote %q: name the space in the path or in filter, not both", u.String())
		default:
			q.Set("filter", key)
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q)
	return out, nil
}

// encodeQuery writes the parameters in a fixed order and leaves commas readable.
func encodeQuery(q url.Values) string {
	var parts []string
	for _, k := range []string{"base", "filter", "type", "exclude"} {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v), "%2C", ","))
		}
	}
	return strings.Join(parts, "&")
}

func parseSelection(q url.Values) (selection, error) {
	split := func(v string) []string {
		var out []string
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	sel := selection{keys: split(q.Get("filter")), typ: q.Get("type"), exclude: split(q.Get("exclude"))}
	switch {
	case len(sel.keys) > 0 && sel.typ != "":
		return selection{}, errors.New("type and filter cannot be combined: filter already names the spaces")
	case len(sel.keys) > 0:
	case sel.typ == "":
		sel.typ = "global"
	case sel.typ != "global" && sel.typ != "personal" && sel.typ != "all":
		return selection{}, fmt.Errorf("type=%s: want global, personal or all", sel.typ)
	}
	return sel, nil
}

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	sel, err := parseSelection(n.Query())
	if err != nil {
		return target{}, fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	t := target{host: n.Hostname(), sel: sel, base: "https://" + n.Host}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := n.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	email, err := resolveEmail(n, cfg, getenv, lk)
	if err != nil {
		return target{}, err
	}
	if email == "" {
		return target{}, fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (confluence://me%%40x.com@%s)", t.host, t.host, t.host)
	}
	// … the token lookup below is unchanged
```

`internal/adapter/confluence/adapter.go` — replace the `DefaultDir` one-liner and add `Normalize`:

```go
func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	if _, err := parseSelection(n.Query()); err != nil {
		return "", fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	return n.String(), nil
}

// DefaultDir is the space folder for a one-space selection, else the site name.
func (*Adapter) DefaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "confluence"
	}
	if sel, err := parseSelection(n.Query()); err == nil && len(sel.keys) == 1 {
		return strings.ToLower(sel.keys[0])
	}
	return strings.SplitN(n.Hostname(), ".", 2)[0]
}
```

`internal/cli/clone.go` — record the normalised URL (add `"net/url"` import):

```go
			ad, u, err := adapter.ForURL(args[0])
			if err != nil {
				return usage("%v", err)
			}
			raw := args[0]
			if n, ok := ad.(adapter.Normalizer); ok {
				if raw, err = n.Normalize(u); err != nil {
					return usage("%v", err)
				}
				if u, err = url.Parse(raw); err != nil {
					return err
				}
			}
			dir := ad.DefaultDir(u)
			if len(args) == 2 {
				dir = args[1]
			}
			sess, err := ad.Open(cmd.Context(), u, nil)
			if err != nil {
				return err
			}
			defer sess.Close()
			_, err = engine.Clone(cmd.Context(), ad, sess, raw, dir, cmd.OutOrStdout())
			return err
```

`session.go` `openSession` — interim, until Task 4 makes the session multi-space. Replace `t.space` with a single key:

```go
func openSession(ctx context.Context, t target) (*session, error) {
	if len(t.sel.keys) != 1 {
		return nil, errors.New("a working tree with several spaces needs the multi-space session") // replaced in Task 4
	}
	key := t.sel.keys[0]
	s := &session{c: newClient(t), spaceKey: key, spaceDir: strings.ToLower(key), names: map[string]string{}}
	// … the rest as before, with t.space replaced by key
```

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS. The existing e2e tests clone `confluence://acme.atlassian.net/ENG?base=…`, which normalises to one key.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter internal/cli/clone.go
git commit -m "confluence: remote URL forms, space selection, clone records the normalised URL"
```

---

### Task 4: Multi-space session: opening, page trees, listing, fetch

**Files:**
- Modify: `internal/adapter/confluence/session.go`, `internal/adapter/confluence/paths.go`, `internal/adapter/confluence/adapter.go` (package doc)
- Create: `internal/adapter/confluence/site_test.go`

**Interfaces:**
- Consumes: `selection`, `target.sel` (Task 3); `cftest.PutSpace`, `TakeRequests` (Task 1).
- Produces:
  - `type space struct{ key, id, dir string; loaded bool }`
  - `session` fields: `c *client; sel selection; spaces map[string]*space` (by dir); `byID map[string]*space` (by space id); `tree map[string]pageRef` (never nil); `names map[string]string`; `now func() time.Time`. Fields `spaceKey`, `spaceID`, `spaceDir` are removed.
  - `pageRef{ID, Title, Parent, Space string; Version int}` (`Space` is the space id)
  - `func (s *session) load(ctx context.Context, sp *space) error`
  - `func (s *session) loadAll(ctx context.Context) error`
  - `func (s *session) sortedSpaces() []*space`
  - `func (s *session) paths(sp *space) map[string]string`
  - `func (s *session) dirList() string` (e.g. `"eng, ops"`)
  - `func (s *session) spaceFor(p string) (*space, error)`
  - `Fetch` returns an error wrapping `adapter.ErrNotFound` for a page in an unselected space

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/confluence/site_test.go`:

```go
package confluence

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

// site builds ENG and OPS (global, both with a "Home"), ~jan (personal) and OLD (archived).
func site(t *testing.T) *cftest.Server {
	t.Helper()
	s := cftest.New()
	t.Cleanup(s.Close)
	s.AddSpace("ENG", "100")
	s.AddSpace("OPS", "200")
	s.PutSpace(cftest.Space{Key: "~jan", ID: "300", Type: "personal"})
	s.PutSpace(cftest.Space{Key: "OLD", ID: "400", Status: "archived"})
	s.AddPage(cftest.Page{ID: "81001", Title: "Home", SpaceID: "100", Storage: "<p>eng</p>"})
	s.AddPage(cftest.Page{ID: "81002", Title: "Architecture", ParentID: "81001", SpaceID: "100", Storage: "<p>a</p>"})
	s.AddPage(cftest.Page{ID: "82001", Title: "Home", SpaceID: "200", Storage: "<p>ops</p>"})
	s.AddPage(cftest.Page{ID: "83001", Title: "Jan", SpaceID: "300", Storage: "<p>jan</p>"})
	s.AddPage(cftest.Page{ID: "84001", Title: "Old", SpaceID: "400", Storage: "<p>old</p>"})
	return s
}

func open(t *testing.T, s *cftest.Server, sel selection) *session {
	t.Helper()
	sess, err := openSession(bg, target{base: s.URL, email: "me@x.com", token: "t", sel: sel})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func dirs(sess *session) string {
	var out []string
	for _, sp := range sess.sortedSpaces() {
		out = append(out, sp.dir)
	}
	return strings.Join(out, ",")
}

func TestOpenSelection(t *testing.T) {
	s := site(t)
	cases := []struct {
		sel  selection
		want string
	}{
		{selection{typ: "global"}, "eng,ops"},
		{selection{typ: "personal"}, "~jan"},
		{selection{typ: "all"}, "eng,ops,~jan"},
		{selection{typ: "global", exclude: []string{"OPS", "NOPE"}}, "eng"},
		{selection{keys: []string{"OLD", "~jan"}}, "old,~jan"},
	}
	for _, c := range cases {
		if got := dirs(open(t, s, c.sel)); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.sel, got, c.want)
		}
	}
	_, err := openSession(bg, target{base: s.URL, email: "me@x.com", token: "t", sel: selection{keys: []string{"ENG", "NOPE"}}})
	if err == nil || !strings.Contains(err.Error(), "space NOPE not found") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenPagesThroughSpaces(t *testing.T) {
	s := site(t)
	s.PageLimit = 1
	if got := dirs(open(t, s, selection{typ: "all"})); got != "eng,ops,~jan" {
		t.Fatal(got)
	}
}

func TestListSite(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	l, err := sess.List(bg, "")
	if err != nil || !l.Full {
		t.Fatalf("%+v %v", l, err)
	}
	var paths []string
	for _, r := range l.Resources {
		if r.Root == nil {
			t.Fatalf("empty cursor must fetch every page: %s", r.Path)
		}
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, "|") != "eng/Home.xml|eng/Home/Architecture.xml|ops/Home.xml" {
		t.Fatal(paths)
	}
}

var spacePages = regexp.MustCompile(`^GET /wiki/api/v2/spaces/(\d+)/pages$`)

func loadedSpaces(reqs []string) string {
	var out []string
	for _, r := range reqs {
		if m := spacePages.FindStringSubmatch(r); m != nil {
			out = append(out, m[1])
		}
	}
	return strings.Join(out, ",")
}

func TestFetchLoadsOnlyItsSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	s.TakeRequests()
	r, err := sess.Fetch(bg, "82001")
	if err != nil || r.Path != "ops/Home.xml" {
		t.Fatalf("%+v %v", r, err)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "200" {
		t.Fatalf("loaded spaces %q, want only 200", got)
	}
	if _, err := sess.Fetch(bg, "81002"); err != nil {
		t.Fatal(err)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "100" {
		t.Fatalf("second fetch loaded %q", got)
	}
}

func TestFetchOutsideSelection(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if _, err := sess.Fetch(bg, "84001"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("page in an unselected space: want ErrNotFound, got %v", err)
	}
}

func TestPageMovedBetweenSpaces(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if r, _ := sess.Fetch(bg, "81002"); r.Path != "eng/Home/Architecture.xml" {
		t.Fatal(r.Path)
	}
	s.EditPage("81002", func(p *cftest.Page) { p.SpaceID, p.ParentID = "200", "82001" })
	sess = open(t, s, selection{typ: "global"})
	l, err := sess.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Resources {
		if r.ID == "81002" && r.Path == "ops/Home/Architecture.xml" {
			return
		}
	}
	t.Fatalf("moved page not under ops/: %+v", l.Resources)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence -run 'TestOpen|TestListSite|TestFetch|TestPageMoved'`
Expected: FAIL to compile (`sortedSpaces`, `space` type undefined) or FAIL at the interim single-space guard.

- [ ] **Step 3: Implement**

`paths.go`:

```go
type pageRef struct {
	ID, Title, Parent string
	Space             string // space id
	Version           int
}
```

`adapter.go` package doc: `// Package confluence mirrors the spaces of a Confluence Cloud site as page trees of XML files.`

`session.go` — add `"time"` to imports; replace the struct, `openSession`, `loadTree`, `paths`, `List`, and the head of `Fetch`:

```go
type space struct {
	key, id, dir string // "HF", "98307", "hf"
	loaded       bool   // its pages are in tree
}

type session struct {
	c      *client
	sel    selection
	spaces map[string]*space  // by dir
	byID   map[string]*space  // by space id
	tree   map[string]pageRef // pages of the loaded spaces
	names  map[string]string
	now    func() time.Time
}

func openSession(ctx context.Context, t target) (*session, error) {
	s := &session{c: newClient(t), sel: t.sel, spaces: map[string]*space{}, byID: map[string]*space{},
		tree: map[string]pageRef{}, names: map[string]string{}, now: time.Now}
	if err := s.resolveSpaces(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveSpaces turns the selection into spaces (site clone spec §4.1).
func (s *session) resolveSpaces(ctx context.Context) error {
	path := "/wiki/api/v2/spaces?limit=250"
	if len(s.sel.keys) > 0 {
		esc := make([]string, len(s.sel.keys))
		for i, k := range s.sel.keys {
			esc[i] = url.QueryEscape(k)
		}
		path += "&keys=" + strings.Join(esc, ",") // no status: a named archived space is included
	} else {
		path += "&status=current"
		if s.sel.typ != "all" {
			path += "&type=" + s.sel.typ
		}
	}
	excluded := map[string]bool{}
	for _, k := range s.sel.exclude {
		excluded[k] = true
	}
	err := s.c.paginate(ctx, path, func(raw json.RawMessage) error {
		var a struct{ ID, Key string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if !excluded[a.Key] {
			sp := &space{key: a.Key, id: a.ID, dir: strings.ToLower(a.Key)}
			s.spaces[sp.dir], s.byID[sp.id] = sp, sp
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for _, k := range s.sel.keys {
		if s.spaces[strings.ToLower(k)] == nil && !excluded[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("space %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *session) sortedSpaces() []*space {
	out := make([]*space, 0, len(s.spaces))
	for _, sp := range s.spaces {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out
}

func (s *session) dirList() string {
	var ds []string
	for _, sp := range s.sortedSpaces() {
		ds = append(ds, sp.dir)
	}
	return strings.Join(ds, ", ")
}

// load reads sp's page tree once per session (site clone spec §4.2).
func (s *session) load(ctx context.Context, sp *space) error {
	if sp.loaded {
		return nil
	}
	fresh := map[string]pageRef{}
	err := s.c.paginate(ctx, "/wiki/api/v2/spaces/"+sp.id+"/pages?limit=250", func(raw json.RawMessage) error {
		var p apiPage
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		fresh[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
		return nil
	})
	if err != nil {
		return fmt.Errorf("space %s: %w", sp.key, err)
	}
	for id, r := range s.tree {
		if r.Space == sp.id {
			delete(s.tree, id)
		}
	}
	for id, r := range fresh {
		s.tree[id] = r
	}
	sp.loaded = true
	return nil
}

func (s *session) loadAll(ctx context.Context) error {
	for _, sp := range s.sortedSpaces() {
		if err := s.load(ctx, sp); err != nil {
			return err
		}
	}
	return nil
}

// paths maps sp's pages to working paths; a path depends only on its own space.
func (s *session) paths(sp *space) map[string]string {
	var refs []pageRef
	for _, r := range s.tree {
		if r.Space == sp.id {
			refs = append(refs, r)
		}
	}
	return pagePaths(sp.dir, refs)
}

func (s *session) List(ctx context.Context, _ string) (adapter.Listing, error) {
	if err := s.loadAll(ctx); err != nil {
		return adapter.Listing{}, err
	}
	ids := make([]string, 0, len(s.tree))
	for id := range s.tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	l := adapter.Listing{Full: true}
	for _, id := range ids {
		r, err := s.Fetch(ctx, id)
		if errors.Is(err, adapter.ErrNotFound) {
			continue // deleted while listing
		}
		if err != nil {
			return adapter.Listing{}, err
		}
		l.Resources = append(l.Resources, *r)
	}
	return l, nil
}

func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	var p apiPage
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+id+"?body-format=storage", nil, &p); err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			delete(s.tree, id)
		}
		return nil, err
	}
	sp := s.byID[p.SpaceID]
	if sp == nil {
		delete(s.tree, id)
		return nil, fmt.Errorf("%w: page %s is in a space outside this tree", adapter.ErrNotFound, id)
	}
	if err := s.load(ctx, sp); err != nil {
		return nil, err
	}
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
	// … labels, comments, attachments and pageNode unchanged …
	return &adapter.Resource{ID: p.ID, Version: strconv.Itoa(p.Version.Number), Path: s.paths(sp)[p.ID],
		By: s.name(ctx, p.Version.AuthorID), At: p.Version.CreatedAt, Root: root}, nil
}
```

Add `spaceFor` (Task 5 builds on it):

```go
// spaceFor maps a working path's first folder to its space (site clone spec §4.5).
func (s *session) spaceFor(p string) (*space, error) {
	dir, _, ok := strings.Cut(p, "/")
	if !ok {
		return nil, fmt.Errorf("pages must live in a space folder (%s)", s.dirList())
	}
	sp := s.spaces[dir]
	if sp == nil {
		return nil, fmt.Errorf("no space %q in this tree; gfs does not create spaces (spaces: %s)", dir, s.dirList())
	}
	return sp, nil
}
```

Delete `loadTree` and the old `paths()`. The commit side still uses the removed fields; bridge it so this task compiles and behaves as before for one space (Task 5 replaces the bridge):
- `Check` and `Apply`: replace each `if s.tree == nil { … loadTree … }` block with `if err := s.loadAll(ctx); err != nil {` followed by the same error return.
- `parentFor`: replace the `spaceDir` prefix check with `if _, err := s.spaceFor(p); err != nil { return "", err }`.
- The two `"title %q is already used in space %s"` messages: replace `s.spaceKey` with `s.keyOf(req.Local.Path)`, a bridge helper:

```go
// keyOf is a bridge until Task 5: the space key of a working path, or "".
func (s *session) keyOf(p string) string {
	if sp, err := s.spaceFor(p); err == nil {
		return sp.key
	}
	return ""
}
```

- `create`: after `parentFor` succeeds, `sp, _ := s.spaceFor(req.Local.Path)`; send `"spaceId": sp.id`; record `s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}`.

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS, including the existing single-space `session_test.go` (its `space` helper uses `selection{keys: []string{"ENG"}}`).

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: one session serves every selected space; page trees load per space"
```

---

### Task 5: Commit across spaces

**Files:**
- Modify: `internal/adapter/confluence/session.go` (`titleTaken`, `parentFor`, `pagePut`, `Check`, `Apply`, `create`; new `prepare`; delete the bridge `keyOf`)
- Test: `internal/adapter/confluence/site_test.go`

**Interfaces:**
- Consumes: Task 4 `space`, `load`, `dirList`, `sortedSpaces`, `spaceFor`.
- Produces:
  - `func (s *session) prepare(ctx context.Context, req adapter.ApplyRequest) error`
  - `func (s *session) titleTaken(sp *space, title, except string) bool`
  - `func (s *session) parentFor(p string, idByPath func(string) (string, bool)) (*space, string, error)`

- [ ] **Step 1: Write the failing tests** (append to `site_test.go`; add `"github.com/KrzysztofBogdan/gitfs/internal/xmltree"` import)

```go
var siteIDs = ids(map[string]string{"eng/Home.xml": "81001", "eng/Home/Architecture.xml": "81002", "ops/Home.xml": "82001"})

func pageRoot(t *testing.T, title string) *xmltree.Node {
	t.Helper()
	root, err := xmltree.ParseString(`<page><title>` + title + `</title><body type="application/xhtml+xml"><p>x</p></body></page>`)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createReq(t *testing.T, path, title string) adapter.ApplyRequest {
	return adapter.ApplyRequest{Local: &adapter.Resource{Path: path, Root: pageRoot(t, title)},
		Actions: []adapter.Action{{Verb: "create"}}, IDByPath: siteIDs}
}

func TestCreateInEachSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	for path, space := range map[string]string{"ops/Home/Deploy.xml": "200", "eng/Home/Runbook.xml": "100"} {
		res := sess.Apply(bg, createReq(t, path, strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".xml")))
		if res[0].Err != nil {
			t.Fatalf("%s: %+v", path, res)
		}
		if p, _ := s.Page(res[0].ID); p.SpaceID != space {
			t.Fatalf("%s: created in space %s", path, p.SpaceID)
		}
	}
}

func TestTitleTakenPerSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	if res := sess.Check(bg, createReq(t, "ops/Home/Architecture.xml", "Architecture")); res[0].Err != nil {
		t.Fatalf("title used only in ENG must be free in OPS: %+v", res)
	}
	res := sess.Check(bg, createReq(t, "eng/Home/Arch2.xml", "Architecture"))
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `"Architecture" is already used in space ENG`) {
		t.Fatalf("%+v", res)
	}
}

func TestCreateUnknownSpaceFolder(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	for _, check := range []bool{true, false} {
		req := createReq(t, "new/Page.xml", "Page")
		var res []adapter.Result
		if check {
			res = sess.Check(bg, req)
		} else {
			res = sess.Apply(bg, req)
		}
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), `no space "new" in this tree; gfs does not create spaces (spaces: eng, ops)`) {
			t.Fatalf("check=%v: %+v", check, res)
		}
	}
}

func TestMoveBetweenSpacesRefused(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	base, err := sess.Fetch(bg, "81002")
	if err != nil {
		t.Fatal(err)
	}
	local := &adapter.Resource{ID: base.ID, Path: "ops/Home/Architecture.xml", Root: base.Root.Clone()}
	req := adapter.ApplyRequest{Local: local, Base: base, Lock: base.Version, IDByPath: siteIDs,
		Actions: []adapter.Action{{Verb: "move", From: base.Path, To: local.Path}}}
	for _, res := range [][]adapter.Result{sess.Check(bg, req), sess.Apply(bg, req)} {
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "moving pages between spaces is not supported") {
			t.Fatalf("%+v", res)
		}
	}
	if p, _ := s.Page("81002"); p.SpaceID != "100" || p.Version != 1 {
		t.Fatalf("page changed: %+v", p)
	}
}

func TestApplyLoadsOnlyTouchedSpace(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	base, _ := sess.Fetch(bg, "82001")
	sess = open(t, s, selection{typ: "global"})
	s.TakeRequests()
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	res := sess.Apply(bg, adapter.ApplyRequest{Local: local, Base: base, Lock: base.Version, IDByPath: siteIDs,
		Actions: []adapter.Action{{Verb: "update", Group: "body"}}})
	if res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	if got := loadedSpaces(s.TakeRequests()); got != "200" {
		t.Fatalf("loaded %q, want only 200", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence -run 'TestCreateInEachSpace|TestTitleTaken|TestCreateUnknown|TestMoveBetween|TestApplyLoads'`
Expected: FAIL in `TestTitleTakenPerSpace` (taken in OPS), `TestMoveBetweenSpacesRefused` (the move is applied) and `TestApplyLoadsOnlyTouchedSpace` (both spaces load). `TestCreateInEachSpace` and `TestCreateUnknownSpaceFolder` may already pass through Task 4's bridge; they pin behaviour this task must keep.

- [ ] **Step 3: Implement** in `session.go`, replacing Task 4's bridge (delete `keyOf`):

```go
// prepare loads the page trees of the spaces req touches, and only those.
func (s *session) prepare(ctx context.Context, req adapter.ApplyRequest) error {
	var paths []string
	if req.Local != nil {
		paths = append(paths, req.Local.Path)
	}
	if req.Base != nil {
		paths = append(paths, req.Base.Path)
	}
	for _, a := range req.Actions {
		if a.Verb == "move" {
			paths = append(paths, a.From, a.To)
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		sp, err := s.spaceFor(p)
		if err != nil {
			return err
		}
		if err := s.load(ctx, sp); err != nil {
			return err
		}
	}
	return nil
}

// titleTaken reports whether another page in sp already has title.
func (s *session) titleTaken(sp *space, title, except string) bool {
	for _, r := range s.tree {
		if r.Space == sp.id && r.Title == title && r.ID != except {
			return true
		}
	}
	return false
}

func (s *session) parentFor(p string, idByPath func(string) (string, bool)) (*space, string, error) {
	sp, err := s.spaceFor(p)
	if err != nil {
		return nil, "", err
	}
	parent := parentPath(p)
	if parent == "" {
		return sp, "", nil
	}
	id, ok := idByPath(parent)
	if !ok {
		return nil, "", fmt.Errorf("parent page %s is not on the remote yet; commit it first", parent)
	}
	return sp, id, nil
}
```

In `pagePut`, the move loop body becomes:

```go
		from, err := s.spaceFor(a.From)
		if err != nil {
			return "", "", err
		}
		to, err := s.spaceFor(a.To)
		if err != nil {
			return "", "", err
		}
		if from != to {
			return "", "", errors.New("moving pages between spaces is not supported")
		}
		if parentPath(a.To) == "" {
			return "", "", errors.New("moving a page to the space root is not supported; move it under a page")
		}
		if _, parent, err = s.parentFor(a.To, req.IDByPath); err != nil {
			return "", "", err
		}
		if !titleChanged && sanitize(title) != baseName(a.To) {
			title = baseName(a.To)
		}
```

(`from, err :=` declares a loop-local `err` that shadows the named result; that is fine because every error path returns explicit values. `parent` is still the named result, so assign it with `=`.)

`Check`: first lines become

```go
	if err := s.prepare(ctx, req); err != nil {
		return []adapter.Result{{Action: req.Actions[0], Err: err}}
	}
```

and the create and move cases:

```go
		case a.Verb == "create":
			title := titleOf(req.Local.Root)
			if title == "" {
				title = baseName(req.Local.Path)
			}
			if sp, _, err := s.parentFor(req.Local.Path, req.IDByPath); err != nil {
				res.Err = err
			} else if s.titleTaken(sp, title, "") {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, sp.key)
			}
		case a.Verb == "move" || (a.Verb == "update" && a.Group == "title"):
			title, _, err := s.pagePut(req, []int{i})
			if err != nil {
				res.Err = err
			} else if sp, _ := s.spaceFor(req.Local.Path); s.titleTaken(sp, title, req.Local.ID) {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, sp.key)
			}
```

`Apply`: first lines become

```go
	if err := s.prepare(ctx, req); err != nil {
		return []adapter.Result{{Action: req.Actions[0], Err: err, Code: codeOf(err)}}
	}
```

and after a successful PUT also record the version: `ref.Version = lock + 1`.

`create`:

```go
	sp, parent, err := s.parentFor(req.Local.Path, req.IDByPath)
	if err != nil {
		return fail(err)
	}
	// … title as before …
	body := map[string]any{"spaceId": sp.id, "status": "current", "title": title,
		"body": storageBody(storageOf(req.Local.Root.Child("body")))}
	// … POST as before …
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
```

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS (existing `TestCheckDuplicateTitle` still reports "already used"; `TestCreateUnderParent` still reports "commit it first").

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: commit routes to the file's space; refuse new spaces and moves between spaces"
```

---

### Task 6: Incremental listing

**Files:**
- Create: `internal/adapter/confluence/listing.go`, `internal/adapter/confluence/listing_test.go`
- Modify: `internal/adapter/confluence/session.go` (remove `List`; it moves to `listing.go`)
- Modify: `internal/cli/confluence_e2e_test.go` (step 4 comment gets `CreatedAt: srv.Stamp()`)

**Interfaces:**
- Consumes: Task 4 `loadAll`, `paths`, `tree`, `byID`, `now`; Task 1 `Clock`, `Stamp`, content search; Task 2 v1 pagination.
- Produces:
  - `List(ctx, cursor)`: empty or unreadable cursor → every page fetched; RFC 3339 cursor → stubs plus full resources for pages with changed comments/attachments; always `Full: true` and `Cursor` = start time
  - `func parseCursor(c string) (time.Time, bool)`
  - `func searchWindow(since, now time.Time) int`
  - `const searchOverlap = 10`
  - `func (s *session) changedContainers(ctx context.Context, mins int) (map[string]bool, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/adapter/confluence/listing_test.go`:

```go
package confluence

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

var t0 = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// clocked shares one settable clock between the fake and the session.
func clocked(srv *cftest.Server, sess *session) func(time.Duration) {
	now := t0
	clock := func() time.Time { return now }
	srv.Clock, sess.now = clock, clock
	return func(d time.Duration) { now = now.Add(d) }
}

var pageGet = regexp.MustCompile(`^GET /wiki/api/v2/pages/[^/]+$`)

func bodyFetches(reqs []string) int {
	n := 0
	for _, r := range reqs {
		if pageGet.MatchString(r) {
			n++
		}
	}
	return n
}

func full(l adapter.Listing) map[string]bool {
	m := map[string]bool{}
	for _, r := range l.Resources {
		if r.Root != nil {
			m[r.ID] = true
		}
	}
	return m
}

func TestListCursorAndStubs(t *testing.T) {
	srv, sess := space(t)
	advance := clocked(srv, sess)
	l0, err := sess.List(bg, "")
	if err != nil || l0.Cursor != "2026-09-29T12:00:00Z" || len(full(l0)) != 3 {
		t.Fatalf("%+v %v", l0, err)
	}
	advance(5 * time.Minute)
	srv.TakeRequests()
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil || !l1.Full || len(l1.Resources) != 3 || len(full(l1)) != 0 {
		t.Fatalf("quiet listing must be all stubs: %+v %v", l1, err)
	}
	if n := bodyFetches(srv.TakeRequests()); n != 0 {
		t.Fatalf("%d page fetches on a quiet listing", n)
	}
	for _, r := range l1.Resources {
		if r.ID == "98120" && (r.Version != "1" || r.Path != "eng/Home/Architecture.xml") {
			t.Fatalf("stub %+v", r)
		}
	}
	if l1.Cursor != "2026-09-29T12:05:00Z" {
		t.Fatal(l1.Cursor)
	}
}

func TestListRefetchesPagesWithNewCommentsAndAttachments(t *testing.T) {
	srv, sess := space(t) // filter=ENG: the scoped CQL shape
	advance := clocked(srv, sess)
	l0, _ := sess.List(bg, "")
	advance(2 * time.Minute)
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>new</p>", CreatedAt: srv.Stamp()})
	srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "x.png", CreatedAt: srv.Stamp()})
	advance(3 * time.Minute)
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := full(l1); !got["98120"] || !got["98130"] || got["98001"] {
		t.Fatalf("full resources: %v", got)
	}
}

func TestListIgnoresChangesBeforeWindow(t *testing.T) {
	srv, sess := space(t)
	advance := clocked(srv, sess)
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>old</p>", CreatedAt: t0.Add(-20 * time.Minute).Format("2006-01-02T15:04:05.000Z")})
	l0, _ := sess.List(bg, "")
	advance(5 * time.Minute) // window 15m reaches back to t0-10m
	l1, _ := sess.List(bg, l0.Cursor)
	if len(full(l1)) != 0 {
		t.Fatalf("%v", full(l1))
	}
}

func TestListWholeSiteSearch(t *testing.T) {
	srv := site(t)
	sess := open(t, srv, selection{typ: "global"}) // no filter: the unscoped CQL shape
	advance := clocked(srv, sess)
	l0, _ := sess.List(bg, "")
	srv.AddComment(cftest.Comment{PageID: "82001", CreatedAt: srv.Stamp()})
	srv.AddComment(cftest.Comment{PageID: "84001", CreatedAt: srv.Stamp()}) // archived space: not in the tree
	advance(time.Minute)
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := full(l1); len(got) != 1 || !got["82001"] {
		t.Fatalf("%v", got)
	}
}

func TestListUnreadableCursorListsEverything(t *testing.T) {
	_, sess := space(t)
	l, err := sess.List(bg, "garbage")
	if err != nil || len(full(l)) != 3 {
		t.Fatalf("%v %v", full(l), err)
	}
}

func TestListFailsWhenASpaceFails(t *testing.T) {
	srv := site(t)
	sess := open(t, srv, selection{typ: "global"})
	srv.Fail = map[string]int{"GET /wiki/api/v2/spaces/200/pages": 403}
	_, err := sess.List(bg, t0.Format(time.RFC3339))
	if err == nil || !strings.Contains(err.Error(), "space OPS") {
		t.Fatalf("got %v", err)
	}
}

func TestListSearchFailureSuggestsFull(t *testing.T) {
	srv, sess := space(t)
	srv.Fail = map[string]int{"GET /wiki/rest/api/content/search": 400}
	_, err := sess.List(bg, t0.Format(time.RFC3339))
	if err == nil || !strings.Contains(err.Error(), "gfs pull --full") {
		t.Fatalf("got %v", err)
	}
}

func TestSearchWindow(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{{0, 10}, {30 * time.Second, 11}, {5 * time.Minute, 15}, {5*time.Minute + time.Second, 16}, {-2 * time.Minute, 10}}
	for _, c := range cases {
		if got := searchWindow(t0, t0.Add(c.d)); got != c.want {
			t.Errorf("%v: got %d, want %d", c.d, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence -run 'TestList|TestSearchWindow'`
Expected: FAIL to compile (`searchWindow` undefined); after a stub, `TestListCursorAndStubs` fails (no cursor, full resources).

- [ ] **Step 3: Implement**

Delete `List` from `session.go`. Create `listing.go`:

```go
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// searchOverlap widens every change search, in minutes, for clock skew and
// search indexing delay (site clone spec §5.3).
const searchOverlap = 10

// List reports every page of the selected spaces. With a cursor from an
// earlier List, pages come as stubs (no body) unless a comment or attachment
// on them changed since; pull fetches the stubs whose version or path moved
// (site clone spec §5).
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	start := s.now()
	if err := s.loadAll(ctx); err != nil {
		return adapter.Listing{}, err
	}
	since, incremental := parseCursor(cursor)
	var dirty map[string]bool
	if incremental {
		var err error
		if dirty, err = s.changedContainers(ctx, searchWindow(since, start)); err != nil {
			return adapter.Listing{}, fmt.Errorf("%w (gfs pull --full lists everything without searching)", err)
		}
	}
	ids := make([]string, 0, len(s.tree))
	for id := range s.tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	pathsBySpace := map[string]map[string]string{}
	l := adapter.Listing{Full: true, Cursor: start.UTC().Format(time.RFC3339)}
	for _, id := range ids {
		ref := s.tree[id]
		if incremental && !dirty[id] {
			ps, ok := pathsBySpace[ref.Space]
			if !ok {
				ps = s.paths(s.byID[ref.Space])
				pathsBySpace[ref.Space] = ps
			}
			l.Resources = append(l.Resources, adapter.Resource{ID: id, Version: strconv.Itoa(ref.Version), Path: ps[id]})
			continue
		}
		r, err := s.Fetch(ctx, id)
		if errors.Is(err, adapter.ErrNotFound) {
			continue // deleted while listing
		}
		if err != nil {
			return adapter.Listing{}, err
		}
		l.Resources = append(l.Resources, *r)
	}
	return l, nil
}

// parseCursor reads List's cursor; anything unreadable means list everything.
func parseCursor(c string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, c)
	return t, err == nil
}

// searchWindow is how many minutes back the change searches reach: the time
// since the last listing, rounded up, plus searchOverlap. CQL reads absolute
// dates in the account's time zone, so gfs only sends relative ones.
func searchWindow(since, now time.Time) int {
	m := int(math.Ceil(now.Sub(since).Minutes()))
	return max(m, 0) + searchOverlap
}

// changedContainers returns the loaded pages whose comments or attachments
// changed in the last mins minutes.
func (s *session) changedContainers(ctx context.Context, mins int) (map[string]bool, error) {
	scope := ""
	if len(s.sel.keys) > 0 {
		q := make([]string, len(s.sel.keys))
		for i, k := range s.sel.keys {
			q[i] = strconv.Quote(k)
		}
		scope = " AND space in (" + strings.Join(q, ",") + ")"
	}
	out := map[string]bool{}
	for _, typ := range []string{"comment", "attachment"} {
		cql := fmt.Sprintf(`type = %s AND lastmodified >= now("-%dm")%s`, typ, mins, scope)
		err := s.c.paginate(ctx, "/wiki/rest/api/content/search?limit=250&expand=container&cql="+url.QueryEscape(cql), func(raw json.RawMessage) error {
			var hit struct {
				Container struct {
					ID string `json:"id"`
				} `json:"container"`
			}
			if err := json.Unmarshal(raw, &hit); err != nil {
				return err
			}
			if _, ok := s.tree[hit.Container.ID]; ok {
				out[hit.Container.ID] = true
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("search %ss changed since the last pull: %w", typ, err)
		}
	}
	return out, nil
}
```

Remove imports that `session.go` no longer uses (`go build` names them).

In `internal/cli/confluence_e2e_test.go` step 4, change the comment to
`srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>Nice.</p>", CreatedAt: srv.Stamp()})` — a comment added after the clone must carry a recent time for the search to find it.

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS. `TestConfluenceAttachmentsEndToEnd` passes because `EditAttachment` now stamps the current time.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence internal/cli/confluence_e2e_test.go
git commit -m "confluence: incremental listing: page stubs plus a search for changed comments and attachments"
```

---

### Task 7: `gfs pull --full`

**Files:**
- Modify: `internal/engine/pull.go:18-21,49`, `internal/cli/pull.go`, `internal/adapter/fake/fake.go` (`Remote`, `List`)
- Test: `internal/engine/pull_test.go`

**Interfaces:**
- Produces: `PullOpts.Full bool`; `fake.Remote.Cursors []string` (cursor of every `List` call); fake `List` returns `Cursor: "c1"`.

- [ ] **Step 1: Write the failing test** (append to `pull_test.go`)

```go
func TestPullFullPassesEmptyCursor(t *testing.T) {
	env, ad, _ := cloned(t)
	pull(t, env, PullOpts{})
	pull(t, env, PullOpts{Full: true})
	if got := strings.Join(ad.Remote.Cursors, ","); got != ",c1," {
		t.Fatalf("cursors passed to List (clone, pull, pull --full): %q", got)
	}
}
```

(`strings` is already imported in `pull_test.go`; add it if not.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine -run TestPullFullPassesEmptyCursor`
Expected: FAIL to compile (`Full`, `Cursors` undefined).

- [ ] **Step 3: Implement**

`fake.go`: add to `Remote` `Cursors []string // cursor passed to each List call`, and make `List` record it:

```go
func (s session) List(_ context.Context, cursor string) (adapter.Listing, error) {
	s.r.Cursors = append(s.r.Cursors, cursor)
	l := adapter.Listing{Full: true, Cursor: "c1"}
	for id, rec := range s.r.recs {
		l.Resources = append(l.Resources, *s.r.resource(id, rec))
	}
	return l, nil
}
```

`engine/pull.go`:

```go
type PullOpts struct {
	Force  bool
	Full   bool // list everything, ignoring the cursor
	Filter func(string) bool
}
```

and at line 49:

```go
	cursor := e.Index.Cursor
	if o.Full {
		cursor = "" // label-only changes, deleted comments and attachments show up only this way
	}
	l, err := e.Session.List(ctx, cursor)
```

`cli/pull.go`, after the `--force` flag:

```go
	cmd.Flags().BoolVar(&o.Full, "full", false, "fetch every page instead of only what changed")
```

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine internal/cli/pull.go internal/adapter/fake
git commit -m "pull --full: list everything, ignoring the cursor"
```

---

### Task 8: Whole-site end-to-end

**Files:**
- Create: `internal/cli/confluence_site_e2e_test.go`

**Interfaces:**
- Consumes: everything above through the `gfs` CLI; helpers `mustRun`, `gfs`, `replaceIn`, `mustContain` from `internal/cli` tests.

- [ ] **Step 1: Write the tests**

```go
package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func siteServer(t *testing.T) *cftest.Server {
	t.Helper()
	srv := cftest.New()
	t.Cleanup(srv.Close)
	srv.AddSpace("ENG", "100")
	srv.AddSpace("OPS", "200")
	srv.PutSpace(cftest.Space{Key: "~jan", ID: "300", Type: "personal"})
	srv.PutSpace(cftest.Space{Key: "OLD", ID: "400", Status: "archived"})
	srv.AddPage(cftest.Page{ID: "81001", Title: "Home", SpaceID: "100", Storage: "<p>eng</p>"})
	srv.AddPage(cftest.Page{ID: "81002", Title: "Architecture", ParentID: "81001", SpaceID: "100", Storage: "<p>a</p>"})
	srv.AddPage(cftest.Page{ID: "82001", Title: "Home", SpaceID: "200", Storage: "<p>ops</p>"})
	srv.AddPage(cftest.Page{ID: "83001", Title: "Jan", SpaceID: "300", Storage: "<p>jan</p>"})
	srv.AddPage(cftest.Page{ID: "84001", Title: "Old", SpaceID: "400", Storage: "<p>old</p>"})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	return srv
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var urlLine = regexp.MustCompile(`(?m)^\s*url = (.*)$`)

func remoteURL(t *testing.T, tree string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tree, ".gfs", "config"))
	if err != nil {
		t.Fatal(err)
	}
	m := urlLine.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("no url in config:\n%s", b)
	}
	return m[1]
}

var pageBodyGet = regexp.MustCompile(`^GET /wiki/api/v2/pages/[^/]+$`)

func TestConfluenceSiteEndToEnd(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)

	// whole site: global, current spaces only
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "acme"))
	for _, p := range []string{"eng/Home.xml", "eng/Home/Architecture.xml", "ops/Home.xml"} {
		if !exists(p) {
			t.Fatalf("missing %s", p)
		}
	}
	if exists("~jan") || exists("old") {
		t.Fatal("personal and archived spaces must be skipped")
	}

	// a quiet pull fetches no page bodies
	srv.TakeRequests()
	mustContain(t, mustRun(t, 0, "pull"), "Already up to date.")
	for _, r := range srv.TakeRequests() {
		if pageBodyGet.MatchString(r) {
			t.Fatalf("quiet pull fetched a page: %s", r)
		}
	}

	// comment-only change is picked up; label-only change waits for --full
	srv.AddComment(cftest.Comment{PageID: "82001", Storage: "<p>on call</p>", CreatedAt: srv.Stamp()})
	srv.SetLabels("81002", "design")
	out := mustRun(t, 0, "pull")
	mustContain(t, out, "~  ops/Home.xml")
	if strings.Contains(out, "Architecture") {
		t.Fatalf("label-only change must wait for --full:\n%s", out)
	}
	mustContain(t, mustRun(t, 0, "pull", "--full"), "~  eng/Home/Architecture.xml")

	// one commit, two spaces
	replaceIn(t, "eng/Home.xml", "<p>eng</p>", "<p>eng v2</p>")
	os.MkdirAll("ops/Home", 0o755)
	os.WriteFile("ops/Home/Deploy.xml", []byte(`<page><title>Deploy</title><body type="application/xhtml+xml"><p>steps</p></body></page>`), 0o644)
	mustRun(t, 0, "commit")
	b, _ := os.ReadFile("ops/Home/Deploy.xml")
	m := regexp.MustCompile(`<page id="(\d+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("write-back:\n%s", b)
	}
	if p, _ := srv.Page(string(m[1])); p.SpaceID != "200" || p.ParentID != "82001" {
		t.Fatalf("%+v", p)
	}
	if p, _ := srv.Page("81001"); !strings.Contains(p.Storage, "eng v2") {
		t.Fatalf("%+v", p)
	}

	// a new top-level folder is refused
	os.MkdirAll("new", 0o755)
	os.WriteFile("new/Page.xml", []byte(`<page><title>Page</title><body type="application/xhtml+xml"/></page>`), 0o644)
	out, code := gfs(t, "commit", "--dry-run")
	if code == 0 || !strings.Contains(out, `no space "new" in this tree; gfs does not create spaces`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	os.RemoveAll("new")

	// a move between spaces is refused
	os.Rename("eng/Home/Architecture.xml", "ops/Home/Architecture.xml")
	out, code = gfs(t, "commit", "--dry-run")
	if code == 0 || !strings.Contains(out, "moving pages between spaces is not supported") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	os.Rename("ops/Home/Architecture.xml", "eng/Home/Architecture.xml")

	// a space created on Confluence arrives with the next pull
	srv.AddSpace("DOC", "500")
	srv.AddPage(cftest.Page{ID: "85001", Title: "Docs", SpaceID: "500", Storage: "<p>d</p>"})
	mustContain(t, mustRun(t, 0, "pull"), "+  doc/Docs.xml")

	// excluding a space in the config removes its files
	replaceIn(t, ".gfs/config", "acme.atlassian.net?", "acme.atlassian.net?exclude=OPS&")
	out = mustRun(t, 0, "pull")
	mustContain(t, out, "-  ops/Home.xml", "-  ops/Home/Deploy.xml")
	if exists("ops/Home.xml") {
		t.Fatal("excluded space still on disk")
	}
}

func TestConfluenceCloneForms(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	base := "base=" + srv.URL
	same := map[string]string{
		"path":    "confluence://acme.atlassian.net/ENG?" + base,
		"browser": "confluence:https://acme.atlassian.net/wiki/spaces/ENG/pages/81002/Architecture?" + base,
		"filter":  "confluence://acme.atlassian.net?filter=ENG&" + base,
	}
	want := ""
	for name, raw := range same {
		mustRun(t, 0, "clone", raw, name)
		if !exists(filepath.Join(name, "eng/Home.xml")) || exists(filepath.Join(name, "ops")) {
			t.Fatalf("%s: wrong spaces", name)
		}
		got := remoteURL(t, name)
		if want == "" {
			want = got
		}
		if got != want || !strings.Contains(got, "filter=ENG") {
			t.Fatalf("%s: url %q, want %q", name, got, want)
		}
	}

	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?type=personal&"+base, "personal")
	if !exists("personal/~jan/Jan.xml") || exists("personal/eng") {
		t.Fatal("type=personal")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?exclude=OPS&"+base, "noops")
	if !exists("noops/eng/Home.xml") || exists("noops/ops") {
		t.Fatal("exclude=OPS")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?filter=OLD,~jan&"+base, "named")
	if !exists("named/old/Old.xml") || !exists("named/~jan/Jan.xml") {
		t.Fatal("filter=OLD,~jan")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?"+base) // default dir from the key
	if !exists("eng/eng/Home.xml") {
		t.Fatal("default dir")
	}

	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net?filter=NOPE&"+base, "x"); code == 0 || !strings.Contains(out, "space NOPE not found") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net?filter=ENG&type=all&"+base, "y"); code == 0 || !strings.Contains(out, "cannot be combined") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net/wiki/ENG?"+base, "z"); code == 0 || !strings.Contains(out, "want confluence://<site>") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
```

- [ ] **Step 2: Run**

Run: `go test ./internal/cli -run 'TestConfluenceSite|TestConfluenceCloneForms' -v`
Expected: PASS. If a step fails, the failure is a real gap in Tasks 1–7: fix it in the owning task's code, not in the test. Two places to check first if they fail: `mustContain` accepts several strings (it does in `docs_test.go`); the `.gfs/config` url line format (`url = …`) matches `urlLine`.

- [ ] **Step 3: Run** `go test ./...` — Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/confluence_site_e2e_test.go
git commit -m "e2e: whole-site clone, selection forms, incremental pull, commit across spaces"
```

---

### Task 9: Docs

**Files:**
- Modify: `docs/confluence.md` (intro, "Remote URL", "Layout", new "Pull" section, "Not supported yet"), `start.md` (typical session, pull flags)

- [ ] **Step 1: Edit `docs/confluence.md`**

Replace the first paragraph and the whole "Remote URL" section with:

````markdown
# gfs and Confluence Cloud

One working tree mirrors a Confluence site: every selected space is a folder,
every page is one XML file, the page tree is the folder tree. General `gfs`
usage: [start.md](../start.md).

## Remote URL

```text
confluence://[<email>@]<site>.atlassian.net[?filter=K1,K2&type=global|personal|all&exclude=K1,K2]
confluence://[<email>@]<site>.atlassian.net/<SPACEKEY>
confluence:https://<site>.atlassian.net/wiki/spaces/<SPACEKEY>/…
```

| parameter | meaning | default |
|-----------|---------|---------|
| `filter=K1,K2` | exactly these spaces, any type, archived included | all spaces |
| `type=global\|personal\|all` | which spaces, when there is no `filter` | `global` |
| `exclude=K1,K2` | leave these out | none |

* No `filter`: every current space of that type the account can see.
  Archived spaces are skipped.
* `confluence://<site>/HF` is short for `?filter=HF`.
* The third form is a URL copied from the browser, prefixed with
  `confluence:`. Any page of the space works; it always means the whole
  space.
* `type` and `filter` cannot be combined.
* The email is optional and URL-encoded (`@` becomes `%40`).
* `?base=<url>` overrides the API base URL (tests only).

```shell
gfs clone confluence://acme.atlassian.net                       # every global space, into acme/
gfs clone confluence://acme.atlassian.net/ENG                   # one space, into eng/
gfs clone "confluence:https://acme.atlassian.net/wiki/spaces/ENG/overview"
gfs clone "confluence://acme.atlassian.net?type=all&exclude=ARCHIVE"
```

`clone` records the URL in its canonical form (`confluence://<site>?filter=ENG`)
as `[remote] url` in `.gfs/config`. To change which spaces the tree holds,
edit that line; the next `pull` adds the spaces now selected and removes the
files of spaces no longer selected.
````

In "Layout", replace the tree with:

````markdown
```text
acme/                           the working tree
├── eng/                        space ENG, key lower-cased
│   ├── Home.xml                a page
│   ├── Home.files/             its attachments (downloaded with gfs get)
│   │   └── logo.svg
│   └── Home/                   its child pages
│       ├── Architecture.xml
│       └── Runbooks.xml
└── ~jan/                       personal space ~jan
    └── Jan's Home.xml
```
````

and add as the first bullet under it: `* The first folder is the space. A new top-level folder does not create a space; commit refuses it.`

Add a section before "## Attachments":

````markdown
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
````

In "Not supported yet" add:

```markdown
* Creating a space (a new top-level folder).
* Moving a page between spaces.
```

- [ ] **Step 2: Edit `start.md`**

In "Typical session" replace the clone and cd lines with:

```shell
gfs clone confluence://acme.atlassian.net          # every space; or .../ENG for one
cd acme
vim "eng/Home/Architecture.xml"
```

In "pull flags" add the row:

```markdown
| `--full` | Download every page instead of only what changed. Picks up label-only changes and deleted comments or attachments. |
```

- [ ] **Step 3: Run** `go test ./internal/cli -run TestDocs` then `go test ./...` — Expected: PASS (`gfs help confluence` still starts with `# gfs and Confluence Cloud`).

- [ ] **Step 4: Commit**

```bash
git add docs/confluence.md start.md
git commit -m "docs: whole-site clone, space selection, incremental pull"
```

---

### Task 10: Live check against Confluence Cloud (manual)

The fake encodes assumptions about the real API. Check them once against a
real site before calling the feature done. No code unless a check fails.

- [ ] **Step 1:** `gfs clone "confluence://<site>?filter=<SCRATCH>" /tmp/gfs-live && cd /tmp/gfs-live && gfs pull` — expect `Already up to date.`. This confirms the v2 page list includes `version` (otherwise every page looks changed) and that `now("-Nm")` CQL is accepted.
- [ ] **Step 2:** Add a footer comment to a page in the browser, run `gfs pull` — expect `~  <that page>`.
- [ ] **Step 3:** Upload an attachment in the browser, run `gfs pull` — expect `~  <that page>`.
- [ ] **Step 4:** `gfs clone confluence://<site> /tmp/gfs-site` — expect every global space as a folder; spot-check one personal space is absent.
- [ ] **Step 5:** If any check fails, record what the API returned and fix the owning task (Task 1 fake first, so a test reproduces it, then the adapter).
