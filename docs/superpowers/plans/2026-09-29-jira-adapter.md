# Jira Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirror Jira Cloud as XML files both ways: an agent-mode adapter (`jira://`) for every project a user can see, and a customer-mode adapter (`jira+customer://`) for the requests a user raised on someone else's service desk.

**Architecture:** A new `internal/adapter/atlassian` package takes credential resolution and the retrying HTTP client out of the Confluence adapter, so both products share them. `internal/adapter/jira` holds both modes: pure units (ADF↔XML, field codecs, issue decoding, transition choice) and two sessions built on `atlassian.Client`. Fakes in `jira/jtest` (a Jira site and a customer portal) drive unit, session and CLI end-to-end tests. The engine gets small generic additions:
- an O(1) index path lookup with batched index saves;
- `Listing.FullDirs`;
- `adapter.Cacher`;
- `adapter.Advisor` behind `gfs actions <path>`;
- dry runs that show `Check`'s detail.

**Tech Stack:** Go 1.25, standard library (`net/http`, `encoding/json`, `net/http/httptest`), cobra (CLI), `xmllint` for the grammar test (skipped when absent), and the repo's `xmltree`, `schema`, `canon`, `changes`, `engine` packages.

**Spec:** `docs/superpowers/specs/2026-09-29-jira-adapter-design.md`. Read it before starting; section numbers below refer to it.

**How this plan was checked:** every code block below was compiled and tested at `9dc2a77` (master when the plan was written). The tree was rebuilt task by task: after each task N, `gofmt -l`, `go vet ./...` and `go test ./...` passed. The read-only real-site checks of Task 21 passed against a live Jira Cloud site and a live customer portal. If master has moved since, adapt the edit anchors (quoted code the steps replace); don't restructure.

## Task overview

| # | task | spec |
|---|------|------|
| 1 | Shared Atlassian package (credentials, retrying client, token verification) | §2, §3.3, §10 |
| 2 | Confluence on the shared package | §2 |
| 3 | Engine scale: O(1) path lookup, batched index saves | Review Focus 5 |
| 4 | Engine hooks: `FullDirs`, `Cacher`, sorted repeated fields, `reply`/`approve` ask | §6.5, §5.1, §7.7 |
| 5 | `gfs actions <path>` and `adapter.Advisor` | §8 |
| 6 | ADF as XML | §5.4 |
| 7 | URLs, selection, schemas | §3, §5.1, §5.10 |
| 8 | People, field metadata, field decoding | §5.2, §5.3, §5.7, §5.8 |
| 9 | Issue decoding, file paths | §4, §5.1 |
| 10 | Fake Jira server, read side | §11.1 |
| 11 | Agent session: list, fetch, cache, `.workflows.xml` | §5.9, §6 |
| 12 | Field edits and transitions | §7.2, §7.3 |
| 13 | Comments, worklogs, links | §5.5, §5.6, §7.6 |
| 14 | Attachments | §7.6 |
| 15 | Create and delete | §7.4, §7.5 |
| 16 | `Check`, `Apply`, `Describe`, `Available`; `jira://` goes live | §7.7, §7.8, §8 |
| 17 | Customer mode, read | §5.10, §9.1 |
| 18 | Customer mode, write; `jira+customer://` goes live | §9.2, §9.3 |
| 19 | End-to-end scenarios through the CLI | §11.3 |
| 20 | Docs, `gfs help jira`, `gfs schema jira`, example tree | §12 |
| 21 | Real-site checks | §11.4 |

## Global Constraints

- Module path `github.com/KrzysztofBogdan/gitfs`; Go 1.25; no new third-party dependencies.
- Adapter names and schemes: `jira` (scheme `jira`) and `jira-customer` (scheme `jira+customer`).
- Credentials realm `atlassian`; environment overrides `GFS_JIRA_EMAIL`, `GFS_JIRA_TOKEN` (Jira, both modes) and `GFS_CONFLUENCE_EMAIL`, `GFS_CONFLUENCE_TOKEN` (Confluence, unchanged).
- Body type for rich text: `application/vnd.atlassian.adf+xml`; customer-mode text: `text/x-jira-wiki`.
- Root elements: `<issue>` (agent), `<request>` (customer), `<people id="people">` at `.people.xml`, `<workflows id="workflows">` at `.workflows.xml`.
- File name: `<project dir>/<KEY> <summary>.xml`, where the project dir is the key lower-cased, cut to at most 200 bytes before `.xml`; path model `adapter.Flat`.
- Policy classes: `create update transition comment reply worklog link delete approve`; `reply`, `approve` and `delete` default to `ask`.
- Retries (Confluence's rules, now shared): 429 on any method after `Retry-After` (seconds or HTTP date); 502/503/504 and network errors on GET only; backoff 1s doubling with up to 25% jitter, capped at 1 minute per wait; at most 6 attempts and 5 minutes of waiting in total; uploads never retried.
- Incremental search overlap: 10 minutes. Search page size 100. Customer list page size 50.
- Cache: `<tree>/.gfs/cache/jira/` (`meta.json`, `people.json`, `workflows.xml`). A missing or broken cache is rebuilt, never an error.
- Error text: lower-case, no trailing period, names the element or key involved, and says what to do (`…; add it`, `…; run gfs pull`).
- After every task: `gofmt -l .` prints nothing, `go vet ./...` is clean, `go test ./...` passes.
- Commit after every task, with a message in the repo's style (`area: what changed`).

## Where the plan refines the spec

These came up while building and checking the plan. The spec was updated alongside this plan to say the same:

1. **Retries:** the shared client keeps Confluence's retry rules (above) rather than spec §10's "at most 5 attempts".
2. **Clearing a field** is removing its element. Canon drops empty elements, so `<assignee/>` cannot exist in a file (§5.7).
3. **Locking** is the engine's own fetch-and-compare just before `Apply`. No extra `GET` in `Apply` (§7.1).
4. **Generated files** have roots `<people id="people">` and `<workflows id="workflows">`. The `id` is what makes the engine track them. The note in a statuses-only `.workflows.xml` is a `<note>` element, since canon drops XML comments (§5.8, §5.9).
5. **Customer default folder** is the site's first host label. A desk's key is unknown before the API answers (§3.2).
6. **Customer `<status>`** is edited with a transition name, because customer transitions report no target status. Customer comments carry no `type` attribute (§5.10, §9.2).
7. **`gfs actions`** results carry a state line and a note besides the list (`adapter.Advice`), and dry runs print `Check`'s detail, e.g. `transition In Progress -> Closed (Resolve this issue) with <resolution>` (§7.8, §8).
8. **Cache location** is `.gfs/cache/jira/`, handed to sessions through `adapter.Cacher` (§5.2).
9. **File names** are cut to 200 bytes at a character boundary. A real portal had summaries long enough to break the 255-byte limit (§4).
10. **Changed attachment bytes** are refused by the generic change computation ("update of attachments is not supported"), since the schema allows only `create delete` (§7.6).

## Review Focus

1. **A summary that is not a safe file name** (`a/b`, a leading `.`, ending `.files`, only spaces, longer than 200 bytes of multi-byte text), and a summary change on an issue whose attachment was fetched: the file is renamed and its `.files/` folder moves with it. Tests: Task 9 `TestIssuePaths`, Task 11 `TestIssuePathsAndRename`, Task 19 `TestJiraRenameMovesSidecar`.
2. **ADF text containing `<`, `&`, `]]>`, and whitespace-only runs next to inline nodes** (a mention followed by a space) round-trips exactly through pull → file → commit. Tests: Task 6 `TestADFRoundTrip` cases `cdata` and `mention space`.
3. **An issue moved between two selected projects while the user has a local edit**: pull renames the file to the new key and keeps the edit. Test: Task 19 `TestJiraMoveWithLocalEdit`.
4. **A rate-limit storm during clone or pull**: requests are retried with `Retry-After`; when retries run out, the pull fails with the HTTP 429 message and the cursor is not advanced. Tests: Task 1 `TestRetry429HonoursRetryAfter`, `TestRetryGivesUp`, `TestRetryGivesUpPastTotalWait`; Task 11 `TestListRateLimit`.
5. **Scale**: a 1,200-issue clone saves the index a handful of times, not once per issue, and search paging across `nextPageToken` boundaries stops exactly at `limit` in the middle of a page. Tests: Task 3 `TestCloneBatchesIndexSaves`; Task 11 `TestFullListingLimitMidPage`.

## File Map

```text
internal/adapter/adapter.go            + Listing.FullDirs, Cacher, Advisor, Advice, Available, AvailableField
internal/adapter/atlassian/            creds.go, client.go, verify.go (from confluence)
internal/adapter/confluence/client.go  thin wrapper over atlassian.Client; paginate stays
internal/adapter/confluence/url.go     parseTarget uses atlassian.Creds
internal/adapter/fake/fake.go          Partial, FullDirs, CacheDir, Advice, CheckDetail knobs
internal/workdir/index.go              byPath map
internal/engine/engine.go, clone.go, pull.go, commit.go   batched saves, FullDirs, Cacher, dry-run detail
internal/canon/canon.go                sort repeated fields by SortKey
internal/policy/policy.go              reply, approve default ask
internal/cli/actions.go, env.go, auth.go, root.go, docs.go   gfs actions <path>, Cacher, verify, registration, help and schema
internal/adapter/jira/
  adfspec.go, adf.go         ADF <-> XML
  url.go, schema.go          URLs, selection, schemas of both modes
  people.go, meta.go, fields.go, issue.go   users, metadata, field codecs, issue decoding, paths
  search.go, session.go, workflows.go       agent session read side
  encode.go, edit.go, transition.go         field edits and transitions
  subs.go, attachments.go, create.go        comments, worklogs, links, attachments, create, delete
  apply.go, advise.go, adapter.go           Check, Apply, Available, registration of jira://
  customer.go, customer_apply.go            customer mode, registration of jira+customer://
  relaxng.go                                gfs schema jira
  jtest/server.go, edit.go, subs.go, attach.go, create.go, portal.go, portal_write.go   fakes
internal/cli/jira_e2e_test.go, jira_scenarios_test.go, jira_customer_e2e_test.go
docs/jira.md, start.md, embed.go, example/README.md, example/jira/abc/
```

---
### Task 1: Shared Atlassian package

Credential resolution and the HTTP client, including the retry logic Confluence gained in `9dc2a77`, move into a package both adapters use (spec §2, §3.3, §10). The code is Confluence's, generalised: the product name goes into messages, errors keep Jira's per-field messages, and uploads take extra form fields.

**Files:**
- Create: `internal/adapter/atlassian/creds.go`
- Create: `internal/adapter/atlassian/client.go`
- Create: `internal/adapter/atlassian/verify.go`
- Test: `internal/adapter/atlassian/creds_test.go`, `internal/adapter/atlassian/client_test.go`

**Interfaces:**
- Consumes: `creds.Lookup` (`Token(email) (string, error)`, `HostEmail(host) (string, error)`, `SoleIdentity() string`), `creds.ErrNotFound`, `adapter.ErrNotFound`, `adapter.ErrLock`.
- Produces:
  - `func Creds(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup, prefix string) (email, token string, err error)`
  - `type Target struct{ Base, Email, Token string }`
  - `type APIError struct{ Product string; Status int; Body string; Fields map[string]string }` with `Error()` (`"<product lower-cased>: HTTP <n>: <message>"`) and `Message()`
  - `func Code(err error) string`
  - `const MaxAttempts = 6`, `MaxTotalWait = 5 * time.Minute`, `MaxBackoff = time.Minute`
  - `type Client struct{ Target Target; Product string; Header http.Header; Sleep func(context.Context, time.Duration) error; Now func() time.Time; OnWait func(string); … }`
  - `func New(t Target, product string) *Client` (product is `"Confluence"` or `"Jira"`)
  - `(c *Client) NewRequest`, `Do(ctx, method, path, in, out) error`, `Download(ctx, path, w) (int64, error)`, `Upload(ctx, path, filename, r, fields map[string]string, out) error`
  - `func VerifyToken(ctx context.Context, base, email, token string) (string, error)`

Retry rules (unchanged from Confluence): 429 is retried for any method after `Retry-After` (seconds or an HTTP date); 502/503/504 and network errors only for GET; otherwise backoff from 1s doubling with up to 25% jitter, capped at a minute per wait; at most 6 attempts and 5 minutes of waiting in total. `OnWait` hears `"Rate limited by Jira, retrying in 3s…"` before each wait. Uploads are never retried.

- [ ] **Step 1.1: Write the failing credential tests**

`internal/adapter/atlassian/creds_test.go`:

```go
package atlassian

import (
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

type lookup struct {
	tokens map[string]string
	hosts  map[string]string
	sole   string
}

func (l lookup) Token(e string) (string, error) {
	if t, ok := l.tokens[e]; ok {
		return t, nil
	}
	return "", creds.ErrNotFound
}
func (l lookup) HostEmail(h string) (string, error) { return l.hosts[h], nil }
func (l lookup) SoleIdentity() string               { return l.sole }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestCreds(t *testing.T) {
	stored := lookup{tokens: map[string]string{"a@x.com": "ta", "h@x.com": "th", "s@x.com": "ts"},
		hosts: map[string]string{"acme.atlassian.net": "h@x.com"}, sole: "s@x.com"}
	cases := []struct {
		name, url string
		cfg       map[string]string
		env       map[string]string
		lk        lookup
		email     string
		token     string
		err       string
	}{
		{"env beats all", "jira://u%40x.com@acme.atlassian.net", map[string]string{"email": "c@x.com"},
			map[string]string{"GFS_JIRA_EMAIL": "e@x.com", "GFS_JIRA_TOKEN": "te"}, stored, "e@x.com", "te", ""},
		{"url user", "jira://a%40x.com@acme.atlassian.net", nil, nil, stored, "a@x.com", "ta", ""},
		{"remote email", "jira://acme.atlassian.net", map[string]string{"email": "a@x.com"}, nil, stored, "a@x.com", "ta", ""},
		{"host default", "jira://acme.atlassian.net", nil, nil, stored, "h@x.com", "th", ""},
		{"sole identity", "jira://other.atlassian.net", nil, nil, stored, "s@x.com", "ts", ""},
		{"env token", "jira://acme.atlassian.net", nil, map[string]string{"GFS_JIRA_TOKEN": "te"}, stored, "h@x.com", "te", ""},
		{"other prefix ignored", "jira://acme.atlassian.net", nil, map[string]string{"GFS_CONFLUENCE_TOKEN": "tc"}, stored, "h@x.com", "th", ""},
		{"no identity", "jira://other.atlassian.net", nil, nil, lookup{}, "", "", "no identity for other.atlassian.net: run gfs auth set <email> --host other.atlassian.net, or put the email in the URL (jira://me%40x.com@other.atlassian.net)"},
		{"no token", "jira://acme.atlassian.net", map[string]string{"email": "z@x.com"}, nil, stored, "", "", "no token for z@x.com: run gfs auth set z@x.com"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		email, token, err := Creds(u, c.cfg, env(c.env), c.lk, "GFS_JIRA")
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || email != c.email || token != c.token {
			t.Errorf("%s: got %q %q %v, want %q %q", c.name, email, token, err, c.email, c.token)
		}
	}
}
```

- [ ] **Step 1.2: Run it to make sure it fails**

Run: `go test ./internal/adapter/atlassian/ -run TestCreds -v`
Expected: FAIL, `undefined: Creds`.

- [ ] **Step 1.3: Implement `creds.go`**

```go
// Package atlassian holds what the Atlassian Cloud adapters share: the
// identity and token of a remote, and an HTTP client that retries when the
// site rate-limits (jira spec §2, §3.3).
package atlassian

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// Creds resolves the email and API token for remote u (credentials spec §5):
// the email from env <prefix>_EMAIL, the URL user, [remote] email, the host
// default, the only stored identity; the token from env <prefix>_TOKEN, else
// the keyring. prefix is "GFS_CONFLUENCE" or "GFS_JIRA".
func Creds(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup, prefix string) (email, token string, err error) {
	host := u.Hostname()
	if email, err = resolveEmail(u, cfg, getenv, lk, prefix); err != nil {
		return "", "", err
	}
	if email == "" {
		return "", "", fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (%s://me%%40x.com@%s)", host, host, u.Scheme, host)
	}
	if token = getenv(prefix + "_TOKEN"); token != "" {
		return email, token, nil
	}
	token, err = lk.Token(email)
	if errors.Is(err, creds.ErrNotFound) {
		return "", "", fmt.Errorf("no token for %s: run gfs auth set %s", email, email)
	}
	if err != nil {
		return "", "", err
	}
	return email, token, nil
}

func resolveEmail(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup, prefix string) (string, error) {
	if e := getenv(prefix + "_EMAIL"); e != "" {
		return e, nil
	}
	if u.User != nil && u.User.Username() != "" {
		return u.User.Username(), nil
	}
	if e := cfg["email"]; e != "" {
		return e, nil
	}
	if e, err := lk.HostEmail(u.Hostname()); err != nil || e != "" {
		return e, err
	}
	return lk.SoleIdentity(), nil
}
```

- [ ] **Step 1.4: Run the credential tests**

Run: `go test ./internal/adapter/atlassian/ -run TestCreds -v`
Expected: PASS.

- [ ] **Step 1.5: Write the failing client tests**

`internal/adapter/atlassian/client_test.go` (the retry cases are Confluence's `retry_test.go` cases against a plain `httptest` server):

```go
package atlassian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var bg = context.Background()

// flaky answers the first times requests with status (and Retry-After, if
// set), then 200 with body.
type flaky struct {
	mu         sync.Mutex
	status     int
	times      int
	retryAfter string
	body       string
	seen       []string // "METHOD body" per request
}

func (f *flaky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Method+" "+string(b))
	if f.times > 0 {
		f.times--
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		http.Error(w, `{"errorMessages":["slow down"]}`, f.status)
		return
	}
	io.WriteString(w, f.body)
}

// retrying returns a client for f whose waits and notices are recorded.
func retrying(t *testing.T, f *flaky) (*Client, *[]time.Duration, *[]string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(Target{Base: srv.URL, Email: "me@x.com", Token: "t"}, "Jira")
	var waits []time.Duration
	var notes []string
	c.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	c.OnWait = func(msg string) { notes = append(notes, msg) }
	return c, &waits, &notes
}

func TestRetry429HonoursRetryAfter(t *testing.T) {
	f := &flaky{status: 429, times: 2, retryAfter: "3", body: `{"displayName":"Jay"}`}
	c, waits, notes := retrying(t, f)
	var out struct{ DisplayName string }
	if err := c.Do(bg, "GET", "/rest/api/3/myself", nil, &out); err != nil || out.DisplayName != "Jay" {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(*waits, []time.Duration{3 * time.Second, 3 * time.Second}) {
		t.Fatalf("waits %v", *waits)
	}
	if len(*notes) != 2 || (*notes)[0] != "Rate limited by Jira, retrying in 3s…" {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c, waits, _ := retrying(t, &flaky{status: 429, times: 1, retryAfter: now.Add(7 * time.Second).Format(http.TimeFormat)})
	c.Now = func() time.Time { return now }
	if err := c.Do(bg, "GET", "/x", nil, nil); err != nil || !reflect.DeepEqual(*waits, []time.Duration{7 * time.Second}) {
		t.Fatalf("%v %v", *waits, err)
	}
}

func TestRetryBacksOffWithoutHeader(t *testing.T) {
	c, waits, notes := retrying(t, &flaky{status: 503, times: 3})
	if err := c.Do(bg, "GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 3 {
		t.Fatalf("waits %v", *waits)
	}
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if w := (*waits)[i]; w < base || w > base+base/4 {
			t.Errorf("wait %d = %v, want %v plus up to 25%% jitter", i, w, base)
		}
	}
	if !strings.Contains((*notes)[0], "Jira unavailable (HTTP 503)") {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryWritesOnlyOn429(t *testing.T) {
	f := &flaky{status: 429, times: 1}
	c, _, _ := retrying(t, f)
	if err := c.Do(bg, "POST", "/x", map[string]int{"a": 1}, nil); err != nil {
		t.Fatalf("POST must be retried after 429: %v", err)
	}
	if !reflect.DeepEqual(f.seen, []string{`POST {"a":1}`, `POST {"a":1}`}) {
		t.Fatalf("the body must be sent again: %q", f.seen)
	}
	f.status, f.times, f.seen = 503, 1, nil
	if err := c.Do(bg, "POST", "/x", nil, nil); Code(err) != "503" || len(f.seen) != 1 {
		t.Fatalf("POST must not be retried after 503: %v, %d sent", err, len(f.seen))
	}
}

func TestRetryGivesUp(t *testing.T) {
	c, waits, _ := retrying(t, &flaky{status: 429, times: 100})
	err := c.Do(bg, "GET", "/x", nil, nil)
	if Code(err) != "429" || !strings.Contains(err.Error(), "jira: HTTP 429: slow down") || len(*waits) != MaxAttempts-1 {
		t.Fatalf("%v after %d waits", err, len(*waits))
	}
}

func TestRetryGivesUpPastTotalWait(t *testing.T) {
	c, waits, _ := retrying(t, &flaky{status: 429, times: 100, retryAfter: "200"})
	if err := c.Do(bg, "GET", "/x", nil, nil); err == nil || len(*waits) != 1 {
		t.Fatalf("waits %v: a second 200s wait would pass the %v cap", *waits, MaxTotalWait)
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	c, _, _ := retrying(t, &flaky{status: 429, times: 5})
	ctx, cancel := context.WithCancel(bg)
	c.Sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	if err := c.Do(ctx, "GET", "/x", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestAPIErrors(t *testing.T) {
	cases := []struct {
		product string
		status  int
		body    string
		want    string
		fields  map[string]string
	}{
		{"Confluence", 400, `{"message":"bad title"}`, "confluence: HTTP 400: bad title", nil},
		{"Jira", 400, `{"errorMessages":["one"],"errors":{"summary":"required","customfield_1":"bad"}}`,
			"jira: HTTP 400: one; customfield_1: bad; summary: required", map[string]string{"summary": "required", "customfield_1": "bad"}},
		{"Jira", 403, `{"errorMessage":"not a customer"}`, "jira: HTTP 403: not a customer", nil},
		{"Jira", 500, "boom\n", "jira: HTTP 500: boom", nil},
		{"Confluence", 404, `{"errors":[{"title":"Not Found"}]}`, `confluence: HTTP 404: {"errors":[{"title":"Not Found"}]}`, nil},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		cl := New(Target{Base: srv.URL}, c.product)
		cl.Sleep = func(context.Context, time.Duration) error { return nil }
		err := cl.Do(bg, "POST", "/x", nil, nil)
		srv.Close()
		var ae *APIError
		if !errors.As(err, &ae) || ae.Error() != c.want || !reflect.DeepEqual(ae.Fields, c.fields) {
			t.Errorf("%d %s: got %v (%+v)", c.status, c.body, err, ae)
		}
		if (c.status == 404) != errors.Is(err, adapter.ErrNotFound) {
			t.Errorf("%d: ErrNotFound mapping", c.status)
		}
	}
}

func TestLockAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "me@x.com" || p != "t" || r.Header.Get("X-ExperimentalApi") != "opt-in" {
			t.Errorf("auth or headers: %v", r.Header)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL, Email: "me@x.com", Token: "t"}, "Jira")
	c.Header.Set("X-ExperimentalApi", "opt-in")
	if err := c.Do(bg, "PUT", "/x", nil, nil); !errors.Is(err, adapter.ErrLock) || Code(err) != "409" {
		t.Fatalf("err %v", err)
	}
}

func TestUploadAndDownload(t *testing.T) {
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			if r.Header.Get("X-Atlassian-Token") != "no-check" {
				t.Error("no XSRF header")
			}
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			if h.Filename != "a b.txt" || string(b) != "bytes" || r.FormValue("minorEdit") != "true" {
				t.Errorf("upload %q %q %q", h.Filename, b, r.FormValue("minorEdit"))
			}
			fmt.Fprint(w, `[{"id":"77"}]`)
		case "GET":
			if downloads++; downloads == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			io.WriteString(w, "content")
		}
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL}, "Jira")
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	var out []struct{ ID string }
	if err := c.Upload(bg, "/up", "a b.txt", strings.NewReader("bytes"), map[string]string{"minorEdit": "true"}, &out); err != nil || out[0].ID != "77" {
		t.Fatalf("%v %+v", err, out)
	}
	var b strings.Builder
	if n, err := c.Download(bg, "/down", &b); err != nil || n != 7 || b.String() != "content" {
		t.Fatalf("download retries a 429: %d %v %q", n, err, b.String())
	}
}

func TestVerifyToken(t *testing.T) {
	site := func(routes map[string]int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			code, ok := routes[r.URL.Path]
			if !ok {
				code = 404
			}
			w.WriteHeader(code)
			if code == 200 {
				fmt.Fprint(w, `{"displayName":"Jay","values":[]}`)
			}
		}))
	}
	cases := []struct {
		name   string
		routes map[string]int
		want   string
		code   string
	}{
		{"jira", map[string]int{"/rest/api/3/myself": 200}, "Jay", ""},
		{"confluence only", map[string]int{"/wiki/rest/api/user/current": 200}, "Jay", ""},
		{"customer only", map[string]int{"/rest/servicedeskapi/request": 200}, "me@x.com", ""},
		{"bad token", map[string]int{"/rest/api/3/myself": 401, "/wiki/rest/api/user/current": 401, "/rest/servicedeskapi/request": 401}, "", "401"},
		{"nothing there", map[string]int{}, "", "404"},
	}
	for _, c := range cases {
		srv := site(c.routes)
		name, err := VerifyToken(bg, srv.URL, "me@x.com", "t")
		srv.Close()
		if name != c.want || Code(err) != c.code {
			t.Errorf("%s: got %q %v", c.name, name, err)
		}
	}
}
```

- [ ] **Step 1.6: Run them to make sure they fail**

Run: `go test ./internal/adapter/atlassian/ -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 1.7: Implement `client.go`**

This is `internal/adapter/confluence/client.go` as of `9dc2a77` with the product name in messages, `APIError.Fields`/`Message`, exported hooks, and fields on uploads:

```go
package atlassian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// Target is where and as whom a client talks.
type Target struct{ Base, Email, Token string }

// APIError is an HTTP error response from an Atlassian API.
type APIError struct {
	Product string // "Confluence", "Jira"; the message starts with it lower-cased
	Status  int
	Body    string
	Fields  map[string]string // Jira "errors": field id -> message
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", strings.ToLower(e.Product), e.Status, e.Message())
}

// Message is the service's own text: Confluence "message", JSM
// "errorMessage", Jira "errorMessages" and "errors"; else the raw body.
func (e *APIError) Message() string {
	var m struct {
		Message       string   `json:"message"`
		ErrorMessage  string   `json:"errorMessage"`
		ErrorMessages []string `json:"errorMessages"`
	}
	var parts []string
	if json.Unmarshal([]byte(e.Body), &m) == nil {
		for _, s := range append([]string{m.Message, m.ErrorMessage}, m.ErrorMessages...) {
			if s != "" {
				parts = append(parts, s)
			}
		}
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	if len(parts) == 0 {
		return strings.TrimSpace(e.Body)
	}
	return strings.Join(parts, "; ")
}

// Code is the HTTP status of err as text, or "".
func Code(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return strconv.Itoa(ae.Status)
	}
	return ""
}

const (
	MaxAttempts  = 6
	MaxTotalWait = 5 * time.Minute
	MaxBackoff   = time.Minute
)

type Client struct {
	Target  Target
	Product string      // "Confluence", "Jira": in messages
	Header  http.Header // added to every request, e.g. X-ExperimentalApi
	Sleep   func(context.Context, time.Duration) error
	Now     func() time.Time
	OnWait  func(msg string) // told before each retry wait; never nil
	hc      *http.Client     // JSON calls, with a timeout
	xfer    *http.Client     // attachment bytes: no timeout, the context cancels
}

func New(t Target, product string) *Client {
	return &Client{Target: t, Product: product, Header: http.Header{}, Sleep: sleepCtx, Now: time.Now,
		OnWait: func(string) {}, hc: &http.Client{Timeout: 60 * time.Second}, xfer: &http.Client{}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Target.Base+path, body)
	if err != nil {
		return nil, err
	}
	if c.Target.Email != "" || c.Target.Token != "" {
		req.SetBasicAuth(c.Target.Email, c.Target.Token)
	}
	req.Header.Set("Accept", "application/json")
	for k, vs := range c.Header {
		req.Header[k] = vs
	}
	return req, nil
}

// apiError maps an HTTP error response: 404 wraps adapter.ErrNotFound and 409
// adapter.ErrLock, both also wrapping the *APIError.
func (c *Client) apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ae := &APIError{Product: c.Product, Status: resp.StatusCode, Body: string(data)}
	var f struct {
		Errors map[string]string `json:"errors"`
	}
	if json.Unmarshal(data, &f) == nil && len(f.Errors) > 0 {
		ae.Fields = f.Errors
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
	}
	return ae
}

// send runs one request with retries. Atlassian answers 429 when an account
// sends too much; the request was not processed, so any method is retried
// after Retry-After. 502/503/504 and network errors are retried only for GET:
// a write may already have been applied. build makes a fresh request per attempt.
func (c *Client) send(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		msg, retry := c.retryReason(req.Method, status, err)
		if !retry || attempt == MaxAttempts {
			return resp, err
		}
		d := c.retryWait(attempt, resp)
		if waited+d > MaxTotalWait {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		c.OnWait(fmt.Sprintf("%s, retrying in %s…", msg, d.Round(time.Second)))
		if err := c.Sleep(ctx, d); err != nil {
			return nil, err
		}
		waited += d
	}
}

func (c *Client) retryReason(method string, status int, err error) (string, bool) {
	switch {
	case status == http.StatusTooManyRequests:
		return "Rate limited by " + c.Product, true
	case method != http.MethodGet:
		return "", false
	case err != nil:
		return "Connection error (" + err.Error() + ")", true
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return fmt.Sprintf("%s unavailable (HTTP %d)", c.Product, status), true
	}
	return "", false
}

// retryWait is Retry-After (seconds or an HTTP date) when the server sent it,
// else exponential backoff from 1s with up to 25% jitter, capped at MaxBackoff.
func (c *Client) retryWait(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				return time.Duration(n) * time.Second
			}
			if t, err := http.ParseTime(ra); err == nil {
				return max(t.Sub(c.Now()), 0)
			}
		}
	}
	d := min(time.Second<<(attempt-1), MaxBackoff)
	return d + time.Duration(rand.Int64N(int64(d)/4+1))
}

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payload = b
	}
	resp, err := c.send(ctx, c.hc, func() (*http.Request, error) {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := c.NewRequest(ctx, method, path, body)
		if err == nil && payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.apiError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Download streams the body of GET path into w; redirects are followed.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, c.xfer, func() (*http.Request, error) {
		req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
		if err == nil {
			req.Header.Set("Accept", "*/*")
		}
		return req, err
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, c.apiError(resp)
	}
	return io.Copy(w, resp.Body)
}

// Upload POSTs r as the multipart part "file" named filename, streaming it,
// then fields, with the XSRF header Atlassian requires, and decodes the JSON
// reply into out. Uploads are not retried: the stream cannot be replayed.
func (c *Client) Upload(ctx context.Context, path, filename string, r io.Reader, fields map[string]string, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() { pw.CloseWithError(writeMultipart(mw, filename, r, fields)) }()
	req, err := c.NewRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	resp, err := c.xfer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.apiError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func writeMultipart(mw *multipart.Writer, filename string, r io.Reader, fields map[string]string) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}
	return mw.Close()
}
```

- [ ] **Step 1.8: Implement `verify.go`**

```go
package atlassian

import (
	"context"
	"net/http"
	"strings"
)

// VerifyToken checks email and token against the site at base and returns
// the account's display name. It tries Jira, then Confluence, then the
// service desk customer API (customers see neither product API), so a token
// verifies on any Atlassian site the account can use. When every check fails
// the error of an auth failure (401, 403) wins over a missing product (404).
func VerifyToken(ctx context.Context, base, email, token string) (string, error) {
	c := New(Target{Base: strings.TrimRight(base, "/"), Email: email, Token: token}, "Atlassian")
	var first, auth error
	note := func(err error) {
		if first == nil {
			first = err
		}
		if code := Code(err); auth == nil && (code == "401" || code == "403") {
			auth = err
		}
	}
	for _, p := range []string{"/rest/api/3/myself", "/wiki/rest/api/user/current"} {
		var u struct {
			DisplayName string `json:"displayName"`
		}
		err := c.Do(ctx, http.MethodGet, p, nil, &u)
		if err == nil && u.DisplayName != "" {
			return u.DisplayName, nil
		}
		note(err)
	}
	err := c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/request?limit=1", nil, nil)
	if err == nil {
		return email, nil
	}
	note(err)
	if auth != nil {
		return "", auth
	}
	return "", first
}
```

- [ ] **Step 1.9: Run the package tests**

Run: `go vet ./internal/adapter/atlassian/ && go test ./internal/adapter/atlassian/ -v`
Expected: PASS.

- [ ] **Step 1.10: Commit**

```bash
git add internal/adapter/atlassian
git commit -m "atlassian: shared credentials and retrying HTTP client, from confluence"
```

### Task 2: Confluence on the shared package

Confluence keeps its behaviour, messages and tests, but uses `atlassian.Client` and `atlassian.Creds`. `gfs auth set --host` verifies with `atlassian.VerifyToken`, so a Jira-only or customer-only site verifies too.

**Files:**
- Modify: `internal/adapter/confluence/client.go` (whole file replaced below, `paginate` kept as is)
- Modify: `internal/adapter/confluence/session.go` (`openSession`: `s.c.onWait` → `s.c.OnWait`)
- Modify: `internal/adapter/confluence/url.go` (`parseTarget`; delete `resolveEmail`)
- Modify: `internal/cli/auth.go` (the `VerifyToken` call in `newAuthSet`)
- Modify: `internal/creds/creds.go` (message in `unavailable`)
- Modify (tests): `internal/adapter/confluence/retry_test.go`, `internal/adapter/confluence/client_test.go` (`TestVerifyToken`), `internal/creds/creds_test.go` (`TestKeyringUnavailable`), `internal/cli/auth_test.go` (`authEnv`)

**Interfaces:**
- Consumes: everything Task 1 produces.
- Produces: unchanged Confluence package API except that `confluence.VerifyToken` is removed (callers use `atlassian.VerifyToken`) and the client's retry hooks are the exported `Sleep`, `Now`, `OnWait` of the embedded `*atlassian.Client`.

- [ ] **Step 2.1: Replace `internal/adapter/confluence/client.go`**

Keep the existing `paginate` method exactly as it is; everything else in the file becomes:

```go
package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
)

// APIError is the shared Atlassian error type; its text still starts "confluence: HTTP …".
type APIError = atlassian.APIError

func codeOf(err error) string { return atlassian.Code(err) }

// client is the shared Atlassian client plus Confluence's own pagination.
type client struct {
	*atlassian.Client
	t target
}

func newClient(t target) *client {
	return &client{Client: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Confluence"), t: t}
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	return c.Do(ctx, method, path, in, out)
}

// download streams the body of GET path into w; redirects are followed.
func (c *client) download(ctx context.Context, path string, w io.Writer) (int64, error) {
	return c.Download(ctx, path, w)
}

// upload POSTs r as a new attachment version without notifying watchers.
func (c *client) upload(ctx context.Context, path, filename string, r io.Reader, out any) error {
	return c.Upload(ctx, path, filename, r, map[string]string{"minorEdit": "true"}, out)
}

```

(The `encoding/json`, `net/http` and `strings` imports are for `paginate`.)

- [ ] **Step 2.2: Point Confluence at the exported hooks**

In `internal/adapter/confluence/session.go`, `openSession`: `s.c.onWait = func(msg string) { … }` becomes `s.c.OnWait = func(msg string) { … }`.

In `internal/adapter/confluence/retry_test.go`: `c.sleep =` → `c.Sleep =`, `c.onWait =` → `c.OnWait =`, `c.now =` → `c.Now =`, `maxAttempts` → `atlassian.MaxAttempts`, `maxTotalWait` → `atlassian.MaxTotalWait`, and add the `internal/adapter/atlassian` import. These tests now exercise the shared client through `cftest`, so they still pin Confluence's behaviour.

- [ ] **Step 2.3: Use `atlassian.Creds` in `parseTarget`**

In `internal/adapter/confluence/url.go`, replace everything in `parseTarget` from `email, err := resolveEmail(...)` to the end of the function with:

```go
	email, token, err := atlassian.Creds(n, cfg, getenv, lk, "GFS_CONFLUENCE")
	if err != nil {
		return target{}, err
	}
	t.email, t.token = email, token
	return t, nil
}
```

Delete `resolveEmail` and add the `internal/adapter/atlassian` import (`creds` stays: `parseTarget` still takes a `creds.Lookup`).

- [ ] **Step 2.4: Verify with the shared function in `gfs auth set`**

In `internal/cli/auth.go`, `confluence.VerifyToken(cmd.Context(), base, email, token)` becomes `atlassian.VerifyToken(cmd.Context(), base, email, token)`; swap the import `internal/adapter/confluence` for `internal/adapter/atlassian`.

In `internal/creds/creds.go`:

```go
func unavailable(err error) error {
	return fmt.Errorf("keyring unavailable (%v); set GFS_CONFLUENCE_TOKEN or GFS_JIRA_TOKEN instead", err)
}
```

- [ ] **Step 2.5: Update the tests that pin old names**

- `internal/adapter/confluence/client_test.go`, `TestVerifyToken`: both `VerifyToken(...)` calls become `atlassian.VerifyToken(...)`; add the import. It still passes against `cftest`: the Jira probe gets 404 and the Confluence probe answers.
- `internal/creds/creds_test.go`, `TestKeyringUnavailable`: the expected text becomes `"keyring unavailable (no dbus); set GFS_CONFLUENCE_TOKEN or GFS_JIRA_TOKEN instead"`.
- `internal/cli/auth_test.go`, `authEnv`: after the two `GFS_CONFLUENCE_*` lines add

```go
	t.Setenv("GFS_JIRA_EMAIL", "")
	t.Setenv("GFS_JIRA_TOKEN", "")
```

- [ ] **Step 2.6: Run the whole suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: every package `ok`, including Confluence's retry, progress and site tests.

- [ ] **Step 2.7: Commit**

```bash
git add internal/adapter/confluence internal/cli/auth.go internal/cli/auth_test.go internal/creds
git commit -m "confluence: on the shared Atlassian client and credentials; auth verifies on any Atlassian site"
```

### Task 3: Engine scale: O(1) path lookup and batched index saves

A Jira site of 36,000 issues breaks two quadratic spots: `Index.ByPath` scans every entry, and `Env.StoreBase`/`Env.Forget` rewrite the whole index after each resource. Clone and pull batch their saves; commit keeps saving after every file.

**Files:**
- Modify: `internal/workdir/index.go` (`Index`, `Put`, `Delete`, `ByPath`, `LoadIndex`)
- Modify: `internal/engine/engine.go` (`Env`, `StoreBase`, `Forget`)
- Modify: `internal/engine/clone.go` (`Clone`)
- Modify: `internal/engine/pull.go` (`Pull`)
- Test: `internal/workdir/index_test.go` (create if missing), `internal/engine/scale_test.go`

**Interfaces:**
- Consumes: `workdir.Index`, `engine.Env`.
- Produces: `Env.Batch bool`, `Env.IndexSaves int` (count, for tests), `func (e *Env) saveIndex(force bool) error`, `const batchSize = 500`.

- [ ] **Step 3.1: Write the failing index test**

`internal/workdir/index_test.go` (add to the file if it exists):

```go
package workdir

import "testing"

func TestIndexByPathFollowsMoves(t *testing.T) {
	ix := &Index{byID: map[string]Entry{}, byPath: map[string]string{}}
	ix.Put(Entry{"1", "v1", "a/one.xml"})
	ix.Put(Entry{"2", "v1", "a/two.xml"})
	ix.Put(Entry{"1", "v2", "b/one.xml"}) // moved
	if _, ok := ix.ByPath("a/one.xml"); ok {
		t.Fatal("old path still maps")
	}
	if e, ok := ix.ByPath("b/one.xml"); !ok || e.ID != "1" || e.Version != "v2" {
		t.Fatalf("%+v %v", e, ok)
	}
	ix.Delete("2")
	if _, ok := ix.ByPath("a/two.xml"); ok {
		t.Fatal("deleted path still maps")
	}
}
```

- [ ] **Step 3.2: Run it to make sure it fails**

Run: `go test ./internal/workdir/ -run TestIndexByPathFollowsMoves -v`
Expected: FAIL, `unknown field byPath`.

- [ ] **Step 3.3: Give the index a path map**

In `internal/workdir/index.go`:

```go
type Index struct {
	Cursor string
	byID   map[string]Entry
	byPath map[string]string // path -> id
}

func (ix *Index) ByPath(path string) (Entry, bool) {
	id, ok := ix.byPath[path]
	if !ok {
		return Entry{}, false
	}
	return ix.byID[id], true
}

func (ix *Index) Put(e Entry) {
	if old, ok := ix.byID[e.ID]; ok && ix.byPath[old.Path] == e.ID {
		delete(ix.byPath, old.Path)
	}
	ix.byID[e.ID] = e
	ix.byPath[e.Path] = e.ID
}

func (ix *Index) Delete(id string) {
	if old, ok := ix.byID[id]; ok && ix.byPath[old.Path] == id {
		delete(ix.byPath, old.Path)
	}
	delete(ix.byID, id)
}
```

and in `LoadIndex` build it: `ix := &Index{byID: map[string]Entry{}, byPath: map[string]string{}}`.

- [ ] **Step 3.4: Run the workdir tests**

Run: `go test ./internal/workdir/ -v`
Expected: PASS.

- [ ] **Step 3.5: Write the failing batching test**

`internal/engine/scale_test.go`:

```go
package engine

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

// A clone of 1,200 resources must not rewrite the index once per resource.
func TestCloneBatchesIndexSaves(t *testing.T) {
	ad := fake.New()
	for i := 1; i <= 1200; i++ {
		ad.Remote.Put(fmt.Sprint(i), fmt.Sprintf("n/%04d.xml", i), `<note><title>t</title></note>`)
	}
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", t.TempDir()+"/wt", &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.IndexSaves > 4 {
		t.Fatalf("index saved %d times", env.IndexSaves)
	}
	ix, err := env.Tree.LoadIndex()
	if err != nil || len(ix.All()) != 1200 || ix.Cursor != "c1" {
		t.Fatalf("index after clone: %d entries, cursor %q, %v", len(ix.All()), ix.Cursor, err)
	}
	for i := 1; i <= 1200; i++ {
		ad.Remote.Edit(fmt.Sprint(i), setTitle("u"))
	}
	env.IndexSaves = 0
	pull(t, env, PullOpts{})
	if env.IndexSaves > 4 {
		t.Fatalf("pull saved the index %d times", env.IndexSaves)
	}
	if ix, _ := env.Tree.LoadIndex(); ix == nil {
		t.Fatal("index unreadable")
	} else if e, _ := ix.ByID("7"); e.Version != "2" {
		t.Fatalf("entry 7 after pull: %+v", e)
	}
}
```

(`ctx`, `pull` and `setTitle` already exist in the engine tests; `fake.Adapter.Open` ignores its arguments.)

- [ ] **Step 3.6: Run it to make sure it fails**

Run: `go test ./internal/engine/ -run TestCloneBatchesIndexSaves -v`
Expected: FAIL, `env.IndexSaves undefined`.

- [ ] **Step 3.7: Batch the saves**

In `internal/engine/engine.go` add to `Env`:

```go
	// Batch defers index saves to every batchSize changes; clone and pull
	// set it and save once at the end. IndexSaves counts writes, for tests.
	Batch      bool
	IndexSaves int
	pending    int
```

and:

```go
const batchSize = 500

// saveIndex writes the index now, or every batchSize calls while batching.
func (e *Env) saveIndex(force bool) error {
	if e.Batch && !force {
		if e.pending++; e.pending < batchSize {
			return nil
		}
	}
	e.pending = 0
	e.IndexSaves++
	return e.Tree.SaveIndex(e.Index)
}
```

In `StoreBase` and `Forget`, replace the final `return e.Tree.SaveIndex(e.Index)` with `return e.saveIndex(false)`.

In `internal/engine/clone.go`, set `Batch: true` in the `Env` literal, and replace the final save:

```go
	ix.Cursor = l.Cursor
	if err := env.saveIndex(true); err != nil {
		return nil, err
	}
	env.Batch = false
```

In `internal/engine/pull.go`, make `Pull` batch and always leave a saved index behind, including when it returns an error (the cursor is only advanced on success, so a saved index after a failure is consistent). Change the signature to use named results and add at the top of the body:

```go
func Pull(ctx context.Context, e *Env, o PullOpts) (r PullReport, err error) {
	e.Batch = true
	defer func() {
		e.Batch = false
		if serr := e.saveIndex(true); err == nil {
			err = serr
		}
	}()
```

Remove the `var r PullReport` line (now a named result), and replace the existing final `if err := e.Tree.SaveIndex(e.Index); err != nil { return r, err }` with nothing (the deferred save does it). Keep every other `return r, err` as it is; inner `err :=` shadowing is fine.

- [ ] **Step 3.8: Run the engine and CLI tests**

Run: `go test ./internal/engine/ ./internal/cli/ ./internal/workdir/`
Expected: PASS. `TestCloneBatchesIndexSaves` reports 3 saves or fewer.

- [ ] **Step 3.9: Commit**

```bash
git add internal/workdir internal/engine
git commit -m "engine: index path map; clone and pull batch index saves"
```

### Task 4: Engine hooks: FullDirs, Cacher, sorted repeated fields, reply and approve policy

Four small generic changes the Jira adapter needs (spec §6.5, §5.2 cache, §5.1 field order, §7.7).

**Files:**
- Modify: `internal/adapter/adapter.go` (`Listing`, new `Cacher`)
- Modify: `internal/engine/pull.go` (the `gone` computation after the listing loop)
- Modify: `internal/engine/clone.go`, `internal/cli/env.go` (call `UseCache`)
- Modify: `internal/canon/canon.go` (`normalizeChildren` sort)
- Modify: `internal/policy/policy.go` (`defaults`)
- Test: `internal/engine/pull_test.go`, `internal/canon/canon_test.go`, `internal/policy/policy_test.go`, `internal/adapter/fake/fake.go`

**Interfaces:**
- Consumes: Task 3 `Env`.
- Produces:
  - `adapter.Listing.FullDirs []string`
  - `type Cacher interface{ UseCache(dir string) }` (dir is `<tree>/.gfs/cache`, may not exist yet)
  - canon: a `schema.Field` with `Repeated: true` and `SortKey` set is sorted by that attribute (keyed before unkeyed, stable).
  - policy defaults `reply: ask`, `approve: ask`.
  - fake: `Remote.FullDirs []string` returned in every listing, `Remote.Partial bool` makes `List` return `Full: false`, `Remote.CacheDir string` records `UseCache`.

- [ ] **Step 4.1: Write the failing tests**

Append to `internal/engine/pull_test.go`:

```go
// A partial listing that covers folder a/ completely deletes a/ files it omits
// and leaves other folders alone.
func TestPullFullDirs(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Partial = true
	ad.Remote.FullDirs = []string{"a"}
	for _, e := range env.Index.All() {
		if strings.HasPrefix(e.Path, "a/") {
			ad.Remote.Delete(e.ID)
		}
	}
	gone := 0
	for _, e := range env.Index.All() {
		if strings.HasPrefix(e.Path, "a/") {
			gone++
		}
	}
	r := pull(t, env, PullOpts{})
	if r.Deleted != gone || gone == 0 {
		t.Fatalf("deleted %d of %d\n%s", r.Deleted, gone, out)
	}
	for _, e := range env.Index.All() {
		if strings.HasPrefix(e.Path, "a/") {
			t.Fatalf("%s still indexed", e.Path)
		}
	}
}

func TestCloneGivesCacheDir(t *testing.T) {
	env, ad, _ := cloned(t)
	if want := filepath.Join(env.Tree.Root, ".gfs", "cache"); ad.Remote.CacheDir != want {
		t.Fatalf("cache dir %q, want %q", ad.Remote.CacheDir, want)
	}
}
```

(Add `"path/filepath"` to the test's imports. `cloned` is the existing helper that clones the fake remote.)

Append to `internal/canon/canon_test.go`:

```go
func TestRepeatedFieldSortedByKey(t *testing.T) {
	s := &schema.Schema{Root: "r", Elems: []schema.Elem{
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id", Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}}},
	}}
	root, _ := xmltree.ParseString(`<r><field name="b" id="c2">2</field><field>new</field><field id="c1">1</field></r>`)
	Normalize(root, s)
	if got := xmltree.Print(root, 0); got != "<r>\n  <field id=\"c1\">1</field>\n  <field id=\"c2\" name=\"b\">2</field>\n  <field>new</field>\n</r>" {
		t.Fatal(got)
	}
}
```

Append to `internal/policy/policy_test.go`:

```go
func TestReplyAndApproveAsk(t *testing.T) {
	p, _ := FromConfig(nil)
	if p.Level("reply") != Ask || p.Level("approve") != Ask || p.Level("comment") != Allow {
		t.Fatal(p.Level("reply"), p.Level("approve"), p.Level("comment"))
	}
}
```

- [ ] **Step 4.2: Run them to make sure they fail**

Run: `go test ./internal/engine/ ./internal/canon/ ./internal/policy/ 2>&1 | grep -E "FAIL|undefined" | head`
Expected: `Remote.Partial undefined`, the canon test prints unsorted fields, the policy test fails on `reply`.

- [ ] **Step 4.3: Add `FullDirs` and `Cacher` to the adapter contract**

In `internal/adapter/adapter.go`, extend `Listing`:

```go
type Listing struct {
	Resources []Resource
	Deleted   []string
	Full      bool
	// FullDirs are top-level folders this listing covers completely even when
	// Full is false: an indexed resource under one of them that the listing
	// omits was deleted on the remote.
	FullDirs []string
	Cursor   string
}
```

and add:

```go
// Cacher is implemented by sessions that keep metadata between runs. The
// engine passes <tree>/.gfs/cache (possibly not yet created) before listing
// or applying; the session owns what it writes there.
type Cacher interface{ UseCache(dir string) }
```

- [ ] **Step 4.4: Honour `FullDirs` in pull**

In `internal/engine/pull.go`, find the block after the listing loop:

```go
	if l.Full {
		for _, en := range e.Index.All() {
			if !listed[en.ID] {
				gone[en.ID] = true
			}
		}
	}
```

and replace it with:

```go
	for _, en := range e.Index.All() {
		if !listed[en.ID] && (l.Full || underAny(en.Path, l.FullDirs)) {
			gone[en.ID] = true
		}
	}
```

plus, at the end of the file:

```go
// underAny reports whether p lies inside one of the top-level folders dirs.
func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}
```

(add `"strings"` to the imports if missing).

- [ ] **Step 4.5: Call `UseCache` in clone and when opening a tree**

In `internal/engine/clone.go`, right after `t, err := workdir.Init(dir, cfg)` and its error check:

```go
	if c, ok := sess.(adapter.Cacher); ok {
		c.UseCache(filepath.Join(t.Root, workdir.Dir, "cache"))
	}
```

(import `path/filepath`). In `internal/cli/env.go`, in `openEnv` right after `sess, err := ad.Open(...)` succeeds:

```go
	if c, ok := sess.(adapter.Cacher); ok {
		c.UseCache(filepath.Join(t.Root, workdir.Dir, "cache"))
	}
```

(import `path/filepath`).

- [ ] **Step 4.6: Teach the fake adapter the new knobs**

In `internal/adapter/fake/fake.go` add to `Remote`:

```go
	Partial  bool     // List returns Full: false
	FullDirs []string // returned as Listing.FullDirs
	CacheDir string   // last UseCache argument
```

change `List` to set `l := adapter.Listing{Full: !s.r.Partial, FullDirs: s.r.FullDirs, Cursor: "c1"}`, and add:

```go
func (s session) UseCache(dir string) { s.r.CacheDir = dir }
```

- [ ] **Step 4.7: Sort repeated fields in canon**

In `internal/canon/canon.go`, in `normalizeChildren`, the sort condition becomes:

```go
		kind := elems[i].Kind
		sortable := kind == schema.Sub || kind == schema.Attachment || (kind == schema.Field && elems[i].Repeated)
		if sortable && elems[i].SortKey != "" {
```

(the comparator body is unchanged: it already orders keyed before unkeyed and ties by id only for attachments). Also update the `SortKey` comment in `internal/schema/schema.go` to `// Sub, Attachment, repeated Field: attribute to sort by`.

- [ ] **Step 4.8: Default `reply` and `approve` to ask**

In `internal/policy/policy.go`:

```go
var defaults = map[string]string{"send": Ask, "delete": Ask, "publish": Ask, "reply": Ask, "approve": Ask}
```

- [ ] **Step 4.9: Run the suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4.10: Commit**

```bash
git add internal/adapter internal/engine internal/canon internal/schema internal/policy internal/cli/env.go
git commit -m "engine: listings that cover folders, session caches, sorted repeated fields; reply and approve ask"
```

### Task 5: `gfs actions <path>` and the Advisor interface

`gfs actions` with paths asks the session what the user may do to each file now (spec §8). The fake adapter implements it so the CLI can be tested before Jira exists.

**Files:**
- Modify: `internal/adapter/adapter.go` (new types)
- Modify: `internal/cli/actions.go`
- Modify: `internal/adapter/fake/fake.go`
- Test: `internal/cli/actions_test.go`

**Interfaces:**
- Consumes: `workdir.Tree.Rel`, `envelope.Parse`, `adapter.Schema.ID`.
- Produces:

```go
// Advisor is implemented by sessions that can say what the user may do to one
// resource now (jira spec §8). local is the file's current content.
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
	Element  string   // "resolution", "field[id=customfield_10040]"
	Required bool
	Allowed  []string // shown when there are at most 8
}
```

and `fake.Remote.Advice map[string]adapter.Advice`.

- [ ] **Step 5.1: Write the failing CLI test**

`internal/cli/actions_test.go`:

```go
package cli

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

func TestActionsForPaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "fake://x", "wt")
	t.Chdir(dir + "/wt")
	fk.Remote.Advice = map[string]adapter.Advice{"1": {
		State: "status: In Progress",
		Note:  `local <status> Done -> "Finish"`,
		Items: []adapter.Available{
			{Verb: "transition", Name: "Finish", To: "Done"},
			{Verb: "transition", Name: "Resolve this issue", To: "Closed", Fields: []adapter.AvailableField{
				{Element: "resolution", Required: true, Allowed: []string{"Done", "Won't Do"}},
				{Element: "link"},
			}},
		},
	}}
	t.Cleanup(func() { fk.Remote.Advice = nil })
	got := mustRun(t, 0, "actions", "a/one.xml")
	want := "a/one.xml    status: In Progress\n" +
		"  transition  Finish                    -> Done\n" +
		"  transition  Resolve this issue        -> Closed\n" +
		"                requires <resolution>: Done | Won't Do\n" +
		"                optional <link>\n" +
		"  note: local <status> Done -> \"Finish\"\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	writeFile(t, "a/new.xml", "<note><title>N</title></note>")
	mustContain(t, mustRun(t, 0, "actions", "a/new.xml"), "a/new.xml    not on the remote yet")
	mustContain(t, mustRun(t, 2, "actions", "a/missing.xml"), "a/missing.xml")
	if out := mustRun(t, 0, "actions"); !strings.Contains(out, "create  allow") {
		t.Fatalf("verb list changed:\n%s", out)
	}
}
```

If `writeFile` does not exist in the cli tests, add it to `actions_test.go`:

```go
func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

(import `os`).

- [ ] **Step 5.2: Run it to make sure it fails**

Run: `go test ./internal/cli/ -run TestActionsForPaths -v`
Expected: FAIL, `undefined: adapter.Advice`.

- [ ] **Step 5.3: Add the types to `internal/adapter/adapter.go`**

Paste the `Advisor`, `Advice`, `Available`, `AvailableField` declarations from the Interfaces block above.

- [ ] **Step 5.4: Let the fake advise**

In `internal/adapter/fake/fake.go` add `Advice map[string]adapter.Advice` to `Remote` and:

```go
func (s session) Available(_ context.Context, id string, _ *adapter.Resource) (adapter.Advice, error) {
	return s.r.Advice[id], nil
}
```

- [ ] **Step 5.5: Implement per-file actions in `internal/cli/actions.go`**

Change the command to `Use: "actions [<path>...]"`, `Short: "List the verbs this remote understands, or what you can do to each file now"`, and at the top of `RunE`, after `defer done()`:

```go
			if len(args) > 0 {
				return printAdvice(cmd, env, args)
			}
```

Add:

```go
// printAdvice asks the session, per file, what the user can do now (jira spec §8).
func printAdvice(cmd *cobra.Command, env *engine.Env, args []string) error {
	w := cmd.OutOrStdout()
	adv, ok := env.Session.(adapter.Advisor)
	failed := false
	for _, a := range args {
		rel, err := env.Tree.Rel(a)
		if err != nil {
			return usage("%v", err)
		}
		data, err := env.Tree.ReadFile(rel)
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		id, _ := doc.Content.Attr(env.Adapter.Schema().ID)
		switch {
		case id == "":
			fmt.Fprintf(w, "%s    not on the remote yet\n", rel)
			continue
		case !ok:
			fmt.Fprintf(w, "%s    no per-file actions for %s\n", rel, env.Adapter.Name())
			continue
		}
		advice, err := adv.Available(cmd.Context(), id, &adapter.Resource{ID: id, Path: rel, Root: doc.Content})
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		fmt.Fprintf(w, "%s    %s\n", rel, advice.State)
		for _, it := range advice.Items {
			fmt.Fprintf(w, "  %-10s  %-24s  -> %s\n", it.Verb, it.Name, it.To)
			for _, f := range it.Fields {
				switch {
				case f.Required && len(f.Allowed) > 0 && len(f.Allowed) <= 8:
					fmt.Fprintf(w, "                requires <%s>: %s\n", f.Element, strings.Join(f.Allowed, " | "))
				case f.Required:
					fmt.Fprintf(w, "                requires <%s>\n", f.Element)
				default:
					fmt.Fprintf(w, "                optional <%s>\n", f.Element)
				}
			}
		}
		if advice.Note != "" {
			fmt.Fprintf(w, "  note: %s\n", advice.Note)
		}
	}
	if failed {
		return &ExitError{Code: 2}
	}
	return nil
}
```

(imports: `internal/engine`, `internal/envelope`). The verb column pads to 10 and the name to 24, exactly as the test expects: `"  transition  Finish                    -> Done"`.

- [ ] **Step 5.6: Run the CLI tests**

Run: `go test ./internal/cli/ -v -run 'TestActions|TestCLIFlow'`
Expected: PASS. If the padding differs, fix the format string, not the test.

- [ ] **Step 5.7: Commit**

```bash
git add internal/adapter internal/cli
git commit -m "cli: gfs actions <path> asks the session what each file allows now"
```

### Task 6: ADF as XML

Jira rich text (ADF JSON) becomes element-per-node XML and back, losslessly (spec §5.4). The vocabulary comes from `@atlaskit/adf-schema` 57.6 (`dist/json-schema/v1/full.json`); only attributes whose JSON type is not a string need a table.

**Files:**
- Create: `internal/adapter/jira/adfspec.go`
- Create: `internal/adapter/jira/adf.go`
- Test: `internal/adapter/jira/adf_test.go`

**Interfaces:**
- Consumes: `xmltree.Node`, `xmltree.PrintInner`, `xmltree.ParseString`.
- Produces:
  - `const adfType = "application/vnd.atlassian.adf+xml"` (declared here; `schema.go` in Task 7 uses it)
  - `func adfToNodes(doc json.RawMessage) ([]*xmltree.Node, error)`: children for a body element; `null`/empty gives none.
  - `func nodesToADF(children []*xmltree.Node) (any, error)`: an ADF doc (`map[string]any` with `type`, `version`, `content`), or nil when there is no content.
  - `func el(name string, attrs ...string) *xmltree.Node` and `func textEl(name, text string) *xmltree.Node` (package helpers used by every later task; empty attribute values are skipped, as in Confluence).

- [ ] **Step 6.1: Write the failing tests**

`internal/adapter/jira/adf_test.go`:

```go
package jira

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

func doc(content string) string { return `{"type":"doc","version":1,"content":[` + content + `]}` }

func TestADFRoundTrip(t *testing.T) {
	cases := []struct {
		name, in, xml string
		back          string // expected JSON after the round trip, when it differs from in
	}{
		{"marks",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"Bursts above "},{"type":"text","text":"200 rps","marks":[{"type":"strong"}]},{"type":"text","text":" see "},{"type":"text","text":"runbook","marks":[{"type":"link","attrs":{"href":"https://x/r"}},{"type":"strong"}]}]}`),
			`<paragraph>Bursts above <strong>200 rps</strong> see <link href="https://x/r"><strong>runbook</strong></link></paragraph>`, ""},
		{"blocks",
			doc(`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Steps"}]},{"type":"orderedList","attrs":{"order":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]}]}`),
			"<heading level=\"2\">Steps</heading>\n<orderedList order=\"3\">\n  <listItem>\n    <paragraph>one</paragraph>\n  </listItem>\n</orderedList>", ""},
		{"cdata",
			doc(`{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"x ]]> y < z & w\nnext"}]}`),
			"<codeBlock language=\"go\"><![CDATA[x ]]]]><![CDATA[> y < z & w\nnext]]></codeBlock>", ""},
		{"mention space",
			doc(`{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"a1","text":"@Adam"}},{"type":"text","text":" "}]}`),
			"<paragraph>\n  <mention id=\"a1\" text=\"@Adam\"/>\n  <text> </text>\n</paragraph>", ""},
		{"table",
			doc(`{"type":"table","attrs":{"isNumberColumnEnabled":false,"layout":"default"},"content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"colspan":2,"colwidth":[120,80]},"content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}]}]}`),
			"<table isNumberColumnEnabled=\"false\" layout=\"default\">\n  <tableRow>\n    <tableCell colspan=\"2\" colwidth=\"[120,80]\">\n      <paragraph>a</paragraph>\n    </tableCell>\n  </tableRow>\n</table>", ""},
		{"block mark",
			doc(`{"type":"paragraph","marks":[{"type":"alignment","attrs":{"align":"center"}}],"content":[{"type":"text","text":"c"}]}`),
			"<alignment align=\"center\">\n  <paragraph>c</paragraph>\n</alignment>", ""},
		{"media",
			doc(`{"type":"mediaSingle","attrs":{"layout":"center"},"content":[{"type":"media","attrs":{"type":"file","id":"552b","collection":"","height":183,"width":200}}]}`),
			"<mediaSingle layout=\"center\">\n  <media collection=\"\" height=\"183\" id=\"552b\" type=\"file\" width=\"200\"/>\n</mediaSingle>", ""},
		{"unknown node",
			doc(`{"type":"futureThing","attrs":{"x":1}}`),
			`<adf-raw>{"type":"futureThing","attrs":{"x":1}}</adf-raw>`, ""},
		{"attribute type mismatch",
			doc(`{"type":"heading","attrs":{"level":"2"},"content":[{"type":"text","text":"x"}]}`),
			`<adf-raw>{"type":"heading","attrs":{"level":"2"},"content":[{"type":"text","text":"x"}]}</adf-raw>`, ""},
		{"unknown mark",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"a "},{"type":"text","text":"b","marks":[{"type":"sparkle"}]}]}`),
			`<paragraph>a <adf-raw>{"type":"text","text":"b","marks":[{"type":"sparkle"}]}</adf-raw></paragraph>`, ""},
		{"null attribute dropped",
			doc(`{"type":"codeBlock","attrs":{"language":null},"content":[{"type":"text","text":"x"}]}`),
			`<codeBlock>x</codeBlock>`,
			doc(`{"type":"codeBlock","content":[{"type":"text","text":"x"}]}`)},
		{"adjacent text merged",
			doc(`{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"text","text":"c","marks":[{"type":"em"}]},{"type":"text","text":"d","marks":[{"type":"em"}]}]}`),
			`<paragraph>ab<em>cd</em></paragraph>`,
			doc(`{"type":"paragraph","content":[{"type":"text","text":"ab"},{"type":"text","text":"cd","marks":[{"type":"em"}]}]}`)},
	}
	for _, c := range cases {
		nodes, err := adfToNodes(json.RawMessage(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		body := &xmltree.Node{Kind: xmltree.Element, Name: "description", Children: nodes}
		if got := xmltree.PrintInner(body); got != c.xml {
			t.Errorf("%s: xml\n%s\nwant\n%s", c.name, got, c.xml)
			continue
		}
		// what a file holds: the body printed at some depth, parsed back
		parsed, err := xmltree.ParseString(xmltree.Print(body, 3))
		if err != nil {
			t.Errorf("%s: reparse: %v", c.name, err)
			continue
		}
		v, err := nodesToADF(parsed.Children)
		if err != nil {
			t.Errorf("%s: back: %v", c.name, err)
			continue
		}
		got, _ := json.Marshal(v)
		want := c.back
		if want == "" {
			want = c.in
		}
		if !jsonEqual(t, got, []byte(want)) {
			t.Errorf("%s: back\n%s\nwant\n%s", c.name, got, want)
		}
	}
}

func TestADFEmpty(t *testing.T) {
	if n, err := adfToNodes(json.RawMessage("null")); n != nil || err != nil {
		t.Fatal(n, err)
	}
	if v, err := nodesToADF(nil); v != nil || err != nil {
		t.Fatal(v, err)
	}
	ws, _ := xmltree.ParseString("<d>\n  \n</d>")
	if v, err := nodesToADF(ws.Children); v != nil || err != nil {
		t.Fatal(v, err)
	}
}

func TestADFFromFileErrors(t *testing.T) {
	cases := map[string]string{
		`<d><blink>x</blink></d>`:                       "unknown ADF element <blink>",
		`<d><heading level="two">x</heading></d>`:       `<heading level="two">: want a number`,
		`<d><table isNumberColumnEnabled="yes"/></d>`:   `<table isNumberColumnEnabled="yes">: want true or false`,
		`<d><strong><adf-raw>{}</adf-raw></strong></d>`: "<adf-raw> cannot be inside a mark",
		`<d><adf-raw>{nope</adf-raw></d>`:               "<adf-raw>:",
		`<d>plain words</d>`:                            "text outside a block: wrap it in <paragraph>",
	}
	for in, want := range cases {
		n, _ := xmltree.ParseString(in)
		if _, err := nodesToADF(n.Children); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", in, err, want)
		}
	}
}

func TestADFUserWrittenInline(t *testing.T) {
	// what a user types: wrappers nested their own way, whitespace between blocks
	n, _ := xmltree.ParseString("<d>\n  <paragraph>Hi <strong>big <em>world</em></strong></paragraph>\n  <rule/>\n</d>")
	v, err := nodesToADF(n.Children)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(v)
	want := doc(`{"type":"paragraph","content":[{"type":"text","text":"Hi "},{"type":"text","text":"big ","marks":[{"type":"strong"}]},{"type":"text","text":"world","marks":[{"type":"strong"},{"type":"em"}]}]},{"type":"rule"}`)
	if !jsonEqual(t, got, []byte(want)) {
		t.Fatalf("%s", got)
	}
}
```

- [ ] **Step 6.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run TestADF -v`
Expected: FAIL, `undefined: adfToNodes` (the package does not exist yet; that is fine, the test file creates it).

- [ ] **Step 6.3: Write `adfspec.go`**

```go
package jira

// ADF vocabulary from @atlaskit/adf-schema 57.6 (dist/json-schema/v1/full.json).
// Nodes and marks not listed here round-trip as <adf-raw> (jira spec §5.4 rule 6).

const adfType = "application/vnd.atlassian.adf+xml"

var adfNodes = map[string]bool{
	"blockCard": true, "blockquote": true, "blockTaskItem": true, "bodiedExtension": true, "bodiedSyncBlock": true,
	"bulletList": true, "caption": true, "codeBlock": true, "date": true, "decisionItem": true, "decisionList": true,
	"embedCard": true, "emoji": true, "expand": true, "extension": true, "hardBreak": true, "heading": true,
	"inlineCard": true, "inlineExtension": true, "layoutColumn": true, "layoutSection": true, "listItem": true,
	"media": true, "mediaGroup": true, "mediaInline": true, "mediaSingle": true, "mention": true, "nestedExpand": true,
	"orderedList": true, "panel": true, "paragraph": true, "placeholder": true, "rule": true, "status": true,
	"syncBlock": true, "table": true, "tableCell": true, "tableHeader": true, "tableRow": true, "taskItem": true,
	"taskList": true,
}

// adfMarks lists mark types in wrapper order, outermost first. No mark type
// is also a node type, so a wrapper element is never ambiguous.
var adfMarks = []string{"link", "annotation", "dataConsumer", "fragment", "border", "alignment", "indentation", "breakout",
	"strong", "em", "underline", "strike", "code", "subsup", "textColor", "backgroundColor", "fontSize"}

type attrKind int

const (
	kString attrKind = iota // the default: every attribute not listed below
	kNumber
	kBool
	kJSON // object, array or any value: kept as compact JSON text
)

// attrKinds lists the attributes whose JSON type is not a string.
var attrKinds = map[string]map[string]attrKind{
	"blockCard":       {"data": kJSON, "datasource": kJSON, "parameters": kJSON, "properties": kJSON, "views": kJSON, "width": kNumber},
	"bodiedExtension": {"parameters": kJSON},
	"border":          {"size": kNumber},
	"breakout":        {"width": kNumber},
	"codeBlock":       {"hideLineNumbers": kBool, "wrap": kBool},
	"dataConsumer":    {"sources": kJSON},
	"embedCard":       {"originalHeight": kNumber, "originalWidth": kNumber, "width": kNumber},
	"extension":       {"parameters": kJSON},
	"heading":         {"level": kNumber},
	"indentation":     {"level": kNumber},
	"inlineCard":      {"data": kJSON},
	"inlineExtension": {"parameters": kJSON},
	"layoutColumn":    {"width": kNumber},
	"media":           {"height": kNumber, "width": kNumber},
	"mediaInline":     {"data": kJSON, "height": kNumber, "width": kNumber},
	"mediaSingle":     {"width": kNumber},
	"orderedList":     {"order": kNumber},
	"table":           {"isNumberColumnEnabled": kBool, "width": kNumber},
	"tableCell":       {"colspan": kNumber, "colwidth": kJSON, "rowspan": kNumber},
	"tableHeader":     {"colspan": kNumber, "colwidth": kJSON, "rowspan": kNumber},
}
```

- [ ] **Step 6.4: Write `adf.go`**

```go
package jira

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// adfRaw holds, as JSON text, an ADF node gfs does not model.
const adfRaw = "adf-raw"

var markRank = func() map[string]int {
	m := map[string]int{}
	for i, t := range adfMarks {
		m[t] = i
	}
	return m
}()

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], attrs[i+1])
		}
	}
	return n
}

func textEl(name, text string) *xmltree.Node {
	n := el(name)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
	}
	return n
}

// adfToNodes renders the content of an ADF doc as XML nodes (jira spec §5.4).
func adfToNodes(doc json.RawMessage) ([]*xmltree.Node, error) {
	doc = bytes.TrimSpace(doc)
	if len(doc) == 0 || string(doc) == "null" {
		return nil, nil
	}
	var d struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("ADF: %w", err)
	}
	if d.Type != "doc" {
		return nil, fmt.Errorf("ADF: root node is %q, want doc", d.Type)
	}
	return adfContent(d.Content)
}

type adfMark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs,omitempty"`
}

// item is one content entry before its marks become wrappers.
type item struct {
	text  *string       // a text run, or
	node  *xmltree.Node // an element
	marks []adfMark     // in wrapper order
	key   string        // identity of marks, for merging runs
}

func adfContent(raws []json.RawMessage) ([]*xmltree.Node, error) {
	var items []item
	for _, raw := range raws {
		it, err := adfItem(raw)
		if err != nil {
			return nil, err
		}
		if n := len(items); n > 0 && it.text != nil && items[n-1].text != nil && items[n-1].key == it.key {
			joined := *items[n-1].text + *it.text
			items[n-1].text = &joined
			continue
		}
		items = append(items, it)
	}
	out := make([]*xmltree.Node, 0, len(items))
	for _, it := range items {
		n := it.node
		if it.text != nil {
			n = &xmltree.Node{Kind: xmltree.Text, Text: *it.text}
		}
		for i := len(it.marks) - 1; i >= 0; i-- {
			w, _ := markElement(it.marks[i]) // validated by adfItem
			w.Children = []*xmltree.Node{n}
			n = w
		}
		out = append(out, n)
	}
	return keepSpaces(out), nil
}

// keepSpaces turns whitespace-only text into <text> where the printer would
// drop it: next to elements, with no other text.
func keepSpaces(ns []*xmltree.Node) []*xmltree.Node {
	if !elementOnly(ns) {
		return ns
	}
	for i, n := range ns {
		if n.Kind == xmltree.Text {
			ns[i] = &xmltree.Node{Kind: xmltree.Element, Name: "text", Children: []*xmltree.Node{n}}
		}
	}
	return ns
}

// elementOnly mirrors the printer: elements and only whitespace text.
func elementOnly(ns []*xmltree.Node) bool {
	hasElem := false
	for _, n := range ns {
		switch n.Kind {
		case xmltree.Element:
			hasElem = true
		case xmltree.Text:
			if strings.TrimSpace(n.Text) != "" {
				return false
			}
		}
	}
	return hasElem
}

var nodeKeys = map[string]bool{"type": true, "attrs": true, "content": true, "marks": true, "text": true}

func adfItem(raw json.RawMessage) (item, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return item{}, fmt.Errorf("ADF: %w", err)
	}
	var typ string
	json.Unmarshal(m["type"], &typ)
	it, ok, err := modelled(typ, m)
	if err != nil {
		return item{}, err
	}
	if ok {
		return it, nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return item{}, fmt.Errorf("ADF: %w", err)
	}
	return item{node: &xmltree.Node{Kind: xmltree.Element, Name: adfRaw,
		Children: []*xmltree.Node{{Kind: xmltree.Text, Text: b.String()}}}}, nil
}

// modelled decodes a node the tables describe; ok is false when any part of
// it (key, node type, mark, attribute type) is outside them.
func modelled(typ string, m map[string]json.RawMessage) (item, bool, error) {
	for k := range m {
		if !nodeKeys[k] {
			return item{}, false, nil
		}
	}
	var marks []adfMark
	if raw, has := m["marks"]; has {
		if json.Unmarshal(raw, &marks) != nil {
			return item{}, false, nil
		}
		for _, mk := range marks {
			if _, known := markRank[mk.Type]; !known {
				return item{}, false, nil
			}
			if _, ok := markElement(mk); !ok {
				return item{}, false, nil
			}
		}
		sort.SliceStable(marks, func(i, j int) bool { return markRank[marks[i].Type] < markRank[marks[j].Type] })
	}
	key, _ := json.Marshal(marks)
	if typ == "text" {
		var s string
		if json.Unmarshal(m["text"], &s) != nil || m["attrs"] != nil || m["content"] != nil {
			return item{}, false, nil
		}
		return item{text: &s, marks: marks, key: string(key)}, true, nil
	}
	if !adfNodes[typ] || m["text"] != nil {
		return item{}, false, nil
	}
	n := el(typ)
	var attrs map[string]json.RawMessage
	if raw, has := m["attrs"]; has && json.Unmarshal(raw, &attrs) != nil {
		return item{}, false, nil
	}
	if !setAttrs(n, typ, attrs) {
		return item{}, false, nil
	}
	if raw, has := m["content"]; has {
		var kids []json.RawMessage
		if json.Unmarshal(raw, &kids) != nil {
			return item{}, false, nil
		}
		var err error
		if n.Children, err = adfContent(kids); err != nil {
			return item{}, false, err
		}
	}
	return item{node: n, marks: marks, key: string(key)}, true, nil
}

func markElement(mk adfMark) (*xmltree.Node, bool) {
	n := el(mk.Type)
	return n, setAttrs(n, mk.Type, mk.Attrs)
}

// setAttrs writes attrs as XML attributes in name order; false on a value
// whose JSON type the tables do not allow.
func setAttrs(n *xmltree.Node, owner string, attrs map[string]json.RawMessage) bool {
	names := make([]string, 0, len(attrs))
	for k := range attrs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v, keep, ok := attrToXML(owner, k, attrs[k])
		if !ok {
			return false
		}
		if keep {
			n.Attrs = append(n.Attrs, xmltree.Attr{Name: k, Value: v})
		}
	}
	return true
}

// attrToXML renders one attribute value; keep is false for null (dropped).
func attrToXML(owner, name string, raw json.RawMessage) (v string, keep, ok bool) {
	t := bytes.TrimSpace(raw)
	if string(t) == "null" {
		return "", false, true
	}
	switch kind := attrKinds[owner][name]; {
	case kind == kJSON:
		var b bytes.Buffer
		if json.Compact(&b, t) != nil {
			return "", false, false
		}
		return b.String(), true, true
	case len(t) > 0 && t[0] == '"':
		var s string
		if kind != kString || json.Unmarshal(t, &s) != nil {
			return "", false, false
		}
		return s, true, true
	case kind == kNumber:
		if _, err := strconv.ParseFloat(string(t), 64); err != nil {
			return "", false, false
		}
		return string(t), true, true
	case kind == kBool && (string(t) == "true" || string(t) == "false"):
		return string(t), true, true
	}
	return "", false, false
}

// nodesToADF turns the children of a body element back into an ADF doc; no
// content gives nil, which clears the field.
func nodesToADF(children []*xmltree.Node) (any, error) {
	var blocks []*xmltree.Node
	for _, c := range children {
		if c.Kind == xmltree.Text {
			if strings.TrimSpace(c.Text) != "" {
				return nil, errors.New("text outside a block: wrap it in <paragraph>")
			}
			continue
		}
		blocks = append(blocks, c)
	}
	content, err := toADF(blocks, nil)
	if err != nil || len(content) == 0 {
		return nil, err
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}, nil
}

func toADF(ns []*xmltree.Node, marks []any) ([]any, error) {
	skipSpace := elementOnly(ns)
	var out []any
	for _, n := range ns {
		switch n.Kind {
		case xmltree.Text:
			if skipSpace && strings.TrimSpace(n.Text) == "" {
				continue
			}
			out = appendMerged(out, textNode(n.Text, marks))
		case xmltree.Element:
			switch {
			case n.Name == "text":
				out = appendMerged(out, textNode(n.TextContent(), marks))
			case n.Name == adfRaw:
				if len(marks) > 0 {
					return nil, fmt.Errorf("<%s> cannot be inside a mark", adfRaw)
				}
				var v any
				if err := json.Unmarshal([]byte(strings.TrimSpace(n.TextContent())), &v); err != nil {
					return nil, fmt.Errorf("<%s>: %w", adfRaw, err)
				}
				out = append(out, v)
			case hasRank(n.Name):
				m := map[string]any{"type": n.Name}
				attrs, err := attrsToADF(n.Name, n.Attrs)
				if err != nil {
					return nil, err
				}
				if len(attrs) > 0 {
					m["attrs"] = attrs
				}
				inner, err := toADF(n.Children, append(append([]any(nil), marks...), m))
				if err != nil {
					return nil, err
				}
				for _, x := range inner {
					out = appendMerged(out, x)
				}
			case adfNodes[n.Name]:
				node := map[string]any{"type": n.Name}
				attrs, err := attrsToADF(n.Name, n.Attrs)
				if err != nil {
					return nil, err
				}
				if len(attrs) > 0 {
					node["attrs"] = attrs
				}
				kids, err := toADF(n.Children, nil)
				if err != nil {
					return nil, err
				}
				if len(kids) > 0 {
					node["content"] = kids
				}
				if len(marks) > 0 {
					node["marks"] = append([]any(nil), marks...)
				}
				out = append(out, node)
			default:
				return nil, fmt.Errorf("unknown ADF element <%s>", n.Name)
			}
		}
	}
	return out, nil
}

func hasRank(name string) bool { _, ok := markRank[name]; return ok }

func textNode(s string, marks []any) map[string]any {
	t := map[string]any{"type": "text", "text": s}
	if len(marks) > 0 {
		t["marks"] = append([]any(nil), marks...)
	}
	return t
}

// appendMerged appends x, joining it to a preceding text node with the same marks.
func appendMerged(out []any, x any) []any {
	xm, ok := x.(map[string]any)
	if n := len(out); ok && n > 0 && xm["type"] == "text" {
		if prev, ok := out[n-1].(map[string]any); ok && prev["type"] == "text" {
			a, _ := json.Marshal(prev["marks"])
			b, _ := json.Marshal(xm["marks"])
			if bytes.Equal(a, b) {
				prev["text"] = prev["text"].(string) + xm["text"].(string)
				return out
			}
		}
	}
	return append(out, x)
}

func attrsToADF(owner string, attrs []xmltree.Attr) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range attrs {
		switch attrKinds[owner][a.Name] {
		case kNumber:
			if _, err := strconv.ParseFloat(a.Value, 64); err != nil {
				return nil, fmt.Errorf("<%s %s=%q>: want a number", owner, a.Name, a.Value)
			}
			out[a.Name] = json.Number(a.Value)
		case kBool:
			if a.Value != "true" && a.Value != "false" {
				return nil, fmt.Errorf("<%s %s=%q>: want true or false", owner, a.Name, a.Value)
			}
			out[a.Name] = a.Value == "true"
		case kJSON:
			if !json.Valid([]byte(a.Value)) {
				return nil, fmt.Errorf("<%s %s=%q>: want JSON", owner, a.Name, a.Value)
			}
			out[a.Name] = json.RawMessage(a.Value)
		default:
			out[a.Name] = a.Value
		}
	}
	return out, nil
}
```

- [ ] **Step 6.5: Run the tests**

Run: `go test ./internal/adapter/jira/ -run TestADF -v`
Expected: PASS. If `cdata` fails, check `xmltree.writeText` output against the expected string; it splits `]]>` as `]]]]><![CDATA[>`.

- [ ] **Step 6.6: Commit**

```bash
git add internal/adapter/jira/adf.go internal/adapter/jira/adfspec.go internal/adapter/jira/adf_test.go
git commit -m "jira: ADF as XML, lossless both ways"
```

### Task 7: Jira URLs, selection and schemas

Both remote forms and their parameters (spec §3), and the `<issue>` / `<request>` schemas (spec §5.1, §5.10). Pure code; nothing is registered yet.

**Files:**
- Create: `internal/adapter/jira/url.go`
- Create: `internal/adapter/jira/schema.go`
- Test: `internal/adapter/jira/url_test.go`, `internal/adapter/jira/schema_test.go`

**Interfaces:**
- Consumes: `atlassian.Creds` (Task 1), `creds.Lookup`, `adfType` (Task 6).
- Produces:
  - `type selection struct{ keys, exclude []string; since string; limit int }`
  - `type custSelection struct{ desks []string; ownership, status string }`
  - `type target struct{ base, host, email, token string; sel selection; csel custSelection }`
  - `func normalize(u *url.URL) (*url.URL, error)`: both schemes; the result's query holds only known parameters in a fixed order.
  - `func parseSelection(q url.Values) (selection, error)`, `func parseCustSelection(q url.Values) (custSelection, error)`
  - `func sinceDate(since string, now time.Time) string`: the JQL date for `since`.
  - `func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error)`
  - `func defaultDir(u *url.URL) string`
  - `var issueSchema, requestSchema *schema.Schema`; `const wikiType = "text/x-jira-wiki"`
  - test helper `func parse(t *testing.T, s string) *xmltree.Node` (in `schema_test.go`, reused by later tests)

- [ ] **Step 7.1: Write the failing URL tests**

`internal/adapter/jira/url_test.go`:

```go
package jira

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want, err string }{
		{"site", "jira://acme.atlassian.net", "jira://acme.atlassian.net", ""},
		{"path key", "jira://acme.atlassian.net/GEN", "jira://acme.atlassian.net?filter=GEN", ""},
		{"lower key", "jira://acme.atlassian.net/gen", "jira://acme.atlassian.net?filter=GEN", ""},
		{"params ordered", "jira://me%40x.com@acme.atlassian.net?limit=5k&since=-90d&exclude=OLD&filter=GEN,SUP", "jira://me%40x.com@acme.atlassian.net?filter=GEN,SUP&exclude=OLD&since=-90d&limit=5k", ""},
		{"browse issue", "jira:https://acme.atlassian.net/browse/GEN-123", "jira://acme.atlassian.net?filter=GEN", ""},
		{"browse project", "jira:https://acme.atlassian.net/browse/GEN", "jira://acme.atlassian.net?filter=GEN", ""},
		{"company board", "jira:https://acme.atlassian.net/jira/software/c/projects/GEN/boards/1?selectedIssue=GEN-5", "jira://acme.atlassian.net?filter=GEN", ""},
		{"team board", "jira:https://acme.atlassian.net/jira/software/projects/SG/boards/2", "jira://acme.atlassian.net?filter=SG", ""},
		{"queues", "jira:https://acme.atlassian.net/jira/servicedesk/projects/SUP/queues/custom/1", "jira://acme.atlassian.net?filter=SUP", ""},
		{"projects page", "jira:https://acme.atlassian.net/projects/GEN/issues", "jira://acme.atlassian.net?filter=GEN", ""},
		{"your work", "jira:https://acme.atlassian.net/jira/your-work", "jira://acme.atlassian.net", ""},
		{"browser base kept", "jira:https://acme.atlassian.net/browse/GEN-1?base=http://127.0.0.1:9", "jira://acme.atlassian.net?base=http%3A%2F%2F127.0.0.1%3A9&filter=GEN", ""},
		{"wiki is not jira", "jira:https://acme.atlassian.net/wiki/spaces/HF", "", "want jira://<site>"},
		{"deep path", "jira://acme.atlassian.net/GEN/x", "", "want jira://<site>"},
		{"key twice", "jira://acme.atlassian.net/GEN?filter=SUP", "", "not both"},
		{"unknown param", "jira://acme.atlassian.net?type=all", "", `unknown parameter "type"`},
		{"bad since", "jira://acme.atlassian.net?since=yesterday", "", "since=yesterday: want YYYY-MM-DD"},
		{"bad limit", "jira://acme.atlassian.net?limit=lots", "", "limit=lots: want a number"},
		{"bad key", "jira://acme.atlassian.net?filter=1ABC", "", `"1ABC" is not a project key`},
		{"customer site", "jira+customer://ecosystem.atlassian.net", "jira+customer://ecosystem.atlassian.net", ""},
		{"customer params", "jira+customer://ecosystem.atlassian.net?status=open&desk=34,12&ownership=owned", "jira+customer://ecosystem.atlassian.net?desk=34,12&ownership=owned&status=open", ""},
		{"customer requests page", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/user/requests?page=2", "jira+customer://ecosystem.atlassian.net", ""},
		{"customer portal", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/34/group/41", "jira+customer://ecosystem.atlassian.net?desk=34", ""},
		{"customer bad portal", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/x", "", "want jira+customer://<site>"},
		{"customer path", "jira+customer://ecosystem.atlassian.net/ECOHELP", "", "want jira+customer://<site>"},
		{"customer bad ownership", "jira+customer://e.atlassian.net?ownership=mine", "", "ownership=mine: want owned or all"},
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
	q, _ := url.ParseQuery("filter=gen,SUP&exclude=OLD&since=2023-01-01&limit=5k")
	got, err := parseSelection(q)
	want := selection{keys: []string{"GEN", "SUP"}, exclude: []string{"OLD"}, since: "2023-01-01", limit: 5000}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	q, _ = url.ParseQuery("desk=34")
	cs, err := parseCustSelection(q)
	if err != nil || !reflect.DeepEqual(cs, custSelection{desks: []string{"34"}, ownership: "all", status: "all"}) {
		t.Fatalf("%+v %v", cs, err)
	}
}

func TestSinceDate(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"2023-01-01": "2023-01-01", "-90d": "-90d", "-2w": "-2w", "-6m": "2026-03-29"} {
		if got := sinceDate(in, now); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

type lookup struct{}

func (lookup) Token(e string) (string, error) {
	if e == "me@x.com" {
		return "tok", nil
	}
	return "", creds.ErrNotFound
}
func (lookup) HostEmail(string) (string, error) { return "me@x.com", nil }
func (lookup) SoleIdentity() string             { return "" }

func TestParseTargetAndDefaultDir(t *testing.T) {
	u, _ := url.Parse("jira:https://acme.atlassian.net/browse/GEN-1")
	tg, err := parseTarget(u, map[string]string{"base": "http://b/"}, func(string) string { return "" }, lookup{})
	if err != nil || tg.base != "http://b" || tg.email != "me@x.com" || tg.token != "tok" || !reflect.DeepEqual(tg.sel.keys, []string{"GEN"}) {
		t.Fatalf("%+v %v", tg, err)
	}
	u, _ = url.Parse("jira+customer://ecosystem.atlassian.net?desk=34")
	tg, err = parseTarget(u, nil, func(k string) string { return map[string]string{"GFS_JIRA_TOKEN": "e"}[k] }, lookup{})
	if err != nil || tg.base != "https://ecosystem.atlassian.net" || tg.token != "e" || tg.csel.ownership != "all" {
		t.Fatalf("%+v %v", tg, err)
	}
	for in, want := range map[string]string{
		"jira://acme.atlassian.net/GEN":                  "gen",
		"jira://acme.atlassian.net?filter=GEN,SUP":       "acme",
		"jira+customer://ecosystem.atlassian.net?desk=3": "ecosystem",
		"jira://": "jira",
	} {
		u, _ := url.Parse(in)
		if got := defaultDir(u); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 7.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestNormalize|TestParseSelection|TestSinceDate|TestParseTarget' -v`
Expected: FAIL, `undefined: normalize`.

- [ ] **Step 7.3: Implement `url.go`**

```go
package jira

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// selection is which projects an agent-mode tree holds (jira spec §3.1).
type selection struct {
	keys, exclude []string
	since         string // "", "2023-01-01", "-90d", "-2w", "-6m"
	limit         int    // most recently updated issues per project; 0 = all
}

// custSelection is which requests a customer-mode tree holds (jira spec §3.2).
type custSelection struct {
	desks     []string // portal ids; none = every desk
	ownership string   // "owned" or "all"
	status    string   // "open" or "all"
}

type target struct {
	base, host, email, token string
	sel                      selection
	csel                     custSelection
}

const (
	agentForms    = "want jira://<site>[?filter=K1,K2&exclude=K3&since=<date>&limit=<n>], jira://<site>/<KEY>, or a browser URL jira:https://<site>/browse/<KEY>-<n>"
	customerForms = "want jira+customer://<site>[?desk=<id>,<id>&ownership=owned|all&status=open|all], or jira+customer:https://<site>/servicedesk/customer/…"
)

var (
	keyRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	issueRe = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)-\d+$`)
	sinceRe = regexp.MustCompile(`^-(\d+)([dwm])$`)
	limitRe = regexp.MustCompile(`^(\d+)(k?)$`)
	deskRe  = regexp.MustCompile(`^\d+$`)
)

func split(v string) []string {
	var out []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// encodeQuery writes the parameters in a fixed order and leaves commas readable.
func encodeQuery(q url.Values, order ...string) string {
	var parts []string
	for _, k := range order {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v), "%2C", ","))
		}
	}
	return strings.Join(parts, "&")
}

// browserURL parses the https URL inside jira:https://… or jira+customer:https://….
func browserURL(u *url.URL) (*url.URL, []string, bool) {
	in, err := url.Parse(u.Opaque)
	if err != nil || (in.Scheme != "https" && in.Scheme != "http") || in.Host == "" {
		return nil, nil, false
	}
	return in, strings.Split(strings.Trim(in.Path, "/"), "/"), true
}

// normalizeAgent rewrites every accepted agent-mode form as
// jira://[user@]<site>?<parameters> (jira spec §3.1).
func normalizeAgent(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), agentForms) }
	out := &url.URL{Scheme: "jira"}
	q := url.Values{}
	if u.Opaque != "" {
		in, segs, ok := browserURL(u)
		if !ok {
			return bad()
		}
		key, ok := browserProject(segs)
		if !ok {
			return bad()
		}
		if key != "" {
			q.Set("filter", key)
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
			case "base", "filter", "exclude", "since", "limit":
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
			return nil, fmt.Errorf("bad remote %q: name the project in the path or in filter, not both", u.String())
		default:
			q.Set("filter", strings.ToUpper(key))
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q, "base", "filter", "exclude", "since", "limit")
	return out, nil
}

// browserProject reads the project key from a Jira page path; "" means the
// whole site, false a path that names neither.
func browserProject(segs []string) (string, bool) {
	switch {
	case len(segs) == 2 && segs[0] == "browse":
		up := strings.ToUpper(segs[1])
		if m := issueRe.FindStringSubmatch(up); m != nil {
			return m[1], true
		}
		return up, keyRe.MatchString(up)
	case len(segs) >= 2 && segs[0] == "projects":
		up := strings.ToUpper(segs[1])
		return up, keyRe.MatchString(up)
	case segs[0] == "jira":
		for i := 1; i+1 < len(segs); i++ {
			if segs[i] == "projects" {
				up := strings.ToUpper(segs[i+1])
				return up, keyRe.MatchString(up)
			}
		}
		return "", true
	}
	return "", false
}

func parseSelection(q url.Values) (selection, error) {
	up := func(ks []string) []string {
		for i := range ks {
			ks[i] = strings.ToUpper(ks[i])
		}
		return ks
	}
	sel := selection{keys: up(split(q.Get("filter"))), exclude: up(split(q.Get("exclude"))), since: q.Get("since")}
	for _, k := range append(append([]string(nil), sel.keys...), sel.exclude...) {
		if !keyRe.MatchString(k) {
			return selection{}, fmt.Errorf("%q is not a project key", k)
		}
	}
	if s := sel.since; s != "" && !sinceRe.MatchString(s) {
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return selection{}, fmt.Errorf("since=%s: want YYYY-MM-DD, or -<n>d, -<n>w, -<n>m (days, weeks, months)", s)
		}
	}
	if l := q.Get("limit"); l != "" {
		m := limitRe.FindStringSubmatch(l)
		if m == nil {
			return selection{}, fmt.Errorf("limit=%s: want a number of issues, e.g. 5000 or 5k", l)
		}
		n, _ := strconv.Atoi(m[1])
		if m[2] == "k" {
			n *= 1000
		}
		if n == 0 {
			return selection{}, errors.New("limit=0: want at least 1")
		}
		sel.limit = n
	}
	return sel, nil
}

// sinceDate is the JQL date for since at now: relative days and weeks stay
// relative, months (which JQL lacks) become a date.
func sinceDate(since string, now time.Time) string {
	m := sinceRe.FindStringSubmatch(since)
	if m == nil {
		return since
	}
	if m[2] == "m" {
		n, _ := strconv.Atoi(m[1])
		return now.AddDate(0, -n, 0).Format("2006-01-02")
	}
	return since
}

// normalizeCustomer rewrites every accepted customer-mode form as
// jira+customer://[user@]<site>?<parameters> (jira spec §3.2).
func normalizeCustomer(u *url.URL) (*url.URL, error) {
	bad := func() (*url.URL, error) { return nil, fmt.Errorf("bad remote %q: %s", u.String(), customerForms) }
	out := &url.URL{Scheme: "jira+customer"}
	q := url.Values{}
	if u.Opaque != "" {
		in, segs, ok := browserURL(u)
		if !ok || len(segs) < 2 || segs[0] != "servicedesk" || segs[1] != "customer" {
			return bad()
		}
		if len(segs) >= 3 && segs[2] == "portal" {
			if len(segs) < 4 || !deskRe.MatchString(segs[3]) {
				return bad()
			}
			q.Set("desk", segs[3])
		}
		if b := u.Query().Get("base"); b != "" {
			q.Set("base", b)
		}
		out.User, out.Host = in.User, in.Host
	} else {
		if u.Host == "" || strings.Trim(u.Path, "/") != "" {
			return bad()
		}
		for k, vs := range u.Query() {
			switch k {
			case "base", "desk", "ownership", "status":
				q.Set(k, vs[len(vs)-1])
			default:
				return nil, fmt.Errorf("bad remote %q: unknown parameter %q", u.String(), k)
			}
		}
		out.User, out.Host = u.User, u.Host
	}
	out.RawQuery = encodeQuery(q, "base", "desk", "ownership", "status")
	return out, nil
}

func parseCustSelection(q url.Values) (custSelection, error) {
	sel := custSelection{desks: split(q.Get("desk")), ownership: q.Get("ownership"), status: q.Get("status")}
	for _, d := range sel.desks {
		if !deskRe.MatchString(d) {
			return custSelection{}, fmt.Errorf("desk=%s: want portal ids, e.g. desk=34", d)
		}
	}
	switch sel.ownership {
	case "":
		sel.ownership = "all"
	case "owned", "all":
	default:
		return custSelection{}, fmt.Errorf("ownership=%s: want owned or all", sel.ownership)
	}
	switch sel.status {
	case "":
		sel.status = "all"
	case "open", "all":
	default:
		return custSelection{}, fmt.Errorf("status=%s: want open or all", sel.status)
	}
	return sel, nil
}

// normalize dispatches on the scheme and checks the parameters.
func normalize(u *url.URL) (*url.URL, error) {
	var n *url.URL
	var err error
	if u.Scheme == "jira+customer" {
		if n, err = normalizeCustomer(u); err == nil {
			_, err = parseCustSelection(n.Query())
		}
	} else {
		if n, err = normalizeAgent(u); err == nil {
			_, err = parseSelection(n.Query())
		}
	}
	if err != nil && n != nil {
		return nil, fmt.Errorf("bad remote %q: %w", n.String(), err)
	}
	return n, err
}

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	n, err := normalize(u)
	if err != nil {
		return target{}, err
	}
	t := target{host: n.Hostname(), base: "https://" + n.Host}
	if n.Scheme == "jira+customer" {
		t.csel, _ = parseCustSelection(n.Query())
	} else {
		t.sel, _ = parseSelection(n.Query())
	}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := n.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if t.email, t.token, err = atlassian.Creds(n, cfg, getenv, lk, "GFS_JIRA"); err != nil {
		return target{}, err
	}
	return t, nil
}

// defaultDir is the project folder for a one-project agent selection, else
// the site's first host label.
func defaultDir(u *url.URL) string {
	n, err := normalize(u)
	if err != nil {
		return "jira"
	}
	if n.Scheme == "jira" {
		if sel, err := parseSelection(n.Query()); err == nil && len(sel.keys) == 1 {
			return strings.ToLower(sel.keys[0])
		}
	}
	return strings.SplitN(n.Hostname(), ".", 2)[0]
}
```

- [ ] **Step 7.4: Run the URL tests**

Run: `go test ./internal/adapter/jira/ -run 'TestNormalize|TestParseSelection|TestSinceDate|TestParseTarget' -v`
Expected: PASS.

- [ ] **Step 7.5: Write the failing schema tests**

`internal/adapter/jira/schema_test.go`:

```go
package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func parse(t *testing.T, s string) *xmltree.Node {
	t.Helper()
	n, err := xmltree.ParseString(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIssueSchemaCanon(t *testing.T) {
	root := parse(t, `<issue key="A-1" id="1">
  <comment id="9" created="2026-02-01">b</comment>
  <field id="customfield_2">2</field>
  <summary>S</summary>
  <comment internal="true">new</comment>
  <field id="customfield_1">1</field>
  <labels><label>z</label><label>a</label></labels>
  <status>To Do</status>
</issue>`)
	canon.Normalize(root, issueSchema)
	want := `<issue id="1" key="A-1">
  <summary>S</summary>
  <status>To Do</status>
  <labels>
    <label>a</label>
    <label>z</label>
  </labels>
  <field id="customfield_1">1</field>
  <field id="customfield_2">2</field>
  <comment id="9" created="2026-02-01">b</comment>
  <comment internal="true">new</comment>
</issue>`
	if got := xmltree.Print(root, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestIssueSchemaValidate(t *testing.T) {
	base := parse(t, `<issue id="1" key="A-1"><comment id="9" author="Bo" created="c">x</comment><link id="5" type="blocks">A-2</link></issue>`)
	cases := map[string]string{
		`<issue id="1" key="A-1"><comment id="9" author="Bo" created="c">edited</comment></issue>`:             "",
		`<issue id="1" key="A-1"><comment internal="true">new</comment><link type="blocks">A-3</link></issue>`: "",
		`<issue id="1" key="A-2"></issue>`:                                                                             `read-only attribute "key" changed`,
		`<issue id="1" key="A-1"><comment author="Me">new</comment></issue>`:                                           `read-only attribute "author" changed`,
		`<issue id="1" key="A-1"><worklog><started>s</started><spent>2h</spent><comment>x</comment></worklog></issue>`: `<comment> type "" not allowed`,
		`<issue id="1" key="A-1"><worklog><started>s</started><spent>2h</spent><comment type="application/vnd.atlassian.adf+xml"><paragraph>x</paragraph></comment></worklog></issue>`: "",
		`<issue id="1" key="A-1"><attachment name="a.png"/></issue>`: "new attachments are created by adding a file",
	}
	for in, want := range cases {
		err := validate.Resource(parse(t, in), base, issueSchema)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", in, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err %v, want %q", in, err, want)
		}
	}
}
```

- [ ] **Step 7.6: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run TestIssueSchema -v`
Expected: FAIL, `undefined: issueSchema`.

- [ ] **Step 7.7: Implement `schema.go`**

```go
package jira

import "github.com/KrzysztofBogdan/gitfs/internal/schema"

// wikiType is the body type of customer-mode text, as the customer API returns it.
const wikiType = "text/x-jira-wiki"

func ro(names ...string) []schema.Attr {
	out := make([]schema.Attr, len(names))
	for i, n := range names {
		out[i] = schema.Attr{Name: n, ReadOnly: true}
	}
	return out
}

func userElem(name string) schema.Elem {
	return schema.Elem{Name: name, Kind: schema.Field, Attrs: []schema.Attr{{Name: "account"}}}
}

func adfBody(name string) schema.Elem {
	return schema.Elem{Name: name, Kind: schema.Body, BodyTypes: []string{adfType}, Attrs: []schema.Attr{{Name: "type"}}}
}

// issueSchema is the <issue> root of agent mode (jira spec §5.1). User
// attributes on the root's fields (account on people) and the two declared
// sub-resource exceptions (internal/public on comments, type on links) are
// not read-only; the adapter checks them in Check.
var issueSchema = &schema.Schema{
	Root: "issue", ID: "id", Version: "updated",
	RootAttrs: ro("id", "key", "created", "updated", "resolved"),
	Elems: []schema.Elem{
		{Name: "summary", Kind: schema.Field},
		{Name: "type", Kind: schema.Field},
		{Name: "status", Kind: schema.Field},
		{Name: "resolution", Kind: schema.Field},
		{Name: "priority", Kind: schema.Field},
		userElem("assignee"),
		userElem("reporter"),
		userElem("creator"),
		{Name: "parent", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "components", Kind: schema.List, Item: "component", Sorted: true},
		{Name: "fixVersions", Kind: schema.List, Item: "version", Sorted: true},
		{Name: "affectsVersions", Kind: schema.List, Item: "version", Sorted: true},
		{Name: "due", Kind: schema.Field},
		{Name: "timetracking", Kind: schema.Field, Attrs: []schema.Attr{{Name: "spent"}}},
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id",
			Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}, {Name: "type"}}},
		adfBody("environment"),
		adfBody("description"),
		{Name: "link", Kind: schema.Sub, ID: "id", SortKey: "id",
			Attrs: append(ro("id"), schema.Attr{Name: "type"})},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name",
			Ops:   []string{"create", "delete"},
			Attrs: ro("id", "name", "size", "mime", "created", "author")},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: append(ro("id", "account", "author", "created", "updated"), schema.Attr{Name: "internal"}, schema.Attr{Name: "public"})},
		{Name: "worklog", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: ro("id", "account", "author", "created", "updated"),
			Children: []schema.Elem{
				{Name: "started", Kind: schema.Field},
				{Name: "spent", Kind: schema.Field},
				adfBody("comment"),
			}},
	},
}

// requestSchema is the <request> root of customer mode (jira spec §5.10).
var requestSchema = &schema.Schema{
	Root: "request", ID: "id", Version: "status-date",
	RootAttrs: ro("id", "key", "desk", "type", "created", "status-date"),
	Elems: []schema.Elem{
		{Name: "summary", Kind: schema.Field},
		{Name: "requestType", Kind: schema.Field},
		{Name: "status", Kind: schema.Field, Attrs: []schema.Attr{{Name: "category"}}},
		userElem("reporter"),
		{Name: "participant", Kind: schema.Field, Repeated: true, SortKey: "account", Attrs: []schema.Attr{{Name: "account"}}},
		{Name: "field", Kind: schema.Field, Repeated: true, SortKey: "id",
			Attrs: []schema.Attr{{Name: "id"}, {Name: "name"}, {Name: "type"}}},
		{Name: "description", Kind: schema.Body, BodyTypes: []string{wikiType}, Attrs: []schema.Attr{{Name: "type"}}},
		{Name: "approval", Kind: schema.Sub, ID: "id", SortKey: "id",
			Attrs: append(ro("id", "name", "status"), schema.Attr{Name: "decision"})},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name",
			Ops:   []string{"create"},
			Attrs: ro("id", "name", "size", "mime", "created", "author")},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: ro("id", "account", "author", "created")},
	},
}
```

Notes for the implementer:
- `<field>` is a repeated `Field` sorted by `id` (Task 4's canon change); it cannot be a `Sub`, because a newly added field carries its `id` and a `Sub` with an unknown id fails validation.
- Customer transitions have no target status, only names, so the request `<status>` element is written with the status name by gfs and edited by the user with a transition name (Task 18).
- The spec's `<assignee/>` for unassigning is not representable: canon drops empty elements. Removing the element clears the field (Task 12). `docs/jira.md` (Task 20) says so.

- [ ] **Step 7.8: Run the package tests and vet**

Run: `go vet ./internal/adapter/jira/ && go test ./internal/adapter/jira/`
Expected: PASS.

- [ ] **Step 7.9: Commit**

```bash
git add internal/adapter/jira/url.go internal/adapter/jira/url_test.go internal/adapter/jira/schema.go internal/adapter/jira/schema_test.go
git commit -m "jira: remote URLs, selection, issue and request schemas"
```

### Task 8: People, field metadata and field decoding

The people registry behind `.people.xml` (spec §5.7, §5.8), the metadata types and their cache file (spec §5.2), and the JSON → XML half of the field codecs (spec §5.3).

**Files:**
- Create: `internal/adapter/jira/people.go`
- Create: `internal/adapter/jira/meta.go`
- Create: `internal/adapter/jira/fields.go`
- Test: `internal/adapter/jira/fields_test.go`

**Interfaces:**
- Consumes: `el`, `textEl`, `adfToNodes` (Task 6), `adapter.Resource`.
- Produces:
  - `type apiUser struct{ AccountID, DisplayName, EmailAddress, AccountType string; Active bool }` (JSON tags `accountId` …)
  - `type registry`; `newRegistry()`, `(*registry).see(apiUser)`, `.seeMention(id, text)`, `.seeMentions([]*xmltree.Node)`, `.reset()`, `.byEmail(string) (person, bool)`, `.node()`, `.resource() adapter.Resource`, `.load(path) error`, `.save(path) error`; fields `m map[string]person`, `dirty bool`
  - `const peopleID = "people"`, `const peoplePath = ".people.xml"`, `func contentHash(*xmltree.Node) string`, `func writeFileAtomic(path string, data []byte) error`
  - `func userNode(name string, u apiUser) *xmltree.Node`
  - `type fieldMeta struct{ ID, Name, Type, Items, Custom string; Required bool }`, `type typeMeta struct{ ID, Name string; Subtask bool; Fields map[string]fieldMeta }`, `type projectMeta struct{ Key, ID string; JSM bool; Types map[string]*typeMeta }` with `dir()` and `typeNamed(name)`
  - `type metaCache struct{ Projects map[string]*projectMeta }`, `loadMeta(path)`, `(*metaCache).save(path)`
  - `var systemElems map[string]string` (field id → element), `var elemFields` (inverse), `var mappedApart map[string]bool`, `var alwaysFields []string`
  - `func fieldSet(t *typeMeta) map[string]fieldMeta`, `func projectFieldSet(p *projectMeta) map[string]fieldMeta`
  - `type codec int` with `cRaw cText cADF cNumber cDate cDateTime cOption cOptions cUser cUsers cLabels cKey cSprint`; `func codecOf(fieldMeta) codec`
  - `type apiOption struct{ ID, Value string }`, `func optionNode(apiOption) *xmltree.Node`, `func isEmptyJSON(json.RawMessage) bool`, `func rawField(fieldMeta, json.RawMessage) *xmltree.Node`
  - `func decodeField(m fieldMeta, raw json.RawMessage, reg *registry) (*xmltree.Node, error)`
  - test helpers `cf(id, name, custom string) fieldMeta` and `const sys` (in `fields_test.go`)

- [ ] **Step 8.1: Write the failing tests**

`internal/adapter/jira/fields_test.go`:

```go
package jira

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func cf(id, name, custom string) fieldMeta {
	return fieldMeta{ID: id, Name: name, Custom: custom}
}

const sys = "com.atlassian.jira.plugin.system.customfieldtypes:"

func TestDecodeField(t *testing.T) {
	cases := []struct {
		m    fieldMeta
		raw  string
		want string
	}{
		{cf("customfield_1", "Team", sys+"textfield"), `"Platform"`, `<field id="customfield_1" name="Team">Platform</field>`},
		{cf("customfield_2", "Story Points", "com.pyxis.greenhopper.jira:jsw-story-points"), `5.0`, `<field id="customfield_2" name="Story Points">5.0</field>`},
		{cf("customfield_3", "Release", sys+"datepicker"), `"2026-10-15"`, `<field id="customfield_3" name="Release">2026-10-15</field>`},
		{cf("customfield_4", "Users", sys+"select"), `{"self":"x","id":"10022","value":"1-10"}`, "<field id=\"customfield_4\" name=\"Users\">\n  <option id=\"10022\">1-10</option>\n</field>"},
		{cf("customfield_5", "Apps", sys+"multicheckboxes"), `[{"id":"1","value":"A"},{"id":"2","value":"B"}]`,
			"<field id=\"customfield_5\" name=\"Apps\">\n  <option id=\"1\">A</option>\n  <option id=\"2\">B</option>\n</field>"},
		{cf("customfield_6", "DRI", sys+"userpicker"), `{"accountId":"712020:a","displayName":"Adam","accountType":"atlassian","active":true}`,
			"<field id=\"customfield_6\" name=\"DRI\">\n  <user account=\"712020:a\">Adam</user>\n</field>"},
		{cf("customfield_7", "Tags", sys+"labels"), `["x","y"]`, "<field id=\"customfield_7\" name=\"Tags\">\n  <label>x</label>\n  <label>y</label>\n</field>"},
		{cf("customfield_8", "Sprint", "com.pyxis.greenhopper.jira:gh-sprint"), `[{"id":42,"name":"Sprint 17","state":"active"}]`,
			"<field id=\"customfield_8\" name=\"Sprint\">\n  <sprint id=\"42\">Sprint 17</sprint>\n</field>"},
		{cf("customfield_9", "License", sys+"textarea"), doc(`{"type":"paragraph","content":[{"type":"text","text":"hi "},{"type":"mention","attrs":{"id":"m1","text":"@Mo"}}]}`),
			"<field id=\"customfield_9\" name=\"License\" type=\"adf\">\n  <paragraph>hi <mention id=\"m1\" text=\"@Mo\"/></paragraph>\n</field>"},
		{cf("customfield_10", "Team", sys+"atlassian-team"), `{"id":"t1","name":"Core"}`, `<field id="customfield_10" name="Team" type="raw">{"id":"t1","name":"Core"}</field>`},
		{cf("customfield_11", "Odd", sys+"textfield"), `{"not":"a string"}`, `<field id="customfield_11" name="Odd" type="raw">{"not":"a string"}</field>`},
		{cf("customfield_12", "App", "ari:cloud:ecosystem::extension/x:static/f"), `"v"`, `<field id="customfield_12" name="App" type="raw">"v"</field>`},
		{fieldMeta{ID: "security", Name: "Security Level", Type: "securitylevel"}, `{"id":"1","name":"Staff"}`, `<field id="security" name="Security Level" type="raw">{"id":"1","name":"Staff"}</field>`},
		{cf("customfield_13", "Empty", sys+"textfield"), `null`, ``},
		{cf("customfield_14", "Empty list", sys+"multiselect"), `[]`, ``},
	}
	reg := newRegistry()
	for _, c := range cases {
		n, err := decodeField(c.m, json.RawMessage(c.raw), reg)
		if err != nil {
			t.Errorf("%s: %v", c.m.ID, err)
			continue
		}
		got := ""
		if n != nil {
			got = xmltree.Print(n, 0)
		}
		if got != c.want {
			t.Errorf("%s: got\n%s\nwant\n%s", c.m.ID, got, c.want)
		}
	}
	if reg.m["712020:a"].Name != "Adam" || reg.m["m1"].Name != "Mo" {
		t.Fatalf("registry %+v", reg.m)
	}
}

func TestRegistry(t *testing.T) {
	r := newRegistry()
	r.see(apiUser{AccountID: "b", DisplayName: "Bo", EmailAddress: "bo@x.com", AccountType: "atlassian", Active: true})
	r.seeMention("a", "@Al")
	r.see(apiUser{AccountID: "a", DisplayName: "Al", AccountType: "customer", Active: true})
	r.seeMention("a", "@Other")    // known: ignored
	r.see(apiUser{AccountID: "b"}) // no new facts
	want := "<people id=\"people\">\n  <person account=\"a\" type=\"customer\" active=\"true\">Al</person>\n  <person account=\"b\" type=\"atlassian\" active=\"true\" email=\"bo@x.com\">Bo</person>\n</people>"
	if got := xmltree.Print(r.node(), 0); got != want {
		t.Fatalf("got\n%s", got)
	}
	if p, ok := r.byEmail("BO@x.com"); !ok || p.Account != "b" {
		t.Fatal(p, ok)
	}
	path := filepath.Join(t.TempDir(), "jira", "people.json")
	if err := r.save(path); err != nil {
		t.Fatal(err)
	}
	r2 := newRegistry()
	if err := r2.load(path); err != nil || r2.resource().Version != r.resource().Version {
		t.Fatalf("reload: %v", err)
	}
	if err := newRegistry().load(filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatal("missing cache must be empty, not an error")
	}
}

func TestFieldSetAndMeta(t *testing.T) {
	tm := &typeMeta{ID: "1", Name: "Task", Fields: map[string]fieldMeta{
		"summary": {ID: "summary"}, "comment": {ID: "comment"}, "attachment": {ID: "attachment"}, "customfield_1": {ID: "customfield_1"},
	}}
	got := fieldSet(tm)
	if _, ok := got["comment"]; ok || len(got) != 3 || got["environment"].ID != "environment" {
		t.Fatalf("%+v", got)
	}
	path := filepath.Join(t.TempDir(), "meta.json")
	m, _ := loadMeta(path)
	m.Projects["GEN"] = &projectMeta{Key: "GEN", ID: "10017", Types: map[string]*typeMeta{"1": tm}}
	if err := m.save(path); err != nil {
		t.Fatal(err)
	}
	m2, err := loadMeta(path)
	if err != nil || m2.Projects["GEN"].typeNamed("task") == nil || m2.Projects["GEN"].dir() != "gen" {
		t.Fatalf("%+v %v", m2, err)
	}
	os.WriteFile(path, []byte("{broken"), 0o644)
	if m3, err := loadMeta(path); err != nil || len(m3.Projects) != 0 {
		t.Fatal("a broken cache must load empty")
	}
}
```

- [ ] **Step 8.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestDecodeField|TestRegistry|TestFieldSet' -v`
Expected: FAIL, `undefined: newRegistry`.

- [ ] **Step 8.3: Implement `people.go`**

```go
package jira

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// apiUser is a user as Jira and the customer API return one.
type apiUser struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	AccountType  string `json:"accountType"`
	Active       bool   `json:"active"`
}

type person struct {
	Account string `json:"account"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	Type    string `json:"type,omitempty"`
	Active  bool   `json:"active"`
}

const (
	peopleID   = "people"
	peoplePath = ".people.xml"
)

// registry is every user seen in the tree; it becomes .people.xml (jira spec §5.8).
type registry struct {
	m     map[string]person
	dirty bool // changed since load
}

func newRegistry() *registry { return &registry{m: map[string]person{}} }

// see records u; a later sighting fills in what an earlier one lacked.
func (r *registry) see(u apiUser) {
	if u.AccountID == "" {
		return
	}
	p := person{Account: u.AccountID, Name: u.DisplayName, Email: u.EmailAddress, Type: u.AccountType, Active: u.Active}
	if old, ok := r.m[p.Account]; ok {
		if p.Name == "" {
			p.Name = old.Name
		}
		if p.Email == "" {
			p.Email = old.Email
		}
		if p.Type == "" {
			p.Type, p.Active = old.Type, old.Active
		}
		if old == p {
			return
		}
	}
	r.m[p.Account] = p
	r.dirty = true
}

// seeMention records a user known only from a mention, unless known already.
func (r *registry) seeMention(id, text string) {
	if id == "" {
		return
	}
	if _, ok := r.m[id]; ok {
		return
	}
	r.m[id] = person{Account: id, Name: strings.TrimPrefix(text, "@"), Active: true}
	r.dirty = true
}

// seeMentions records the mentions anywhere in ns.
func (r *registry) seeMentions(ns []*xmltree.Node) {
	for _, n := range ns {
		if n.Kind != xmltree.Element {
			continue
		}
		if n.Name == "mention" {
			id, _ := n.Attr("id")
			text, _ := n.Attr("text")
			r.seeMention(id, text)
		}
		r.seeMentions(n.Children)
	}
}

func (r *registry) reset() {
	r.m = map[string]person{}
	r.dirty = true
}

// byEmail finds a known account by email, case-insensitively.
func (r *registry) byEmail(email string) (person, bool) {
	for _, p := range r.m {
		if p.Email != "" && strings.EqualFold(p.Email, email) {
			return p, true
		}
	}
	return person{}, false
}

func (r *registry) node() *xmltree.Node {
	ps := make([]person, 0, len(r.m))
	for _, p := range r.m {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].Account < ps[j].Account
	})
	root := el("people", "id", peopleID)
	for _, p := range ps {
		n := el("person", "account", p.Account, "type", p.Type, "active", strconv.FormatBool(p.Active), "email", p.Email)
		if p.Name != "" {
			n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: p.Name}}
		}
		root.Children = append(root.Children, n)
	}
	return root
}

// resource is .people.xml; its version is a hash of the content.
func (r *registry) resource() adapter.Resource {
	root := r.node()
	return adapter.Resource{ID: peopleID, Version: contentHash(root), Path: peoplePath, Root: root}
}

func contentHash(root *xmltree.Node) string {
	sum := sha256.Sum256([]byte(xmltree.Print(root, 0)))
	return hex.EncodeToString(sum[:6])
}

func (r *registry) load(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ps []person
	if err := json.Unmarshal(data, &ps); err != nil {
		return err
	}
	for _, p := range ps {
		r.m[p.Account] = p
	}
	return nil
}

func (r *registry) save(path string) error {
	ps := make([]person, 0, len(r.m))
	for _, p := range r.m {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Account < ps[j].Account })
	data, err := json.MarshalIndent(ps, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic writes data to path through a temp file and a rename.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// userNode is <name account="…">Display Name</name>.
func userNode(name string, u apiUser) *xmltree.Node {
	n := el(name, "account", u.AccountID)
	if u.DisplayName != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: u.DisplayName}}
	}
	return n
}
```

- [ ] **Step 8.4: Implement `meta.go`**

```go
package jira

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// fieldMeta is one field of an issue type's create screen (createmeta).
type fieldMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`   // schema.type: string, number, array, user, option, …
	Items    string `json:"items,omitempty"`  // schema.items for arrays
	Custom   string `json:"custom,omitempty"` // schema.custom: the custom field type key
	Required bool   `json:"required,omitempty"`
}

type typeMeta struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Subtask bool                 `json:"subtask,omitempty"`
	Fields  map[string]fieldMeta `json:"fields"`
}

type projectMeta struct {
	Key   string               `json:"key"`
	ID    string               `json:"id"`
	JSM   bool                 `json:"jsm,omitempty"`
	Types map[string]*typeMeta `json:"types,omitempty"` // by issue type id; nil until loaded
}

func (p *projectMeta) dir() string { return strings.ToLower(p.Key) }

// typeNamed finds an issue type by name, case-insensitively.
func (p *projectMeta) typeNamed(name string) *typeMeta {
	for _, t := range p.Types {
		if strings.EqualFold(t.Name, name) {
			return t
		}
	}
	return nil
}

// metaCache is .gfs/cache/jira/meta.json: project, type and field metadata.
type metaCache struct {
	Projects map[string]*projectMeta `json:"projects"` // by key
}

func loadMeta(path string) (*metaCache, error) {
	m := &metaCache{Projects: map[string]*projectMeta{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, m); err != nil {
		return &metaCache{Projects: map[string]*projectMeta{}}, nil // a broken cache is rebuilt
	}
	if m.Projects == nil {
		m.Projects = map[string]*projectMeta{}
	}
	return m, nil
}

func (m *metaCache) save(path string) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// systemElems maps Jira system field ids to their elements (jira spec §5.1).
var systemElems = map[string]string{
	"summary": "summary", "issuetype": "type", "status": "status", "resolution": "resolution",
	"priority": "priority", "assignee": "assignee", "reporter": "reporter", "creator": "creator",
	"parent": "parent", "labels": "labels", "components": "components", "fixVersions": "fixVersions",
	"versions": "affectsVersions", "duedate": "due", "timetracking": "timetracking",
	"environment": "environment", "description": "description",
}

// elemFields is systemElems inverted: element name to field id.
var elemFields = func() map[string]string {
	m := map[string]string{}
	for id, e := range systemElems {
		m[e] = id
	}
	return m
}()

// mappedApart are shown by their own elements, never as <field> (jira spec §5.2).
var mappedApart = map[string]bool{"comment": true, "attachment": true, "issuelinks": true, "worklog": true, "project": true}

// alwaysFields are requested for every issue, whatever its screens.
var alwaysFields = []string{"summary", "issuetype", "status", "resolution", "reporter", "creator", "created", "updated",
	"resolutiondate", "timetracking", "project", "comment", "worklog", "attachment", "issuelinks"}

// fieldSet is what an issue of type t shows (jira spec §5.2): the create
// screen's fields plus environment, minus the separately mapped ones.
func fieldSet(t *typeMeta) map[string]fieldMeta {
	out := map[string]fieldMeta{}
	for id, m := range t.Fields {
		if !mappedApart[id] {
			out[id] = m
		}
	}
	if _, ok := out["environment"]; !ok {
		out["environment"] = fieldMeta{ID: "environment", Name: "Environment", Type: "string"}
	}
	return out
}

// projectFieldSet is the union of every type's field set in p: the fallback
// for an issue whose type has no create screen.
func projectFieldSet(p *projectMeta) map[string]fieldMeta {
	out := map[string]fieldMeta{}
	for _, t := range p.Types {
		for id, m := range fieldSet(t) {
			out[id] = m
		}
	}
	if len(out) == 0 {
		out["environment"] = fieldMeta{ID: "environment", Name: "Environment", Type: "string"}
	}
	return out
}
```

- [ ] **Step 8.5: Implement `fields.go`**

```go
package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type codec int

const (
	cRaw codec = iota
	cText
	cADF
	cNumber
	cDate
	cDateTime
	cOption
	cOptions
	cUser
	cUsers
	cLabels
	cKey
	cSprint
)

// customCodecs maps a custom field type (the part after ':' of schema.custom,
// from the prefixes below) to its file shape (jira spec §5.3).
var customCodecs = map[string]codec{
	"textfield": cText, "url": cText, "gh-epic-label": cText,
	"textarea": cADF,
	"float":    cNumber, "jsw-story-points": cNumber,
	"datepicker": cDate, "datetime": cDateTime,
	"select": cOption, "radiobuttons": cOption, "gh-epic-status": cOption,
	"multiselect": cOptions, "multicheckboxes": cOptions,
	"userpicker": cUser, "multiuserpicker": cUsers, "sd-request-participants": cUsers,
	"labels":       cLabels,
	"gh-epic-link": cKey,
	"gh-sprint":    cSprint,
}

var customPrefixes = map[string]bool{
	"com.atlassian.jira.plugin.system.customfieldtypes": true,
	"com.pyxis.greenhopper.jira":                        true,
	"com.atlassian.servicedesk":                         true,
}

func codecOf(m fieldMeta) codec {
	prefix, name, ok := strings.Cut(m.Custom, ":")
	if !ok || !customPrefixes[prefix] {
		return cRaw
	}
	return customCodecs[name]
}

type apiOption struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func optionNode(o apiOption) *xmltree.Node {
	n := el("option", "id", o.ID)
	if o.Value != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: o.Value}}
	}
	return n
}

func isEmptyJSON(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "[]", `""`, "{}":
		return true
	}
	return false
}

// rawField shows a value gfs has no codec for, read-only, as JSON.
func rawField(m fieldMeta, raw json.RawMessage) *xmltree.Node {
	var b bytes.Buffer
	json.Compact(&b, raw)
	n := el("field", "id", m.ID, "name", m.Name, "type", "raw")
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: b.String()}}
	return n
}

// decodeField renders a custom (or unmapped system) field value as
// <field id name [type]>; nil for an empty value. A value whose JSON shape
// does not match its codec is shown raw, so pull never fails on it.
func decodeField(m fieldMeta, raw json.RawMessage, reg *registry) (*xmltree.Node, error) {
	if isEmptyJSON(raw) {
		return nil, nil
	}
	n := el("field", "id", m.ID, "name", m.Name)
	add := func(c *xmltree.Node) { n.Children = append(n.Children, c) }
	switch codecOf(m) {
	case cText, cDate, cDateTime, cKey:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return rawField(m, raw), nil
		}
		add(&xmltree.Node{Kind: xmltree.Text, Text: s})
	case cNumber:
		var f json.Number
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&f) != nil {
			return rawField(m, raw), nil
		}
		add(&xmltree.Node{Kind: xmltree.Text, Text: f.String()})
	case cADF:
		kids, err := adfToNodes(raw)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", m.ID, err)
		}
		n.SetAttr("type", "adf")
		n.Children = kids
		reg.seeMentions(kids)
	case cOption:
		var o apiOption
		if json.Unmarshal(raw, &o) != nil {
			return rawField(m, raw), nil
		}
		add(optionNode(o))
	case cOptions:
		var os []apiOption
		if json.Unmarshal(raw, &os) != nil {
			return rawField(m, raw), nil
		}
		for _, o := range os {
			add(optionNode(o))
		}
	case cUser:
		var u apiUser
		if json.Unmarshal(raw, &u) != nil || u.AccountID == "" {
			return rawField(m, raw), nil
		}
		reg.see(u)
		add(userNode("user", u))
	case cUsers:
		var us []apiUser
		if json.Unmarshal(raw, &us) != nil {
			return rawField(m, raw), nil
		}
		for _, u := range us {
			reg.see(u)
			add(userNode("user", u))
		}
	case cLabels:
		var ls []string
		if json.Unmarshal(raw, &ls) != nil {
			return rawField(m, raw), nil
		}
		for _, l := range ls {
			add(textEl("label", l))
		}
	case cSprint:
		var ss []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		}
		if json.Unmarshal(raw, &ss) != nil {
			return rawField(m, raw), nil
		}
		for _, s := range ss {
			sp := el("sprint", "id", s.ID.String())
			sp.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: s.Name}}
			add(sp)
		}
	default:
		return rawField(m, raw), nil
	}
	return n, nil
}
```

- [ ] **Step 8.6: Run the tests**

Run: `go vet ./internal/adapter/jira/ && go test ./internal/adapter/jira/ -v -run 'TestDecodeField|TestRegistry|TestFieldSet'`
Expected: PASS.

- [ ] **Step 8.7: Commit**

```bash
git add internal/adapter/jira/people.go internal/adapter/jira/meta.go internal/adapter/jira/fields.go internal/adapter/jira/fields_test.go
git commit -m "jira: people registry, field metadata, field value decoding"
```

### Task 9: Issue decoding and file paths

A Jira issue's JSON becomes the `<issue>` root of spec §5.1; paths are `<dir>/<KEY> <summary>.xml` (spec §4).

**Files:**
- Create: `internal/adapter/jira/issue.go`
- Test: `internal/adapter/jira/issue_test.go`

**Interfaces:**
- Consumes: Task 8 (registry, `decodeField`, `systemElems`, `isEmptyJSON`), Task 6 (`adfToNodes`, `el`, `textEl`), `attach.SidecarSuffix`.
- Produces:
  - `type apiIssue struct{ ID, Key string; Fields map[string]json.RawMessage }` with `str(id)`, `named(id)`, `idOf(id)`, `project() (key, typeID string)`
  - `type apiComment`, `type apiWorklog`, `type apiLink`, `type apiAttachment`, `type issueRef struct{ Key string }`
  - `func page[T any](raw json.RawMessage, key string) ([]T, int, error)`: inline comment/worklog pages
  - `func sanitize(string) string`, `const maxName = 200`, `func issuePath(dir, key, summary string) string` (cuts names longer than `maxName` bytes at a character boundary)
  - `type decoder struct{ reg *registry; jsm bool }` with `issue(is apiIssue, set map[string]fieldMeta, comments []apiComment, worklogs []apiWorklog) (*xmltree.Node, error)`, `comment(apiComment)`, `worklog(apiWorklog)`, `user(name, raw)`, `body(name, raw)`
  - `func textEl2(name, text string, attrs ...string) *xmltree.Node`
  - test constant `issueJSON` (in `issue_test.go`, reused by Task 20's grammar test)

- [ ] **Step 9.1: Write the failing tests**

`internal/adapter/jira/issue_test.go`:

```go
package jira

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const issueJSON = `{"id":"103922","key":"SUP-4057","fields":{
 "summary":"Promo code request","issuetype":{"id":"10006","name":"Support"},
 "status":{"name":"Waiting for support","statusCategory":{"key":"indeterminate"}},
 "resolution":null,"priority":{"name":"Lowest"},
 "assignee":{"accountId":"712020:a","displayName":"Adam","accountType":"atlassian","active":true},
 "reporter":{"accountId":"qm:h","displayName":"hadas@example.com","emailAddress":"hadas@example.com","accountType":"customer","active":true},
 "creator":{"accountId":"qm:h","displayName":"hadas@example.com","accountType":"customer","active":true},
 "project":{"id":"10001","key":"SUP"},"parent":{"key":"SUP-4000"},"labels":["renewal"],
 "components":[{"name":"billing"}],"fixVersions":[],"versions":[{"name":"2026.09"}],"duedate":"2026-09-30",
 "timetracking":{"originalEstimate":"2d","remainingEstimate":"4h","timeSpent":"1d 2h"},
 "created":"2026-09-29T15:22:28.934+0200","updated":"2026-09-29T15:23:31.800+0200","resolutiondate":null,
 "customfield_10040":{"id":"10022","value":"1-10"},"customfield_10019":"0|i06csf:","environment":null,
 "description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]},
 "issuelinks":[{"id":"10231","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"SUP-3990"}},
   {"id":"10232","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"SUP-3001"}}],
 "attachment":[{"id":"10500","filename":"log.txt","size":1204,"mimeType":"text/plain","created":"2026-09-29T15:30:00.000+0200","author":{"accountId":"712020:a","displayName":"Adam"}}]
}}`

func TestDecodeIssue(t *testing.T) {
	var is apiIssue
	if err := json.Unmarshal([]byte(issueJSON), &is); err != nil {
		t.Fatal(err)
	}
	set := map[string]fieldMeta{
		"summary": {ID: "summary"}, "priority": {ID: "priority"}, "description": {ID: "description"},
		"customfield_10040": cf("customfield_10040", "Number of users", sys+"select"),
	}
	pub, internal := true, false
	comments := []apiComment{
		{ID: "2", Author: apiUser{AccountID: "712020:a", DisplayName: "Adam"}, Created: "2026-09-29T16:00:00.000+0200",
			Updated: "2026-09-29T16:00:00.000+0200", JSDPublic: &internal, Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"note"}]}`))},
		{ID: "1", Author: apiUser{AccountID: "qm:h", DisplayName: "hadas@example.com"}, Created: "2026-09-29T15:50:00.000+0200",
			Updated: "2026-09-29T15:50:00.000+0200", JSDPublic: &pub, Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"thanks"}]}`))},
	}
	worklogs := []apiWorklog{{ID: "4412", Author: apiUser{AccountID: "712020:a", DisplayName: "Adam"}, Started: "2026-09-29T09:00:00.000+0200",
		TimeSpent: "2h", Created: "2026-09-29T17:00:00.000+0200", Updated: "2026-09-29T17:00:00.000+0200"}}
	reg := newRegistry()
	root, err := decoder{reg: reg, jsm: true}.issue(is, set, comments, worklogs)
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(root, issueSchema)
	want := `<issue id="103922" key="SUP-4057" created="2026-09-29T15:22:28.934+0200" updated="2026-09-29T15:23:31.800+0200">
  <summary>Promo code request</summary>
  <type>Support</type>
  <status>Waiting for support</status>
  <priority>Lowest</priority>
  <assignee account="712020:a">Adam</assignee>
  <reporter account="qm:h">hadas@example.com</reporter>
  <creator account="qm:h">hadas@example.com</creator>
  <parent>SUP-4000</parent>
  <labels>
    <label>renewal</label>
  </labels>
  <components>
    <component>billing</component>
  </components>
  <affectsVersions>
    <version>2026.09</version>
  </affectsVersions>
  <due>2026-09-30</due>
  <timetracking spent="1d 2h">
    <original>2d</original>
    <remaining>4h</remaining>
  </timetracking>
  <field id="customfield_10040" name="Number of users">
    <option id="10022">1-10</option>
  </field>
  <description type="application/vnd.atlassian.adf+xml">
    <paragraph>Hello</paragraph>
  </description>
  <link id="10231" type="blocks">SUP-3990</link>
  <link id="10232" type="is blocked by">SUP-3001</link>
  <attachment id="10500" name="log.txt" size="1204" mime="text/plain" created="2026-09-29T15:30:00.000+0200" author="Adam"/>
  <comment id="1" account="qm:h" author="hadas@example.com" created="2026-09-29T15:50:00.000+0200" updated="2026-09-29T15:50:00.000+0200" public="true">
    <paragraph>thanks</paragraph>
  </comment>
  <comment id="2" account="712020:a" author="Adam" created="2026-09-29T16:00:00.000+0200" updated="2026-09-29T16:00:00.000+0200" internal="true">
    <paragraph>note</paragraph>
  </comment>
  <worklog id="4412" account="712020:a" author="Adam" created="2026-09-29T17:00:00.000+0200" updated="2026-09-29T17:00:00.000+0200">
    <started>2026-09-29T09:00:00.000+0200</started>
    <spent>2h</spent>
  </worklog>
</issue>`
	if got := xmltree.Print(root, 0); got != want {
		t.Fatalf("got\n%s", got)
	}
	if reg.m["qm:h"].Email != "hadas@example.com" || len(reg.m) != 2 {
		t.Fatalf("people %+v", reg.m)
	}
	// outside a service project comments carry no visibility
	root, _ = decoder{reg: reg}.issue(is, set, comments[:1], nil)
	if c := root.Child("comment"); c == nil || len(c.Attrs) != 5 {
		t.Fatalf("non-JSM comment attrs: %+v", c)
	}
}

func TestInlinePage(t *testing.T) {
	cs, total, err := page[apiComment](json.RawMessage(`{"comments":[{"id":"1"}],"total":7,"maxResults":1}`), "comments")
	if err != nil || len(cs) != 1 || total != 7 {
		t.Fatal(cs, total, err)
	}
	if cs, total, err := page[apiComment](json.RawMessage(`null`), "comments"); cs != nil || total != 0 || err != nil {
		t.Fatal("null page")
	}
}

func TestIssuePaths(t *testing.T) {
	for _, c := range []struct{ key, summary, want string }{
		{"GEN-1", "Migrate auth", "gen/GEN-1 Migrate auth.xml"},
		{"GEN-2", "a/b\\c", "gen/GEN-2 a-b-c.xml"},
		{"GEN-3", "  ", "gen/GEN-3.xml"},
		{"GEN-4", "notes.files", "gen/GEN-4 notes.files_.xml"},
		{"GEN-5", "tab\there", "gen/GEN-5 tab here.xml"},
	} {
		if got := issuePath("gen", c.key, c.summary); got != c.want {
			t.Errorf("%s: %q, want %q", c.key, got, c.want)
		}
	}
	long := issuePath("sup", "SUP-9", strings.Repeat("Zażółć gęślą jaźń ", 20))
	if name := strings.TrimPrefix(long, "sup/"); len(name) > maxName+len(".xml") || !utf8.ValidString(name) || !strings.HasPrefix(name, "SUP-9 Zażółć") {
		t.Fatalf("%d bytes: %q", len(name), name)
	}
}
```

- [ ] **Step 9.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestDecodeIssue|TestInlinePage|TestIssuePaths' -v`
Expected: FAIL, `undefined: apiIssue`.

- [ ] **Step 9.3: Implement `issue.go`**

```go
package jira

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiIssue struct {
	ID     string                     `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type apiComment struct {
	ID        string          `json:"id"`
	Author    apiUser         `json:"author"`
	Body      json.RawMessage `json:"body"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
	JSDPublic *bool           `json:"jsdPublic"`
}

type apiWorklog struct {
	ID        string          `json:"id"`
	Author    apiUser         `json:"author"`
	Comment   json.RawMessage `json:"comment"`
	Started   string          `json:"started"`
	TimeSpent string          `json:"timeSpent"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
}

type issueRef struct {
	Key string `json:"key"`
}

type apiLink struct {
	ID   string `json:"id"`
	Type struct {
		Name    string `json:"name"`
		Inward  string `json:"inward"`
		Outward string `json:"outward"`
	} `json:"type"`
	InwardIssue  *issueRef `json:"inwardIssue"`
	OutwardIssue *issueRef `json:"outwardIssue"`
}

type apiAttachment struct {
	ID       string  `json:"id"`
	Filename string  `json:"filename"`
	MimeType string  `json:"mimeType"`
	Created  string  `json:"created"`
	Size     int64   `json:"size"`
	Author   apiUser `json:"author"`
}

// str reads a string field; "" when absent, null or not a string.
func (is apiIssue) str(id string) string {
	var s string
	json.Unmarshal(is.Fields[id], &s)
	return s
}

// named reads the "name" of an object field (priority, status, …).
func (is apiIssue) named(id string) string {
	var v struct {
		Name string `json:"name"`
	}
	json.Unmarshal(is.Fields[id], &v)
	return v.Name
}

// idOf reads the "id" of an object field (issuetype, project).
func (is apiIssue) idOf(id string) string {
	var v struct {
		ID string `json:"id"`
	}
	json.Unmarshal(is.Fields[id], &v)
	return v.ID
}

func (is apiIssue) project() (key, typeID string) {
	var p struct {
		Key string `json:"key"`
	}
	json.Unmarshal(is.Fields["project"], &p)
	return p.Key, is.idOf("issuetype")
}

// page reads an inline comment or worklog page: its items and total.
func page[T any](raw json.RawMessage, key string) ([]T, int, error) {
	if isEmptyJSON(raw) {
		return nil, 0, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, 0, err
	}
	var items []T
	if err := json.Unmarshal(m[key], &items); err != nil && m[key] != nil {
		return nil, 0, err
	}
	total := len(items)
	if t, ok := m["total"]; ok {
		json.Unmarshal(t, &total)
	}
	return items, total, nil
}

func sanitize(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "untitled"
	}
	if s[0] == '.' {
		s = "_" + s[1:]
	}
	if strings.HasSuffix(s, attach.SidecarSuffix) {
		s += "_" // a file name must never look like a sidecar folder
	}
	return s
}

// maxName keeps file names under the usual 255-byte limit, with room for
// ".xml", ".files" and conflict-copy suffixes.
const maxName = 200

// issuePath is <dir>/<KEY> <summary>.xml (jira spec §4); a long summary is
// cut at a character boundary.
func issuePath(dir, key, summary string) string {
	name := key
	if s := strings.TrimSpace(summary); s != "" {
		name += " " + s
	}
	if len(name) > maxName {
		i := maxName
		for i > 0 && !utf8.RuneStart(name[i]) {
			i--
		}
		name = name[:i]
	}
	return dir + "/" + sanitize(name) + ".xml"
}

// decoder turns issue JSON into an <issue> root (jira spec §5.1).
type decoder struct {
	reg *registry
	jsm bool // the project is a service project: comments carry internal/public
}

func (d decoder) user(name string, raw json.RawMessage) *xmltree.Node {
	var u apiUser
	if isEmptyJSON(raw) || json.Unmarshal(raw, &u) != nil || u.AccountID == "" {
		return nil
	}
	d.reg.see(u)
	return userNode(name, u)
}

func (d decoder) body(name string, raw json.RawMessage) (*xmltree.Node, error) {
	kids, err := adfToNodes(raw)
	if err != nil || len(kids) == 0 {
		return nil, err
	}
	d.reg.seeMentions(kids)
	n := el(name, "type", adfType)
	n.Children = kids
	return n, nil
}

func names(raw json.RawMessage, item string) []*xmltree.Node {
	var vs []struct {
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &vs)
	var out []*xmltree.Node
	for _, v := range vs {
		out = append(out, textEl(item, v.Name))
	}
	return out
}

func list(name string, items []*xmltree.Node) *xmltree.Node {
	if len(items) == 0 {
		return nil
	}
	n := el(name)
	n.Children = items
	return n
}

// issue decodes is. set is the issue type's field set; comments and worklogs
// are complete (topped up by the caller when search cut them short).
func (d decoder) issue(is apiIssue, set map[string]fieldMeta, comments []apiComment, worklogs []apiWorklog) (*xmltree.Node, error) {
	f := is.Fields
	root := el("issue", "id", is.ID, "key", is.Key, "created", is.str("created"), "updated", is.str("updated"),
		"resolved", is.str("resolutiondate"))
	add := func(n *xmltree.Node) {
		if n != nil {
			root.Children = append(root.Children, n)
		}
	}
	add(textEl("summary", is.str("summary")))
	add(textEl("type", is.named("issuetype")))
	add(textEl("status", is.named("status")))
	if r := is.named("resolution"); r != "" {
		add(textEl("resolution", r))
	}
	if p := is.named("priority"); p != "" {
		add(textEl("priority", p))
	}
	add(d.user("assignee", f["assignee"]))
	add(d.user("reporter", f["reporter"]))
	add(d.user("creator", f["creator"]))
	var parent issueRef
	if json.Unmarshal(f["parent"], &parent) == nil && parent.Key != "" {
		add(textEl("parent", parent.Key))
	}
	var labels []string
	json.Unmarshal(f["labels"], &labels)
	var ls []*xmltree.Node
	for _, l := range labels {
		ls = append(ls, textEl("label", l))
	}
	add(list("labels", ls))
	add(list("components", names(f["components"], "component")))
	add(list("fixVersions", names(f["fixVersions"], "version")))
	add(list("affectsVersions", names(f["versions"], "version")))
	if due := is.str("duedate"); due != "" {
		add(textEl("due", due))
	}
	var tt struct {
		Original  string `json:"originalEstimate"`
		Remaining string `json:"remainingEstimate"`
		Spent     string `json:"timeSpent"`
	}
	if json.Unmarshal(f["timetracking"], &tt) == nil && (tt.Original != "" || tt.Remaining != "" || tt.Spent != "") {
		n := el("timetracking", "spent", tt.Spent)
		if tt.Original != "" {
			n.Children = append(n.Children, textEl("original", tt.Original))
		}
		if tt.Remaining != "" {
			n.Children = append(n.Children, textEl("remaining", tt.Remaining))
		}
		add(n)
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, sys := systemElems[id]; sys {
			continue
		}
		n, err := decodeField(set[id], f[id], d.reg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", is.Key, err)
		}
		add(n)
	}
	for _, b := range []struct{ elem, field string }{{"environment", "environment"}, {"description", "description"}} {
		if _, shown := set[b.field]; !shown && b.field == "environment" {
			continue
		}
		n, err := d.body(b.elem, f[b.field])
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", is.Key, b.field, err)
		}
		add(n)
	}
	var links []apiLink
	json.Unmarshal(f["issuelinks"], &links)
	for _, l := range links {
		switch {
		case l.OutwardIssue != nil:
			add(textEl2("link", l.OutwardIssue.Key, "id", l.ID, "type", l.Type.Outward))
		case l.InwardIssue != nil:
			add(textEl2("link", l.InwardIssue.Key, "id", l.ID, "type", l.Type.Inward))
		}
	}
	var atts []apiAttachment
	json.Unmarshal(f["attachment"], &atts)
	for _, a := range atts {
		d.reg.see(a.Author)
		add(el("attachment", "id", a.ID, "name", a.Filename, "size", strconv.FormatInt(a.Size, 10), "mime", a.MimeType,
			"created", a.Created, "author", a.Author.DisplayName))
	}
	for _, c := range comments {
		n, err := d.comment(c)
		if err != nil {
			return nil, fmt.Errorf("%s comment %s: %w", is.Key, c.ID, err)
		}
		add(n)
	}
	for _, w := range worklogs {
		n, err := d.worklog(w)
		if err != nil {
			return nil, fmt.Errorf("%s worklog %s: %w", is.Key, w.ID, err)
		}
		add(n)
	}
	return root, nil
}

// textEl2 is an element with attributes and text.
func textEl2(name, text string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
	}
	return n
}

func (d decoder) comment(c apiComment) (*xmltree.Node, error) {
	d.reg.see(c.Author)
	kids, err := adfToNodes(c.Body)
	if err != nil {
		return nil, err
	}
	d.reg.seeMentions(kids)
	n := el("comment", "id", c.ID, "account", c.Author.AccountID, "author", c.Author.DisplayName,
		"created", c.Created, "updated", c.Updated)
	if d.jsm && c.JSDPublic != nil {
		if *c.JSDPublic {
			n.SetAttr("public", "true")
		} else {
			n.SetAttr("internal", "true")
		}
	}
	n.Children = kids
	return n, nil
}

func (d decoder) worklog(w apiWorklog) (*xmltree.Node, error) {
	d.reg.see(w.Author)
	n := el("worklog", "id", w.ID, "account", w.Author.AccountID, "author", w.Author.DisplayName,
		"created", w.Created, "updated", w.Updated)
	n.Children = append(n.Children, textEl("started", w.Started), textEl("spent", w.TimeSpent))
	c, err := d.body("comment", w.Comment)
	if err != nil {
		return nil, err
	}
	if c != nil {
		n.Children = append(n.Children, c)
	}
	return n, nil
}
```

What the decoder deliberately leaves out: fields outside the issue type's field set (Rank, Development, SLA and other noise the spike found), `environment` when the type does not show it, and link direction words other than the link type's own `inward`/`outward` phrases.

- [ ] **Step 9.4: Run the tests**

Run: `go vet ./internal/adapter/jira/ && go test ./internal/adapter/jira/`
Expected: PASS.

- [ ] **Step 9.5: Commit**

```bash
git add internal/adapter/jira/issue.go internal/adapter/jira/issue_test.go
git commit -m "jira: issue JSON to <issue>, file paths"
```

### Task 10: Fake Jira server, read side

An in-memory Jira Cloud (REST v3 subset) in the style of `cftest`, serving everything pull needs (spec §11.1). Write endpoints come in later tasks, each in its own file that appends a registration function to `registrars`, so no later task edits this file.

**Files:**
- Create: `internal/adapter/jira/jtest/server.go`
- Test: `internal/adapter/jira/jtest/server_test.go`

**Interfaces:**
- Consumes: nothing from the adapter (the fake must not import it).
- Produces (package `jtest`):
  - `const TimeFormat = "2006-01-02T15:04:05.000-0700"`
  - types `Project{ID, Key, Type}`, `IssueType{ID, Name; Subtask}`, `Field{ID, Name, Type, Items, Custom; Required}`, `User{Account, Name, Email; Customer}`, `Comment{ID, Author; Body json.RawMessage; Created, Updated; Public *bool}`, `Worklog{ID, Author, Started, Spent; Comment json.RawMessage; Created, Updated}`, `Attachment{ID, Filename, Mime, Author; Data; Created}`, `LinkType{ID, Name, Inward, Outward}`, `Link{ID, Type, From, To}`, `Status{ID, Name, Category}`, `Transition{ID, Name, To; From []string; Screen []ScreenField}`, `ScreenField{ID; Required}`, `Workflow{Name; Statuses; Transitions}`, `Issue{ID, Key, Project, Type, Summary, Status, Assignee, Reporter, Creator; Fields map[string]any; Created, Updated; Comments; Worklogs; Attachments}`
  - `type Server` with exported knobs `Clock`, `Admin`, `CommentLimit`, `WorklogLimit`, `RateLimit`, `Requests`, `Fail`
  - `New()`, `AddProject`, `SetFields`, `AddUser`, `AddLinkType`, `AddLink`, `SetWorkflow`, `AddIssue`, `Edit`, `Delete`, `Move`, `AddComment`, `AddWorklog`, `AddAttachment`, `Issue(id)`, `ID(key)`, `Count(method, prefix)`
  - unexported helpers later tasks' handlers use: `s.mu`, `s.now()`, `s.nextID()`, `s.lookup(idOrKey)`, `s.workflow(is)`, `s.userJSON(account)`, `s.issueJSON(is, fields, inline)`, `writeJSON`, `jiraError`, `startMax`, `window`, `contains`, `s.addIssue(is)`, `defaultWorkflow`
  - `var registrars []func(*Server, *http.ServeMux)`: `New` calls each; a write-side file adds itself with `func init() { registrars = append(registrars, (*Server).xRoutes) }`
  - `Field.Options []Option` (`type Option struct{ ID, Value string }`) and `Server.EditHidden []string`, used by the edit handlers of Task 12

- [ ] **Step 10.1: Write the failing tests**

`internal/adapter/jira/jtest/server_test.go`:

```go
package jtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func post(t *testing.T, s *Server, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func get(t *testing.T, s *Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(s.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type searchResp struct {
	Issues []struct {
		ID, Key string
		Fields  map[string]json.RawMessage
	}
	NextPageToken string
	IsLast        bool
}

func fixture() (*Server, time.Time) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s := New()
	s.Clock = func() time.Time { return now }
	s.AddProject(Project{Key: "GEN"}, IssueType{ID: "1", Name: "Task"})
	s.AddProject(Project{Key: "SUP", Type: "service_desk"}, IssueType{ID: "2", Name: "Support"})
	for i := 0; i < 5; i++ {
		s.AddIssue(Issue{Project: "GEN", Type: "1", Summary: fmt.Sprint("g", i), Updated: now.Add(-time.Duration(i) * time.Hour)})
	}
	s.AddIssue(Issue{Project: "SUP", Type: "2", Summary: "s", Updated: now.Add(-30 * time.Minute)})
	return s, now
}

func TestSearchJQLAndPaging(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	var r searchResp
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project = "GEN" ORDER BY updated DESC`, "maxResults": 2, "fields": []string{"summary"}}, &r)
	if len(r.Issues) != 2 || r.IsLast || r.NextPageToken != "2" || r.Issues[0].Key != "GEN-1" {
		t.Fatalf("%+v", r)
	}
	if _, ok := r.Issues[0].Fields["status"]; ok || string(r.Issues[0].Fields["summary"]) != `"g0"` {
		t.Fatalf("fields not filtered: %v", r.Issues[0].Fields)
	}
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project = "GEN" ORDER BY updated DESC`, "maxResults": 2, "nextPageToken": "4"}, &r)
	if len(r.Issues) != 1 || !r.IsLast || r.Issues[0].Key != "GEN-5" {
		t.Fatalf("%+v", r)
	}
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project in ("GEN","SUP") AND updated >= "-90m" ORDER BY updated ASC`, "maxResults": 100}, &r)
	if len(r.Issues) != 3 || r.Issues[0].Key != "GEN-2" || r.Issues[1].Key != "SUP-1" || r.Issues[2].Key != "GEN-1" {
		t.Fatalf("relative window: %+v", r.Issues)
	}
	var c struct{ Count int }
	post(t, s, "/rest/api/3/search/approximate-count", map[string]any{"jql": `project = "GEN"`}, &c)
	if c.Count != 5 {
		t.Fatal(c)
	}
	if code := post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `assignee = currentUser()`}, nil); code != 400 {
		t.Fatal("unsupported JQL must be refused", code)
	}
}

func TestInlineCapsAndPages(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.WorklogLimit = 2
	for i := 0; i < 3; i++ {
		s.AddWorklog("10003", Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
	}
	pub := false
	s.AddComment("10008", Comment{Author: "me", Body: json.RawMessage(`{"type":"doc","version":1,"content":[]}`), Public: &pub})
	var r searchResp
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project in ("GEN","SUP")`, "maxResults": 100, "fields": []string{"worklog", "comment"}}, &r)
	for _, is := range r.Issues {
		var wl struct {
			Worklogs []any
			Total    int
		}
		json.Unmarshal(is.Fields["worklog"], &wl)
		if is.ID == "10003" && (len(wl.Worklogs) != 2 || wl.Total != 3) {
			t.Fatalf("inline worklogs %d of %d", len(wl.Worklogs), wl.Total)
		}
		if is.Key == "SUP-1" && !bytes.Contains(is.Fields["comment"], []byte(`"jsdPublic":false`)) {
			t.Fatalf("JSM comment: %s", is.Fields["comment"])
		}
	}
	var all struct {
		Worklogs []any
		Total    int
	}
	get(t, s, "/rest/api/3/issue/GEN-1/worklog", &all)
	if len(all.Worklogs) != 3 {
		t.Fatal(all)
	}
}

func TestMetaAndProjects(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.SetFields("GEN", "1", Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		Field{ID: "customfield_1", Name: "Team", Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textfield"})
	var ps struct {
		Values []struct{ ID, Key, ProjectTypeKey string }
		IsLast bool
	}
	get(t, s, "/rest/api/3/project/search?startAt=0&maxResults=1", &ps)
	if len(ps.Values) != 1 || ps.IsLast || ps.Values[0].Key != "GEN" {
		t.Fatalf("%+v", ps)
	}
	var ts struct {
		IssueTypes []struct{ ID, Name string }
		Total      int
	}
	get(t, s, "/rest/api/3/issue/createmeta/GEN/issuetypes", &ts)
	var fs struct {
		Fields []struct {
			FieldID  string
			Required bool
			Schema   struct{ Custom string }
		}
	}
	get(t, s, "/rest/api/3/issue/createmeta/GEN/issuetypes/1", &fs)
	if ts.Total != 1 || len(fs.Fields) != 2 || !fs.Fields[0].Required || fs.Fields[1].Schema.Custom == "" {
		t.Fatalf("%+v %+v", ts, fs)
	}
}

func TestRateLimitAndFail(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.RateLimit = 1
	if code := get(t, s, "/rest/api/3/myself", nil); code != 429 {
		t.Fatal(code)
	}
	if code := get(t, s, "/rest/api/3/myself", nil); code != 200 {
		t.Fatal(code)
	}
	s.Fail["GET /rest/api/3/myself"] = 500
	if code := get(t, s, "/rest/api/3/myself", nil); code != 500 {
		t.Fatal(code)
	}
	if s.Count("GET", "/rest/api/3/myself") != 3 {
		t.Fatal(s.Requests)
	}
}

func TestWorkflowEndpoints(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.SetWorkflow("SUP", "2", Workflow{Name: "Support flow",
		Statuses:    []Status{{"10", "Open", "new"}, {"11", "Closed", "done"}},
		Transitions: []Transition{{ID: "5", Name: "Resolve", To: "Closed", From: []string{"Open"}, Screen: []ScreenField{{ID: "resolution", Required: true}}}}})
	if code := get(t, s, "/rest/api/3/workflow/search?workflowName=Support+flow", nil); code != 403 {
		t.Fatal("workflows need admin", code)
	}
	s.Admin = true
	var wf struct {
		Values []struct {
			Transitions []struct {
				Name, To, Type string
				From           []string
			}
		}
	}
	get(t, s, "/rest/api/3/workflow/search?workflowName=Support+flow&expand=transitions,statuses", &wf)
	if tr := wf.Values[0].Transitions[0]; tr.Name != "Resolve" || tr.To != "11" || tr.From[0] != "10" || tr.Type != "directed" {
		t.Fatalf("%+v", wf)
	}
	var st []struct {
		Name     string
		Statuses []struct{ Name string }
	}
	get(t, s, "/rest/api/3/project/SUP/statuses", &st)
	if len(st) != 1 || st[0].Statuses[1].Name != "Closed" {
		t.Fatalf("%+v", st)
	}
}
```

- [ ] **Step 10.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/jtest/ -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 10.3: Implement `server.go`**

```go
// Package jtest is an in-memory Jira Cloud (REST v3 subset) for tests.
package jtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TimeFormat is how Jira writes timestamps.
const TimeFormat = "2006-01-02T15:04:05.000-0700"

type Project struct {
	ID, Key string
	Type    string // "software" (default), "service_desk", "business"
}

type IssueType struct {
	ID, Name string
	Subtask  bool
}

// Field is one field of a create screen, as createmeta reports it.
type Field struct {
	ID, Name, Type, Items, Custom string
	Required                      bool
	Options                       []Option // select-type fields
}

type Option struct{ ID, Value string }

type User struct {
	Account, Name string
	Email         string // "" = hidden by the user's profile settings
	Customer      bool
}

type Comment struct {
	ID, Author       string          // author account
	Body             json.RawMessage // ADF doc
	Created, Updated time.Time
	Public           *bool // JSM only
}

type Worklog struct {
	ID, Author, Started, Spent string
	Comment                    json.RawMessage
	Created, Updated           time.Time
}

type Attachment struct {
	ID, Filename, Mime, Author string
	Data                       []byte
	Created                    time.Time
}

type LinkType struct{ ID, Name, Inward, Outward string }

// Link reads "From <type.outward> To".
type Link struct{ ID, Type, From, To string }

type Status struct{ ID, Name, Category string } // category: new, indeterminate, done

// Transition leads to status To (a name); no From means from any status.
type Transition struct {
	ID, Name, To string
	From         []string
	Screen       []ScreenField
}

type ScreenField struct {
	ID       string
	Required bool
}

type Workflow struct {
	Name        string
	Statuses    []Status
	Transitions []Transition
}

type Issue struct {
	ID, Key, Project, Type      string // project key, issue type id
	Summary, Status             string
	Assignee, Reporter, Creator string         // account ids
	Fields                      map[string]any // every other field, as Jira JSON
	Created, Updated            time.Time
	Comments                    []*Comment
	Worklogs                    []*Worklog
	Attachments                 []*Attachment
}

type Server struct {
	*httptest.Server
	Clock        func() time.Time // nil: time.Now
	Admin        bool             // the account has Administer Jira
	CommentLimit int              // comments inline in search results (default 20)
	WorklogLimit int              // worklogs inline in search results (default 20)
	RateLimit    int              // the next RateLimit requests get 429 with Retry-After: 0
	Requests     []string         // "METHOD /path" of every request
	Fail         map[string]int   // "METHOD /path" -> status returned instead of handling
	EditHidden   []string         // field ids on the create screen but not the edit screen

	mu        sync.Mutex
	projects  map[string]*Project // by key
	types     map[string][]IssueType
	fields    map[string][]Field // by "KEY/typeID"
	users     map[string]*User
	issues    map[string]*Issue // by id
	linkTypes []LinkType
	links     []*Link
	workflows map[string]*Workflow // by "KEY/typeID"
	seq       int
	counters  map[string]int // next issue number per project
}

func New() *Server {
	s := &Server{CommentLimit: 20, WorklogLimit: 20, Fail: map[string]int{},
		projects: map[string]*Project{}, types: map[string][]IssueType{}, fields: map[string][]Field{},
		users: map[string]*User{"me": {Account: "me", Name: "Me", Email: "me@x.com"}}, issues: map[string]*Issue{},
		workflows: map[string]*Workflow{}, seq: 10000, counters: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/myself", s.myself)
	mux.HandleFunc("GET /rest/api/3/project/search", s.projectSearch)
	mux.HandleFunc("GET /rest/api/3/issue/createmeta/{p}/issuetypes", s.createmetaTypes)
	mux.HandleFunc("GET /rest/api/3/issue/createmeta/{p}/issuetypes/{t}", s.createmetaFields)
	mux.HandleFunc("POST /rest/api/3/search/jql", s.search)
	mux.HandleFunc("POST /rest/api/3/search/approximate-count", s.count)
	mux.HandleFunc("GET /rest/api/3/issue/{id}", s.getIssue)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/comment", s.getComments)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/worklog", s.getWorklogs)
	mux.HandleFunc("GET /rest/api/3/user/search", s.userSearch)
	mux.HandleFunc("GET /rest/api/3/user/assignable/search", s.userSearch)
	mux.HandleFunc("GET /rest/api/3/issueLinkType", s.getLinkTypes)
	mux.HandleFunc("GET /rest/api/3/mypermissions", s.myPermissions)
	mux.HandleFunc("GET /rest/api/3/workflowscheme/project", s.workflowScheme)
	mux.HandleFunc("GET /rest/api/3/workflow/search", s.workflowSearch)
	mux.HandleFunc("GET /rest/api/3/project/{key}/statuses", s.projectStatuses)
	mux.HandleFunc("GET /rest/api/3/status", s.allStatuses)
	mux.HandleFunc("GET /rest/api/3/attachment/content/{id}", s.attachmentContent)
	for _, register := range registrars { // write endpoints, one file per kind
		register(s, mux)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		key := r.Method + " " + r.URL.Path
		s.Requests = append(s.Requests, key)
		status, fail := s.Fail[key]
		limited := s.RateLimit > 0
		if limited {
			s.RateLimit--
		}
		s.mu.Unlock()
		switch {
		case limited:
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"errorMessages":["rate limited"]}`, http.StatusTooManyRequests)
		case fail:
			http.Error(w, `{"errorMessages":["injected failure"]}`, status)
		default:
			mux.ServeHTTP(w, r)
		}
	}))
	return s
}

// registrars add handlers to a new server; each write-side file appends its own.
var registrars []func(*Server, *http.ServeMux)

func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Server) nextID() string {
	s.seq++
	return strconv.Itoa(s.seq)
}

// Count is how many requests were "METHOD /path" with path starting with prefix.
func (s *Server) Count(method, prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if strings.HasPrefix(r, method+" "+prefix) {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jiraError(w http.ResponseWriter, status int, msg string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"errorMessages": []string{}, "errors": map[string]string{}}
	if msg != "" {
		body["errorMessages"] = []string{msg}
	}
	if fields != nil {
		body["errors"] = fields
	}
	json.NewEncoder(w).Encode(body)
}

// ---- setup ----

func (s *Server) AddProject(p Project, types ...IssueType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Type == "" {
		p.Type = "software"
	}
	if p.ID == "" {
		p.ID = s.nextID()
	}
	s.projects[p.Key] = &p
	s.types[p.Key] = types
}

// SetFields sets the create-screen fields of an issue type.
func (s *Server) SetFields(project, typeID string, fs ...Field) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fields[project+"/"+typeID] = fs
}

func (s *Server) AddUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.Account] = &u
}

func (s *Server) AddLinkType(lt LinkType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkTypes = append(s.linkTypes, lt)
}

func (s *Server) AddLink(l Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == "" {
		l.ID = s.nextID()
	}
	s.links = append(s.links, &l)
}

// SetWorkflow sets the workflow of an issue type (default: To Do, In Progress, Done, all global).
func (s *Server) SetWorkflow(project, typeID string, w Workflow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[project+"/"+typeID] = &w
}

var defaultWorkflow = Workflow{Name: "Simple Workflow",
	Statuses: []Status{{"1", "To Do", "new"}, {"2", "In Progress", "indeterminate"}, {"3", "Done", "done"}},
	Transitions: []Transition{{ID: "11", Name: "To Do", To: "To Do"}, {ID: "21", Name: "Start", To: "In Progress"},
		{ID: "31", Name: "Done", To: "Done"}}}

func (s *Server) workflow(is *Issue) *Workflow {
	if w := s.workflows[is.Project+"/"+is.Type]; w != nil {
		return w
	}
	return &defaultWorkflow
}

// AddIssue stores is; empty ID, Key, Status and times are filled in.
func (s *Server) AddIssue(is Issue) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addIssue(is)
}

func (s *Server) addIssue(is Issue) *Issue {
	if is.ID == "" {
		is.ID = s.nextID()
	}
	if is.Key == "" {
		s.counters[is.Project]++
		is.Key = fmt.Sprintf("%s-%d", is.Project, s.counters[is.Project])
	}
	if is.Status == "" {
		is.Status = s.workflow(&is).Statuses[0].Name
	}
	if is.Created.IsZero() {
		is.Created = s.now()
	}
	if is.Updated.IsZero() {
		is.Updated = is.Created
	}
	if is.Fields == nil {
		is.Fields = map[string]any{}
	}
	if is.Creator == "" {
		is.Creator = is.Reporter
	}
	s.issues[is.ID] = &is
	return &is
}

// Edit changes an issue as another user would; Updated moves to now.
func (s *Server) Edit(id string, f func(*Issue)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	f(is)
	is.Updated = s.now()
}

func (s *Server) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.issues, id)
}

// Move puts an issue in another project under a new key; the id stays.
func (s *Server) Move(id, project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	s.counters[project]++
	is.Project, is.Key = project, fmt.Sprintf("%s-%d", project, s.counters[project])
	is.Updated = s.now()
}

// AddComment adds a comment as another user would; the issue's Updated moves.
func (s *Server) AddComment(issueID string, c Comment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if c.ID == "" {
		c.ID = s.nextID()
	}
	if c.Created.IsZero() {
		c.Created = s.now()
	}
	if c.Updated.IsZero() {
		c.Updated = c.Created
	}
	is.Comments = append(is.Comments, &c)
	is.Updated = s.now()
}

func (s *Server) AddWorklog(issueID string, wl Worklog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if wl.ID == "" {
		wl.ID = s.nextID()
	}
	if wl.Created.IsZero() {
		wl.Created = s.now()
	}
	if wl.Updated.IsZero() {
		wl.Updated = wl.Created
	}
	is.Worklogs = append(is.Worklogs, &wl)
	is.Updated = s.now()
}

func (s *Server) AddAttachment(issueID string, a Attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if a.ID == "" {
		a.ID = s.nextID()
	}
	if a.Created.IsZero() {
		a.Created = s.now()
	}
	is.Attachments = append(is.Attachments, &a)
	is.Updated = s.now()
}

// Issue returns a copy of the stored issue (nil when gone).
func (s *Server) Issue(id string) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	if is == nil {
		return nil
	}
	c := *is
	return &c
}

// ---- rendering ----

func (s *Server) userJSON(account string) any {
	u := s.users[account]
	if u == nil {
		if account == "" {
			return nil
		}
		u = &User{Account: account, Name: account}
	}
	m := map[string]any{"accountId": u.Account, "displayName": u.Name, "active": true, "accountType": "atlassian"}
	if u.Customer {
		m["accountType"] = "customer"
	}
	if u.Email != "" {
		m["emailAddress"] = u.Email
	}
	return m
}

func (s *Server) typeOf(is *Issue) IssueType {
	for _, t := range s.types[is.Project] {
		if t.ID == is.Type {
			return t
		}
	}
	return IssueType{ID: is.Type, Name: "Type " + is.Type}
}

func (s *Server) statusJSON(is *Issue) map[string]any {
	cat := "new"
	for _, st := range s.workflow(is).Statuses {
		if st.Name == is.Status {
			cat = st.Category
		}
	}
	return map[string]any{"name": is.Status, "statusCategory": map[string]any{"key": cat}}
}

func (s *Server) commentJSON(is *Issue, c *Comment) map[string]any {
	m := map[string]any{"id": c.ID, "author": s.userJSON(c.Author), "body": c.Body,
		"created": c.Created.Format(TimeFormat), "updated": c.Updated.Format(TimeFormat)}
	if s.projects[is.Project].Type == "service_desk" {
		pub := true
		if c.Public != nil {
			pub = *c.Public
		}
		m["jsdPublic"] = pub
	}
	return m
}

func (s *Server) worklogJSON(wl *Worklog) map[string]any {
	m := map[string]any{"id": wl.ID, "author": s.userJSON(wl.Author), "started": wl.Started, "timeSpent": wl.Spent,
		"created": wl.Created.Format(TimeFormat), "updated": wl.Updated.Format(TimeFormat)}
	if len(wl.Comment) > 0 {
		m["comment"] = wl.Comment
	}
	return m
}

func (s *Server) linksJSON(is *Issue) []any {
	out := []any{}
	for _, l := range s.links {
		var lt LinkType
		for _, t := range s.linkTypes {
			if t.Name == l.Type {
				lt = t
			}
		}
		typ := map[string]any{"id": lt.ID, "name": lt.Name, "inward": lt.Inward, "outward": lt.Outward}
		switch {
		case l.From == is.Key:
			out = append(out, map[string]any{"id": l.ID, "type": typ, "outwardIssue": map[string]any{"key": l.To}})
		case l.To == is.Key:
			out = append(out, map[string]any{"id": l.ID, "type": typ, "inwardIssue": map[string]any{"key": l.From}})
		}
	}
	return out
}

func page(items []any, limit int) map[string]any {
	shown := items
	if limit >= 0 && len(items) > limit {
		shown = items[:limit]
	}
	return map[string]any{"total": len(items), "maxResults": len(shown), "startAt": 0, "items": shown}
}

// issueJSON renders is with the given fields ("*all" or nil for every field).
func (s *Server) issueJSON(is *Issue, want []string, inlineLimit bool) map[string]any {
	p := s.projects[is.Project]
	t := s.typeOf(is)
	f := map[string]any{
		"summary":   is.Summary,
		"issuetype": map[string]any{"id": t.ID, "name": t.Name, "subtask": t.Subtask},
		"status":    s.statusJSON(is),
		"project":   map[string]any{"id": p.ID, "key": p.Key, "projectTypeKey": p.Type},
		"created":   is.Created.Format(TimeFormat), "updated": is.Updated.Format(TimeFormat),
		"assignee": s.userJSON(is.Assignee), "reporter": s.userJSON(is.Reporter), "creator": s.userJSON(is.Creator),
		"issuelinks": s.linksJSON(is),
	}
	climit, wlimit := -1, -1
	if inlineLimit {
		climit, wlimit = s.CommentLimit, s.WorklogLimit
	}
	var cs, ws, as []any
	for _, c := range is.Comments {
		cs = append(cs, s.commentJSON(is, c))
	}
	for _, wl := range is.Worklogs {
		ws = append(ws, s.worklogJSON(wl))
	}
	for _, a := range is.Attachments {
		as = append(as, map[string]any{"id": a.ID, "filename": a.Filename, "mimeType": a.Mime, "size": len(a.Data),
			"created": a.Created.Format(TimeFormat), "author": s.userJSON(a.Author)})
	}
	cp := page(cs, climit)
	cp["comments"] = cp["items"]
	delete(cp, "items")
	wp := page(ws, wlimit)
	wp["worklogs"] = wp["items"]
	delete(wp, "items")
	f["comment"], f["worklog"], f["attachment"] = cp, wp, as
	for k, v := range is.Fields {
		f[k] = v
	}
	all := len(want) == 0
	keep := map[string]bool{}
	for _, w := range want {
		if w == "*all" {
			all = true
		}
		keep[w] = true
	}
	if !all {
		for k := range f {
			if !keep[k] {
				delete(f, k)
			}
		}
	}
	return map[string]any{"id": is.ID, "key": is.Key, "fields": f}
}

// ---- read handlers ----

func (s *Server) myself(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.userJSON("me"))
}

func startMax(r *http.Request, def int) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	max, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if err != nil || max <= 0 {
		max = def
	}
	return start, max
}

func window[T any](items []T, start, max int) []T {
	if start > len(items) {
		return nil
	}
	end := min(start+max, len(items))
	return items[start:end]
}

func (s *Server) projectSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.projects))
	for k := range s.projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	start, max := startMax(r, 50)
	var vals []any
	for _, k := range window(keys, start, max) {
		p := s.projects[k]
		vals = append(vals, map[string]any{"id": p.ID, "key": p.Key, "projectTypeKey": p.Type})
	}
	writeJSON(w, map[string]any{"values": vals, "total": len(keys), "isLast": start+max >= len(keys)})
}

func (s *Server) createmetaTypes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.types[r.PathValue("p")]
	if !ok {
		jiraError(w, 404, "no project "+r.PathValue("p"), nil)
		return
	}
	start, max := startMax(r, 50)
	var vals []any
	for _, t := range window(ts, start, max) {
		vals = append(vals, map[string]any{"id": t.ID, "name": t.Name, "subtask": t.Subtask})
	}
	writeJSON(w, map[string]any{"issueTypes": vals, "total": len(ts), "startAt": start, "maxResults": max})
}

func fieldJSON(f Field) map[string]any {
	sch := map[string]any{"type": f.Type}
	if f.Items != "" {
		sch["items"] = f.Items
	}
	if f.Custom != "" {
		sch["custom"] = f.Custom
	}
	return map[string]any{"fieldId": f.ID, "key": f.ID, "name": f.Name, "required": f.Required, "schema": sch}
}

func (s *Server) createmetaFields(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs := s.fields[r.PathValue("p")+"/"+r.PathValue("t")]
	start, max := startMax(r, 50)
	var vals []any
	for _, f := range window(fs, start, max) {
		vals = append(vals, fieldJSON(f))
	}
	writeJSON(w, map[string]any{"fields": vals, "total": len(fs), "startAt": start, "maxResults": max})
}

// query is the parsed subset of JQL that gfs sends.
type query struct {
	projects []string
	ids      []string
	since    *time.Time
	asc      bool
}

var (
	projectEq = regexp.MustCompile(`^project\s*=\s*"?([A-Z][A-Z0-9_]*)"?$`)
	projectIn = regexp.MustCompile(`^project\s+in\s*\((.*)\)$`)
	idIn      = regexp.MustCompile(`^id\s+in\s*\((.*)\)$`)
	updatedGE = regexp.MustCompile(`^updated\s*>=\s*"([^"]+)"$`)
	relative  = regexp.MustCompile(`^-(\d+)([mhdw])$`)
	orderBy   = regexp.MustCompile(`(?i)\s+ORDER BY updated (ASC|DESC)$`)
)

func unquote(list string) []string {
	var out []string
	for _, v := range strings.Split(list, ",") {
		if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) parseJQL(jql string) (query, error) {
	var q query
	if m := orderBy.FindStringSubmatch(jql); m != nil {
		q.asc = strings.EqualFold(m[1], "ASC")
		jql = jql[:len(jql)-len(m[0])]
	}
	for _, c := range strings.Split(jql, " AND ") {
		c = strings.TrimSpace(c)
		switch {
		case projectEq.MatchString(c):
			q.projects = []string{projectEq.FindStringSubmatch(c)[1]}
		case projectIn.MatchString(c):
			q.projects = unquote(projectIn.FindStringSubmatch(c)[1])
		case idIn.MatchString(c):
			q.ids = unquote(idIn.FindStringSubmatch(c)[1])
		case updatedGE.MatchString(c):
			v := updatedGE.FindStringSubmatch(c)[1]
			var t time.Time
			if m := relative.FindStringSubmatch(v); m != nil {
				n, _ := strconv.Atoi(m[1])
				unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
				t = s.now().Add(-time.Duration(n) * unit)
			} else if d, err := time.Parse("2006-01-02", v); err == nil {
				t = d
			} else {
				return q, fmt.Errorf("bad date %q", v)
			}
			q.since = &t
		default:
			return q, fmt.Errorf("unsupported JQL clause %q", c)
		}
	}
	return q, nil
}

func (s *Server) matching(q query) []*Issue {
	var out []*Issue
	for _, is := range s.issues {
		if len(q.projects) > 0 && !contains(q.projects, is.Project) {
			continue
		}
		if len(q.ids) > 0 && !contains(q.ids, is.ID) {
			continue
		}
		if q.since != nil && is.Updated.Before(*q.since) {
			continue
		}
		out = append(out, is)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.Before(out[j].Updated) == q.asc
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JQL           string   `json:"jql"`
		Fields        []string `json:"fields"`
		MaxResults    int      `json:"maxResults"`
		NextPageToken string   `json:"nextPageToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.parseJQL(req.JQL)
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	max := req.MaxResults
	if max <= 0 || max > 100 {
		max = 50
	}
	start, _ := strconv.Atoi(req.NextPageToken)
	all := s.matching(q)
	var out []any
	for _, is := range window(all, start, max) {
		out = append(out, s.issueJSON(is, req.Fields, true))
	}
	resp := map[string]any{"issues": out, "isLast": start+max >= len(all)}
	if start+max < len(all) {
		resp["nextPageToken"] = strconv.Itoa(start + max)
	}
	writeJSON(w, resp)
}

func (s *Server) count(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JQL string `json:"jql"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.parseJQL(req.JQL)
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	writeJSON(w, map[string]any{"count": len(s.matching(q))})
}

// lookup finds an issue by id or key.
func (s *Server) lookup(idOrKey string) *Issue {
	if is := s.issues[idOrKey]; is != nil {
		return is
	}
	for _, is := range s.issues {
		if is.Key == idOrKey {
			return is
		}
	}
	return nil
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "Issue does not exist or you do not have permission to see it.", nil)
		return
	}
	var fields []string
	if f := r.URL.Query().Get("fields"); f != "" {
		fields = strings.Split(f, ",")
	}
	writeJSON(w, s.issueJSON(is, fields, false))
}

func (s *Server) getComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	start, max := startMax(r, 50)
	var out []any
	for _, c := range window(is.Comments, start, max) {
		out = append(out, s.commentJSON(is, c))
	}
	writeJSON(w, map[string]any{"comments": out, "total": len(is.Comments), "startAt": start, "maxResults": max})
}

func (s *Server) getWorklogs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	start, max := startMax(r, 5000)
	var out []any
	for _, wl := range window(is.Worklogs, start, max) {
		out = append(out, s.worklogJSON(wl))
	}
	writeJSON(w, map[string]any{"worklogs": out, "total": len(is.Worklogs), "startAt": start, "maxResults": max})
}

func (s *Server) userSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(r.URL.Query().Get("query"))
	var accounts []string
	for a, u := range s.users {
		if q != "" && (strings.Contains(strings.ToLower(u.Name), q) || (u.Email != "" && strings.ToLower(u.Email) == q)) {
			accounts = append(accounts, a)
		}
	}
	sort.Strings(accounts)
	out := []any{}
	for _, a := range accounts {
		out = append(out, s.userJSON(a))
	}
	writeJSON(w, out)
}

func (s *Server) getLinkTypes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []any
	for _, lt := range s.linkTypes {
		out = append(out, map[string]any{"id": lt.ID, "name": lt.Name, "inward": lt.Inward, "outward": lt.Outward})
	}
	writeJSON(w, map[string]any{"issueLinkTypes": out})
}

func (s *Server) myPermissions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{"permissions": map[string]any{"ADMINISTER": map[string]any{"havePermission": s.Admin}}})
}

func (s *Server) workflowScheme(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Admin {
		jiraError(w, 403, "You do not have permission", nil)
		return
	}
	pid := r.URL.Query().Get("projectId")
	var p *Project
	for _, x := range s.projects {
		if x.ID == pid {
			p = x
		}
	}
	if p == nil {
		writeJSON(w, map[string]any{"values": []any{}})
		return
	}
	mappings := map[string]string{}
	for _, t := range s.types[p.Key] {
		if wf := s.workflows[p.Key+"/"+t.ID]; wf != nil {
			mappings[t.ID] = wf.Name
		}
	}
	writeJSON(w, map[string]any{"values": []any{map[string]any{"projectIds": []string{pid},
		"workflowScheme": map[string]any{"defaultWorkflow": defaultWorkflow.Name, "issueTypeMappings": mappings}}}})
}

func (s *Server) workflowSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Admin {
		jiraError(w, 403, "You do not have permission", nil)
		return
	}
	name := r.URL.Query().Get("workflowName")
	var wf *Workflow
	if name == defaultWorkflow.Name {
		wf = &defaultWorkflow
	}
	for _, x := range s.workflows {
		if x.Name == name {
			wf = x
		}
	}
	if wf == nil {
		writeJSON(w, map[string]any{"values": []any{}, "total": 0, "isLast": true})
		return
	}
	idOf := map[string]string{}
	var sts []any
	for _, st := range wf.Statuses {
		idOf[st.Name] = st.ID
		sts = append(sts, map[string]any{"id": st.ID, "name": st.Name})
	}
	var ts []any
	for _, t := range wf.Transitions {
		from := []string{}
		for _, f := range t.From {
			from = append(from, idOf[f])
		}
		typ := "global"
		if len(t.From) > 0 {
			typ = "directed"
		}
		ts = append(ts, map[string]any{"id": t.ID, "name": t.Name, "from": from, "to": idOf[t.To], "type": typ})
	}
	ts = append(ts, map[string]any{"id": "1", "name": "Create", "from": []string{}, "to": wf.Statuses[0].ID, "type": "initial"})
	writeJSON(w, map[string]any{"values": []any{map[string]any{"id": map[string]any{"name": wf.Name},
		"statuses": sts, "transitions": ts}}, "total": 1, "isLast": true})
}

func (s *Server) projectStatuses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.PathValue("key")
	var out []any
	for _, t := range s.types[key] {
		wf := s.workflow(&Issue{Project: key, Type: t.ID})
		var sts []any
		for _, st := range wf.Statuses {
			sts = append(sts, map[string]any{"id": st.ID, "name": st.Name, "statusCategory": map[string]any{"key": st.Category}})
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "statuses": sts})
	}
	writeJSON(w, out)
}

func (s *Server) attachmentContent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		for _, a := range is.Attachments {
			if a.ID == r.PathValue("id") {
				w.Header().Set("Content-Type", a.Mime)
				w.Write(a.Data)
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}

func (s *Server) allStatuses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []any
	wfs := []*Workflow{&defaultWorkflow}
	for _, wf := range s.workflows {
		wfs = append(wfs, wf)
	}
	for _, wf := range wfs {
		for _, st := range wf.Statuses {
			if !seen[st.ID] {
				seen[st.ID] = true
				out = append(out, map[string]any{"id": st.ID, "name": st.Name, "statusCategory": map[string]any{"key": st.Category}})
			}
		}
	}
	writeJSON(w, out)
}

// ID is the id of the issue with key, or "".
func (s *Server) ID(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		if is.Key == key {
			return is.ID
		}
	}
	return ""
}
```

- [ ] **Step 10.4: Run the tests**

Run: `go vet ./internal/adapter/jira/jtest/ && go test ./internal/adapter/jira/jtest/ -v`
Expected: PASS.

- [ ] **Step 10.5: Commit**

```bash
git add internal/adapter/jira/jtest
git commit -m "jira: fake Jira server for tests, read side"
```

### Task 11: Agent session: open, list, fetch, cache, workflows

The read half of agent mode (spec §3.1 project resolution, §5.9, §6): full listings per project, incremental JQL listings, sub-resource top-ups, `FullDirs` for projects joining or leaving, the metadata/people/workflows cache, progress, and attachment download.

**Files:**
- Create: `internal/adapter/jira/search.go`
- Create: `internal/adapter/jira/session.go`
- Create: `internal/adapter/jira/workflows.go`
- Test: `internal/adapter/jira/session_test.go`

**Interfaces:**
- Consumes: Tasks 1, 6–10; `adapter.Listing.FullDirs` (Task 4); `adapter.Progress`, `adapter.Reporter` (already on master).
- Produces:
  - `type session struct{ c *atlassian.Client; t target; projects map[string]*projectMeta; meta *metaCache; reg *registry; wf *xmltree.Node; wfStale, wfDirty, metaDirty bool; cacheDir string; report func(adapter.Progress); now func() time.Time }`
  - `func openSession(ctx, target) (*session, error)`; methods `List`, `Fetch`, `Download`, `Close`, `UseCache`, `SetProgress`, `Identity`
  - `(s) resolveProjects`, `(s) sorted() []*projectMeta`, `(s) loadTypes(ctx, p)`, `(s) loadFields`, `(s) setFor(ctx, p, typeID) (map[string]fieldMeta, bool, error)`, `(s) searchFields(ctx, []*projectMeta) ([]string, error)`, `(s) getIssue(ctx, id) (apiIssue, error)`, `(s) allComments`, `(s) allWorklogs`, `(s) resource(ctx, apiIssue, complete bool) (*adapter.Resource, error)`, `(s) listProject`, `(s) listChanged`, `(s) cachePath(name)`
  - `search.go`: `const searchOverlap = 10`, `const searchPage = 100`, `(s) search(ctx, jql, fields, limit, each)`, `quoteKeys`, `formatCursor`, `parseCursor`, `windowMinutes`
  - `workflows.go`: `const workflowsID = "workflows"`, `const workflowsPath = ".workflows.xml"`, `(s) workflows(ctx, rebuild bool) (adapter.Resource, error)`, `buildWorkflows`, `adminWorkflows`, `statusWorkflows`
  - `func plural(n int, word string) string`
  - test helpers in `session_test.go`: `bg`, `site(t) (*jtest.Server, *time.Time)`, `open(t, srv, now, sel, cacheDir) *session`, `paths(l)`, `find(t, l, path)`, constants `typeSelect`, `typeText`

Cache layout under `<tree>/.gfs/cache/jira/`: `meta.json` (projects, issue types, fields), `people.json`, `workflows.xml`. A broken or missing file is rebuilt, never an error.

- [ ] **Step 11.1: Write the failing tests**

`internal/adapter/jira/session_test.go`:

```go
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var bg = context.Background()

const (
	typeSelect = "com.atlassian.jira.plugin.system.customfieldtypes:select"
	typeText   = "com.atlassian.jira.plugin.system.customfieldtypes:textfield"
)

// site is a fake Jira with a software project GEN (Task, Sub-task) and a
// service project SUP (Support), at a fixed clock the test can move.
func site(t *testing.T) (*jtest.Server, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	srv := jtest.New()
	t.Cleanup(srv.Close)
	srv.Clock = func() time.Time { return now }
	srv.AddProject(jtest.Project{Key: "GEN", ID: "10017"}, jtest.IssueType{ID: "1", Name: "Task"}, jtest.IssueType{ID: "2", Name: "Sub-task", Subtask: true})
	srv.AddProject(jtest.Project{Key: "SUP", ID: "10001", Type: "service_desk"}, jtest.IssueType{ID: "3", Name: "Support"})
	common := []jtest.Field{
		{ID: "summary", Name: "Summary", Type: "string", Required: true},
		{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		{ID: "description", Name: "Description", Type: "string"},
		{ID: "priority", Name: "Priority", Type: "priority"},
		{ID: "assignee", Name: "Assignee", Type: "user"},
		{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
	}
	srv.SetFields("GEN", "1", append(common, jtest.Field{ID: "customfield_10050", Name: "Team", Type: "option", Custom: typeSelect})...)
	srv.SetFields("GEN", "2", append(common, jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})...)
	srv.SetFields("SUP", "3", append(common, jtest.Field{ID: "customfield_10040", Name: "Users", Type: "string", Custom: typeText})...)
	srv.AddUser(jtest.User{Account: "712020:a", Name: "Adam"})
	srv.AddUser(jtest.User{Account: "qm:h", Name: "hadas@example.com", Email: "hadas@example.com", Customer: true})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Migrate auth", Assignee: "712020:a", Reporter: "me",
		Created: now.Add(-48 * time.Hour), Updated: now.Add(-3 * time.Hour),
		Fields: map[string]any{"priority": map[string]any{"name": "High"}, "labels": []string{"backend"},
			"customfield_10050": map[string]any{"id": "7", "value": "Platform"}, "customfield_10019": "0|i06csf:",
			"description": json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"Move to OIDC"}]}`))}})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "2", Summary: "Write script", Reporter: "me",
		Created: now.Add(-24 * time.Hour), Updated: now.Add(-2 * time.Hour), Fields: map[string]any{"parent": map[string]any{"key": "GEN-1"}}})
	srv.AddIssue(jtest.Issue{Project: "SUP", Type: "3", Summary: "Promo code", Reporter: "qm:h",
		Created: now.Add(-5 * time.Hour), Updated: now.Add(-time.Hour), Fields: map[string]any{"customfield_10040": "1-10"}})
	return srv, &now
}

func open(t *testing.T, srv *jtest.Server, now *time.Time, sel selection, cache string) *session {
	t.Helper()
	s, err := openSession(bg, target{base: srv.URL, email: "me@x.com", token: "t", sel: sel})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *now }
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	if cache != "" {
		s.UseCache(cache)
	}
	return s
}

// paths lists the resource paths, sorted.
func paths(l adapter.Listing) []string {
	var out []string
	for _, r := range l.Resources {
		out = append(out, r.Path)
	}
	sort.Strings(out)
	return out
}

func find(t *testing.T, l adapter.Listing, path string) *adapter.Resource {
	t.Helper()
	for i := range l.Resources {
		if l.Resources[i].Path == path {
			return &l.Resources[i]
		}
	}
	t.Fatalf("%s not listed: %v", path, paths(l))
	return nil
}

func TestOpenSelection(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{exclude: []string{"SUP"}}, "")
	if len(s.projects) != 1 || s.projects["GEN"] == nil || s.projects["GEN"].JSM {
		t.Fatalf("%+v", s.projects)
	}
	s = open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	if !s.projects["SUP"].JSM {
		t.Fatal("SUP is a service project")
	}
	if _, err := openSession(bg, target{base: srv.URL, sel: selection{keys: []string{"GEN", "NOPE"}}}); err == nil || err.Error() != "project NOPE not found or not visible" {
		t.Fatalf("err %v", err)
	}
	srv.Fail["GET /rest/api/3/project/search"] = 401
	_, err := openSession(bg, target{base: srv.URL, host: "acme.atlassian.net", email: "me@x.com"})
	if err == nil || !strings.HasSuffix(err.Error(), "; check the token with gfs auth set me@x.com --host acme.atlassian.net") {
		t.Fatalf("err %v", err)
	}
}

func TestListFullThenIncremental(t *testing.T) {
	srv, now := site(t)
	cache := t.TempDir()
	s := open(t, srv, now, selection{}, cache)
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".people.xml", ".workflows.xml", "gen/GEN-1 Migrate auth.xml", "gen/GEN-2 Write script.xml", "sup/SUP-1 Promo code.xml"}
	if !l.Full || strings.Join(paths(l), "|") != strings.Join(want, "|") {
		t.Fatalf("full listing: %v %v", l.Full, paths(l))
	}
	if l.Cursor != "GEN=2026-09-29T12:00:00Z,SUP=2026-09-29T12:00:00Z" {
		t.Fatal(l.Cursor)
	}
	gen1 := xmltree.Print(find(t, l, "gen/GEN-1 Migrate auth.xml").Root, 0)
	for _, part := range []string{`<field id="customfield_10050" name="Team">`, `<option id="7">Platform</option>`,
		`<assignee account="712020:a">Adam</assignee>`, `<paragraph>Move to OIDC</paragraph>`} {
		if !strings.Contains(gen1, part) {
			t.Errorf("GEN-1 lacks %s:\n%s", part, gen1)
		}
	}
	if strings.Contains(gen1, "customfield_10019") {
		t.Error("Rank is noise and must not be shown")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// pull --full rereads the field metadata even with a warm cache
	srv.Requests = nil
	if _, err := s.List(bg, ""); err != nil || srv.Count("GET", "/rest/api/3/issue/createmeta/GEN/issuetypes") == 0 {
		t.Fatalf("a full listing must refresh metadata: %v", err)
	}

	// a new process, 20 minutes later: one search, no metadata requests
	*now = now.Add(20 * time.Minute)
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Migrate auth to OIDC" })
	s = open(t, srv, now, selection{}, cache)
	srv.Requests = nil
	l, err = s.List(bg, l.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if l.Full || len(l.FullDirs) != 0 || strings.Join(paths(l), "|") != ".people.xml|.workflows.xml|gen/GEN-1 Migrate auth to OIDC.xml" {
		t.Fatalf("incremental: %v %v %v", l.Full, l.FullDirs, paths(l))
	}
	if srv.Count("POST", "/rest/api/3/search/jql") != 1 || srv.Count("GET", "/rest/api/3/issue/createmeta") != 0 || srv.Count("GET", "/rest/api/3/mypermissions") != 0 {
		t.Fatalf("requests: %v", srv.Requests)
	}
}

func TestListProjectsJoinAndLeave(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{}, "")
	l, err := s.List(bg, "GEN=2026-09-29T11:00:00Z,OLD=2026-09-29T11:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if l.Full || strings.Join(l.FullDirs, ",") != "old,sup" {
		t.Fatalf("FullDirs %v", l.FullDirs)
	}
	find(t, l, "sup/SUP-1 Promo code.xml")
	if !strings.Contains(l.Cursor, "SUP=") || strings.Contains(l.Cursor, "OLD=") {
		t.Fatal(l.Cursor)
	}
}

func TestListTopUps(t *testing.T) {
	srv, now := site(t)
	srv.WorklogLimit, srv.CommentLimit = 2, 1
	gen2 := srv.ID("GEN-2")
	for i := 0; i < 3; i++ {
		srv.AddWorklog(gen2, jtest.Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
		srv.AddComment(gen2, jtest.Comment{Author: "712020:a", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"c"}]}`))})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	root := find(t, l, "gen/GEN-2 Write script.xml").Root
	if len(root.ChildrenNamed("worklog")) != 3 || len(root.ChildrenNamed("comment")) != 3 {
		t.Fatalf("worklogs %d comments %d", len(root.ChildrenNamed("worklog")), len(root.ChildrenNamed("comment")))
	}
	if srv.Count("GET", "/rest/api/3/issue/"+gen2+"/worklog") != 1 || srv.Count("GET", "/rest/api/3/issue/"+gen2+"/comment") != 1 {
		t.Fatal(srv.Requests)
	}
}

func TestFullListingLimitMidPage(t *testing.T) {
	srv, now := site(t)
	for i := 0; i < 250; i++ {
		srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: fmt.Sprint("bulk ", i), Updated: now.Add(-time.Duration(i+10) * time.Minute)})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}, limit: 150}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(l.Resources) - 2; n != 150 {
		t.Fatalf("%d issues listed", n)
	}
	if srv.Count("POST", "/rest/api/3/search/jql") != 2 {
		t.Fatalf("searches: %d", srv.Count("POST", "/rest/api/3/search/jql"))
	}
	if !strings.HasPrefix(l.Resources[0].Path, "gen/GEN-") || strings.Contains(strings.Join(paths(l), "|"), "Migrate auth") {
		t.Fatal("limit keeps the most recently updated issues") // GEN-1 was updated 3h ago, bulk ones within 2.7h
	}
}

func TestListRateLimit(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	srv.RateLimit = 3 // fewer than the retries: the listing succeeds
	if _, err := s.List(bg, ""); err != nil {
		t.Fatal(err)
	}
	srv.RateLimit = 100
	_, err := s.List(bg, "GEN=2026-09-29T11:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429: rate limited") || !strings.Contains(err.Error(), "gfs pull --full") {
		t.Fatalf("err %v", err)
	}
}

func TestFetch(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	gen1, gen2 := srv.ID("GEN-1"), srv.ID("GEN-2")
	r, err := s.Fetch(bg, gen1)
	if err != nil || r.Path != "gen/GEN-1 Migrate auth.xml" || r.Version != "2026-09-29T09:00:00.000+0000" {
		t.Fatalf("%+v %v", r, err)
	}
	srv.Move(gen1, "SUP")
	if _, err := s.Fetch(bg, gen1); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("moved out of the selection: %v", err)
	}
	srv.Delete(gen2)
	if _, err := s.Fetch(bg, gen2); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	p, err := s.Fetch(bg, peopleID)
	if err != nil || p.Path != ".people.xml" || p.Root.Name != "people" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestIssuePathsAndRename(t *testing.T) {
	srv, now := site(t)
	for _, summary := range []string{"a/b", ".hidden", "notes.files", "   "} {
		srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: summary})
	}
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gen/GEN-3 a-b.xml", "gen/GEN-4 .hidden.xml", "gen/GEN-5 notes.files_.xml", "gen/GEN-6.xml"} {
		find(t, l, p)
	}
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Renamed" })
	r, _ := s.Fetch(bg, srv.ID("GEN-1"))
	if r.Path != "gen/GEN-1 Renamed.xml" {
		t.Fatal(r.Path)
	}
}

func TestPeopleResource(t *testing.T) {
	srv, now := site(t)
	srv.AddComment(srv.ID("SUP-1"), jtest.Comment{Author: "qm:h", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"x:9","text":"@Zed"}}]}`))})
	s := open(t, srv, now, selection{}, "")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	got := xmltree.Print(find(t, l, ".people.xml").Root, 0)
	want := `<people id="people">
  <person account="712020:a" type="atlassian" active="true">Adam</person>
  <person account="me" type="atlassian" active="true" email="me@x.com">Me</person>
  <person account="x:9" active="true">Zed</person>
  <person account="qm:h" type="customer" active="true" email="hadas@example.com">hadas@example.com</person>
</people>`
	if got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestWorkflowsFile(t *testing.T) {
	srv, now := site(t)
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "11", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{{ID: "5", Name: "Resolve", To: "Closed", From: []string{"Open"}},
			{ID: "6", Name: "Reopen", To: "Open"}}})
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	wf, err := s.workflows(bg, true)
	if err != nil {
		t.Fatal(err)
	}
	got := xmltree.Print(wf.Root, 0)
	want := `<workflows id="workflows">
  <note>transitions need the Administer Jira permission; run gfs actions with an issue file to see what you can do to it</note>
  <workflow name="SUP Support">
    <scheme project="SUP" type="Support"/>
    <status name="Open" category="new"/>
    <status name="Closed" category="done"/>
  </workflow>
</workflows>`
	if got != want {
		t.Fatalf("non-admin:\n%s", got)
	}
	srv.Admin = true
	wf, _ = s.workflows(bg, true)
	got = xmltree.Print(wf.Root, 0)
	want = `<workflows id="workflows">
  <workflow name="Support flow">
    <scheme project="SUP" type="Support"/>
    <status name="Open" category="new"/>
    <status name="Closed" category="done"/>
    <transition name="Reopen" to="Open"/>
    <transition name="Resolve" to="Closed">
      <from>Open</from>
    </transition>
  </workflow>
</workflows>`
	if got != want {
		t.Fatalf("admin:\n%s", got)
	}
}

func TestDownload(t *testing.T) {
	srv, now := site(t)
	srv.AddAttachment(srv.ID("GEN-1"), jtest.Attachment{ID: "900", Filename: "log.txt", Mime: "text/plain", Data: []byte("hello"), Author: "me"})
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	var b strings.Builder
	info, err := s.Download(bg, srv.ID("GEN-1"), "900", &b)
	if err != nil || b.String() != "hello" || info.Version != "-" || info.Size != 5 {
		t.Fatalf("%+v %v %q", info, err, b.String())
	}
}

func TestCursorCodec(t *testing.T) {
	m := parseCursor("SUP=2026-09-29T12:00:00Z,GEN=2026-09-29T11:00:00Z")
	if len(m) != 2 || formatCursor(m) != "GEN=2026-09-29T11:00:00Z,SUP=2026-09-29T12:00:00Z" {
		t.Fatal(m)
	}
	for _, bad := range []string{"", "2026-09-29T12:00:00Z", "GEN=yesterday", "g e n=2026-09-29T12:00:00Z"} {
		if parseCursor(bad) != nil {
			t.Errorf("%q must mean a full listing", bad)
		}
	}
	since := time.Date(2026, 9, 29, 11, 0, 30, 0, time.UTC)
	if m := windowMinutes(since, since.Add(19*time.Minute)); m != 29 {
		t.Fatal(m)
	}
}
```

- [ ] **Step 11.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestOpen|TestList|TestFetch|TestIssuePathsAndRename|TestPeopleResource|TestWorkflowsFile|TestDownload|TestCursorCodec' -v`
Expected: FAIL, `undefined: openSession`.

- [ ] **Step 11.3: Implement `search.go`**

```go
package jira

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// searchOverlap widens every change search, in minutes, for clock skew and
// search indexing delay (jira spec §6.2).
const searchOverlap = 10

// searchPage is the page size of every search.
const searchPage = 100

type searchReq struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

// search pages through POST /rest/api/3/search/jql, calling each for at most
// limit issues (0: all).
func (s *session) search(ctx context.Context, jql string, fields []string, limit int, each func(apiIssue) error) error {
	req := searchReq{JQL: jql, Fields: fields, MaxResults: searchPage}
	n := 0
	for {
		var resp struct {
			Issues        []apiIssue `json:"issues"`
			NextPageToken string     `json:"nextPageToken"`
			IsLast        bool       `json:"isLast"`
		}
		if err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/search/jql", req, &resp); err != nil {
			return err
		}
		for _, is := range resp.Issues {
			if limit > 0 && n >= limit {
				return nil
			}
			if err := each(is); err != nil {
				return err
			}
			n++
		}
		if resp.IsLast || resp.NextPageToken == "" || (limit > 0 && n >= limit) {
			return nil
		}
		req.NextPageToken = resp.NextPageToken
	}
}

// quoteKeys renders project keys for a JQL "in" list.
func quoteKeys(ps []*projectMeta) string {
	q := make([]string, len(ps))
	for i, p := range ps {
		q[i] = strconv.Quote(p.Key)
	}
	return strings.Join(q, ", ")
}

// formatCursor writes the time each project was last listed:
// "GEN=2026-09-29T21:00:00Z,SUP=…" (jira spec §6.4).
func formatCursor(m map[string]time.Time) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k].UTC().Format(time.RFC3339)
	}
	return strings.Join(parts, ",")
}

// parseCursor reads formatCursor's output; anything unreadable is empty, and
// an empty cursor means list everything.
func parseCursor(c string) map[string]time.Time {
	if c == "" {
		return nil
	}
	out := map[string]time.Time{}
	for _, part := range strings.Split(c, ",") {
		k, v, ok := strings.Cut(part, "=")
		t, err := time.Parse(time.RFC3339, v)
		if !ok || err != nil || !keyRe.MatchString(k) {
			return nil
		}
		out[k] = t
	}
	return out
}

// windowMinutes is how far back a change search reaches: the time since
// since, rounded up, plus searchOverlap. JQL reads absolute dates in the
// account's time zone, so gfs only sends relative ones.
func windowMinutes(since, now time.Time) int {
	m := int(math.Ceil(now.Sub(since).Minutes()))
	return max(m, 0) + searchOverlap
}
```

- [ ] **Step 11.4: Implement `session.go`**

```go
package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// session is agent mode: every selected project of a site (jira spec §6, §7).
type session struct {
	c         *atlassian.Client
	t         target
	projects  map[string]*projectMeta // selected, by key
	meta      *metaCache              // every cached project; the selected ones share pointers
	reg       *registry
	wf        *xmltree.Node // .workflows.xml once built or loaded
	wfStale   bool          // metadata changed since wf was built
	wfDirty   bool          // wf changed since load
	metaDirty bool
	cacheDir  string // <tree>/.gfs/cache/jira; "" without a tree
	report    func(adapter.Progress)
	now       func() time.Time
}

func openSession(ctx context.Context, t target) (*session, error) {
	s := &session{c: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Jira"), t: t,
		projects: map[string]*projectMeta{}, meta: &metaCache{Projects: map[string]*projectMeta{}},
		reg: newRegistry(), report: func(adapter.Progress) {}, now: time.Now}
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	if err := s.resolveProjects(ctx); err != nil {
		return nil, authHint(err, t)
	}
	for k, p := range s.projects {
		s.meta.Projects[k] = p
	}
	return s, nil
}

// authHint says how to fix a token Jira refused (jira spec §10).
func authHint(err error, t target) error {
	if atlassian.Code(err) != "401" {
		return err
	}
	return fmt.Errorf("%w; check the token with gfs auth set %s --host %s", err, t.email, t.host)
}

// resolveProjects turns the selection into projects (jira spec §3.1).
func (s *session) resolveProjects(ctx context.Context) error {
	want := map[string]bool{}
	for _, k := range s.t.sel.keys {
		want[k] = true
	}
	excluded := map[string]bool{}
	for _, k := range s.t.sel.exclude {
		excluded[k] = true
	}
	for start := 0; ; {
		var resp struct {
			Values []struct {
				ID, Key        string
				ProjectTypeKey string `json:"projectTypeKey"`
			} `json:"values"`
			IsLast bool `json:"isLast"`
		}
		if err := s.c.Do(ctx, http.MethodGet, fmt.Sprintf("/rest/api/3/project/search?startAt=%d&maxResults=50", start), nil, &resp); err != nil {
			return err
		}
		for _, v := range resp.Values {
			if excluded[v.Key] || (len(want) > 0 && !want[v.Key]) {
				continue
			}
			s.projects[v.Key] = &projectMeta{Key: v.Key, ID: v.ID, JSM: v.ProjectTypeKey == "service_desk"}
		}
		start += len(resp.Values)
		if resp.IsLast || len(resp.Values) == 0 {
			break
		}
	}
	var missing []string
	for _, k := range s.t.sel.keys {
		if s.projects[k] == nil && !excluded[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("project %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *session) sorted() []*projectMeta {
	out := make([]*projectMeta, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Identity is the account this session acts as (adapter.Identified).
func (s *session) Identity() string { return s.t.email }

// SetProgress reports List's progress to f (adapter.Reporter).
func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }

func (s *session) cachePath(name string) string { return filepath.Join(s.cacheDir, name) }

// UseCache loads metadata, people and workflows kept by earlier runs
// (adapter.Cacher). A cached project whose id changed is ignored.
func (s *session) UseCache(dir string) {
	s.cacheDir = filepath.Join(dir, "jira")
	if m, err := loadMeta(s.cachePath("meta.json")); err == nil {
		for k, p := range s.projects {
			if c := m.Projects[k]; c != nil && c.ID == p.ID && p.Types == nil {
				p.Types = c.Types
			}
			m.Projects[k] = p
		}
		s.meta = m
	}
	s.reg.load(s.cachePath("people.json"))
	if data, err := os.ReadFile(s.cachePath("workflows.xml")); err == nil {
		if n, err := xmltree.ParseString(string(data)); err == nil {
			s.wf = n
		}
	}
}

// Close saves what changed back to the cache.
func (s *session) Close() error {
	if s.cacheDir == "" {
		return nil
	}
	var errs []error
	if s.metaDirty {
		errs = append(errs, s.meta.save(s.cachePath("meta.json")))
	}
	if s.reg.dirty {
		errs = append(errs, s.reg.save(s.cachePath("people.json")))
	}
	if s.wfDirty && s.wf != nil {
		errs = append(errs, writeFileAtomic(s.cachePath("workflows.xml"), []byte(xmltree.Print(s.wf, 0)+"\n")))
	}
	return errors.Join(errs...)
}

// loadTypes reads p's issue types and their create-screen fields (createmeta).
func (s *session) loadTypes(ctx context.Context, p *projectMeta) error {
	types := map[string]*typeMeta{}
	for start := 0; ; {
		var resp struct {
			IssueTypes []struct {
				ID, Name string
				Subtask  bool
			} `json:"issueTypes"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/createmeta/%s/issuetypes?startAt=%d&maxResults=50", url.PathEscape(p.Key), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return fmt.Errorf("issue types of %s: %w", p.Key, err)
		}
		for _, it := range resp.IssueTypes {
			fs, err := s.loadFields(ctx, p.Key, it.ID)
			if err != nil {
				return err
			}
			types[it.ID] = &typeMeta{ID: it.ID, Name: it.Name, Subtask: it.Subtask, Fields: fs}
		}
		start += len(resp.IssueTypes)
		if len(resp.IssueTypes) == 0 || start >= resp.Total {
			break
		}
	}
	p.Types = types
	s.metaDirty, s.wfStale = true, true
	return nil
}

func (s *session) loadFields(ctx context.Context, key, typeID string) (map[string]fieldMeta, error) {
	out := map[string]fieldMeta{}
	for start := 0; ; {
		var resp struct {
			Fields []struct {
				FieldID  string `json:"fieldId"`
				Name     string `json:"name"`
				Required bool   `json:"required"`
				Schema   struct {
					Type, Items, Custom string
				} `json:"schema"`
			} `json:"fields"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/createmeta/%s/issuetypes/%s?startAt=%d&maxResults=200", url.PathEscape(key), url.PathEscape(typeID), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, fmt.Errorf("fields of %s type %s: %w", key, typeID, err)
		}
		for _, f := range resp.Fields {
			out[f.FieldID] = fieldMeta{ID: f.FieldID, Name: f.Name, Type: f.Schema.Type, Items: f.Schema.Items,
				Custom: f.Schema.Custom, Required: f.Required}
		}
		start += len(resp.Fields)
		if len(resp.Fields) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

// setFor is the field set of p's issue type typeID. loaded reports that the
// types were read now, so an issue fetched with the old field list may lack
// some of them.
func (s *session) setFor(ctx context.Context, p *projectMeta, typeID string) (set map[string]fieldMeta, loaded bool, err error) {
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, false, err
		}
		loaded = true
	}
	t := p.Types[typeID]
	if t == nil && !loaded { // a type created since the cache was filled
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, false, err
		}
		loaded, t = true, p.Types[typeID]
	}
	if t == nil { // no create screen: the union of the project's types
		return projectFieldSet(p), loaded, nil
	}
	return fieldSet(t), loaded, nil
}

// searchFields is the field list for searching ps: every type's set plus the
// fields always requested.
func (s *session) searchFields(ctx context.Context, ps []*projectMeta) ([]string, error) {
	seen := map[string]bool{}
	for _, f := range alwaysFields {
		seen[f] = true
	}
	for _, p := range ps {
		if p.Types == nil {
			if err := s.loadTypes(ctx, p); err != nil {
				return nil, err
			}
		}
		for _, t := range p.Types {
			for id := range fieldSet(t) {
				seen[id] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func (s *session) getIssue(ctx context.Context, id string) (apiIssue, error) {
	var is apiIssue
	err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(id)+"?fields=*all", nil, &is)
	return is, err
}

func (s *session) allComments(ctx context.Context, id string) ([]apiComment, error) {
	var out []apiComment
	for start := 0; ; {
		var resp struct {
			Comments []apiComment `json:"comments"`
			Total    int          `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/%s/comment?startAt=%d&maxResults=100", url.PathEscape(id), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Comments...)
		start += len(resp.Comments)
		if len(resp.Comments) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

func (s *session) allWorklogs(ctx context.Context, id string) ([]apiWorklog, error) {
	var out []apiWorklog
	for start := 0; ; {
		var resp struct {
			Worklogs []apiWorklog `json:"worklogs"`
			Total    int          `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/%s/worklog?startAt=%d&maxResults=1000", url.PathEscape(id), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Worklogs...)
		start += len(resp.Worklogs)
		if len(resp.Worklogs) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

// resource turns is into a full resource. complete says is was fetched with
// every field (GET ?fields=*all) rather than a search's field list.
func (s *session) resource(ctx context.Context, is apiIssue, complete bool) (*adapter.Resource, error) {
	key, typeID := is.project()
	p := s.projects[key]
	if p == nil {
		return nil, fmt.Errorf("%w: %s is in project %s, outside this tree", adapter.ErrNotFound, is.Key, key)
	}
	set, loaded, err := s.setFor(ctx, p, typeID)
	if err != nil {
		return nil, err
	}
	if loaded && !complete {
		if is, err = s.getIssue(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	comments, total, err := page[apiComment](is.Fields["comment"], "comments")
	if err != nil {
		return nil, fmt.Errorf("%s comments: %w", is.Key, err)
	}
	if total > len(comments) {
		if comments, err = s.allComments(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	worklogs, total, err := page[apiWorklog](is.Fields["worklog"], "worklogs")
	if err != nil {
		return nil, fmt.Errorf("%s worklogs: %w", is.Key, err)
	}
	if total > len(worklogs) {
		if worklogs, err = s.allWorklogs(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	root, err := decoder{reg: s.reg, jsm: p.JSM}.issue(is, set, comments, worklogs)
	if err != nil {
		return nil, err
	}
	updated := is.str("updated")
	return &adapter.Resource{ID: is.ID, Version: updated, Path: issuePath(p.dir(), is.Key, is.str("summary")), At: updated, Root: root}, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// List reports the selected projects' issues plus .people.xml and
// .workflows.xml (jira spec §6). With a cursor, projects listed before are
// searched for issues updated since; the others are listed in full.
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	start := s.now().UTC()
	prev := parseCursor(cursor)
	l := adapter.Listing{Full: len(prev) == 0}
	if l.Full { // clone or pull --full: rebuild .people.xml, reread field metadata
		s.reg.reset()
		for _, p := range s.projects {
			p.Types = nil
		}
	}
	next := map[string]time.Time{}
	var fresh, known []*projectMeta
	for _, p := range s.sorted() {
		if _, ok := prev[p.Key]; ok {
			known = append(known, p)
		} else {
			fresh = append(fresh, p)
		}
	}
	for i, p := range fresh {
		n, err := s.listProject(ctx, p, &l)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("project %s: %w", p.Key, err)
		}
		s.report(adapter.Progress{Phase: "pages", Done: i + 1, Total: len(fresh), Item: fmt.Sprintf("%s (%s)", p.Key, plural(n, "issue"))})
		if !l.Full {
			l.FullDirs = append(l.FullDirs, p.dir())
		}
		next[p.Key] = start
	}
	for k := range prev {
		if s.projects[k] == nil { // left the selection: all its files go
			l.FullDirs = append(l.FullDirs, strings.ToLower(k))
		}
	}
	sort.Strings(l.FullDirs)
	if len(known) > 0 {
		oldest := start
		for _, p := range known {
			if t := prev[p.Key]; t.Before(oldest) {
				oldest = t
			}
		}
		if err := s.listChanged(ctx, known, windowMinutes(oldest, start), &l); err != nil {
			return adapter.Listing{}, fmt.Errorf("%w (gfs pull --full lists everything without searching)", err)
		}
		for _, p := range known {
			next[p.Key] = start
		}
	}
	wf, err := s.workflows(ctx, len(fresh) > 0)
	if err != nil {
		return adapter.Listing{}, err
	}
	l.Resources = append(l.Resources, s.reg.resource(), wf)
	l.Cursor = formatCursor(next)
	return l, nil
}

// listProject lists every issue of p inside the since/limit window.
func (s *session) listProject(ctx context.Context, p *projectMeta, l *adapter.Listing) (int, error) {
	fields, err := s.searchFields(ctx, []*projectMeta{p})
	if err != nil {
		return 0, err
	}
	jql := fmt.Sprintf("project = %q", p.Key)
	if s.t.sel.since != "" {
		jql += fmt.Sprintf(" AND updated >= %q", sinceDate(s.t.sel.since, s.now()))
	}
	jql += " ORDER BY updated DESC"
	n := 0
	err = s.search(ctx, jql, fields, s.t.sel.limit, func(is apiIssue) error {
		r, err := s.resource(ctx, is, false)
		if errors.Is(err, adapter.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		l.Resources = append(l.Resources, *r)
		n++
		return nil
	})
	return n, err
}

// listChanged lists the issues of ps updated in the last mins minutes.
func (s *session) listChanged(ctx context.Context, ps []*projectMeta, mins int, l *adapter.Listing) error {
	fields, err := s.searchFields(ctx, ps)
	if err != nil {
		return err
	}
	jql := fmt.Sprintf(`project in (%s) AND updated >= "-%dm" ORDER BY updated ASC`, quoteKeys(ps), mins)
	return s.search(ctx, jql, fields, 0, func(is apiIssue) error {
		r, err := s.resource(ctx, is, false)
		if errors.Is(err, adapter.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		l.Resources = append(l.Resources, *r)
		return nil
	})
}

// Fetch returns one resource: an issue by id, or .people.xml / .workflows.xml.
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	switch id {
	case peopleID:
		r := s.reg.resource()
		return &r, nil
	case workflowsID:
		r, err := s.workflows(ctx, false)
		return &r, err
	}
	is, err := s.getIssue(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resource(ctx, is, true)
}

// Download streams an attachment's bytes; Jira attachments have no versions.
func (s *session) Download(ctx context.Context, _ string, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	n, err := s.c.Download(ctx, "/rest/api/3/attachment/content/"+url.PathEscape(attID), w)
	return adapter.AttachmentInfo{Version: "-", Size: n}, err
}
```

Why a search result is refetched when `setFor` had to load metadata: the search asked for the fields of the types known before, so an issue of a new type may be missing fields its own create screen has. One `GET ?fields=*all` per such issue keeps the file complete.

- [ ] **Step 11.5: Implement `workflows.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const (
	workflowsID   = "workflows"
	workflowsPath = ".workflows.xml"
)

// workflows is the .workflows.xml resource (jira spec §5.9), rebuilt when
// asked, when metadata changed, or when none was built or cached yet.
func (s *session) workflows(ctx context.Context, rebuild bool) (adapter.Resource, error) {
	if s.wf == nil || rebuild || s.wfStale {
		root, err := s.buildWorkflows(ctx)
		if err != nil {
			return adapter.Resource{}, fmt.Errorf("workflows: %w", err)
		}
		s.wf, s.wfStale, s.wfDirty = root, false, true
	}
	return adapter.Resource{ID: workflowsID, Version: contentHash(s.wf), Path: workflowsPath, Root: s.wf}, nil
}

func (s *session) buildWorkflows(ctx context.Context) (*xmltree.Node, error) {
	for _, p := range s.sorted() {
		if p.Types == nil {
			if err := s.loadTypes(ctx, p); err != nil {
				return nil, err
			}
		}
	}
	var perm struct {
		Permissions map[string]struct {
			HavePermission bool `json:"havePermission"`
		} `json:"permissions"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/mypermissions?permissions=ADMINISTER", nil, &perm); err != nil {
		return nil, err
	}
	if perm.Permissions["ADMINISTER"].HavePermission {
		return s.adminWorkflows(ctx)
	}
	return s.statusWorkflows(ctx)
}

type scheme struct{ project, typ string }

func typeNames(p *projectMeta) []*typeMeta {
	ts := make([]*typeMeta, 0, len(p.Types))
	for _, t := range p.Types {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return ts
}

// adminWorkflows reads the full graphs: workflow schemes map (project, type)
// to a workflow, and each workflow lists its statuses and transitions.
func (s *session) adminWorkflows(ctx context.Context) (*xmltree.Node, error) {
	var sts []struct {
		ID, Name       string
		StatusCategory struct{ Key string } `json:"statusCategory"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/status", nil, &sts); err != nil {
		return nil, err
	}
	category, statusName := map[string]string{}, map[string]string{}
	for _, st := range sts {
		category[st.ID], statusName[st.ID] = st.StatusCategory.Key, st.Name
	}
	uses := map[string][]scheme{}
	for _, p := range s.sorted() {
		var resp struct {
			Values []struct {
				WorkflowScheme struct {
					DefaultWorkflow   string            `json:"defaultWorkflow"`
					IssueTypeMappings map[string]string `json:"issueTypeMappings"`
				} `json:"workflowScheme"`
			} `json:"values"`
		}
		if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/workflowscheme/project?projectId="+url.QueryEscape(p.ID), nil, &resp); err != nil {
			return nil, err
		}
		if len(resp.Values) == 0 {
			continue
		}
		ws := resp.Values[0].WorkflowScheme
		for _, t := range typeNames(p) {
			name := ws.IssueTypeMappings[t.ID]
			if name == "" {
				name = ws.DefaultWorkflow
			}
			uses[name] = append(uses[name], scheme{p.Key, t.Name})
		}
	}
	names := make([]string, 0, len(uses))
	for n := range uses {
		names = append(names, n)
	}
	sort.Strings(names)
	root := el("workflows", "id", workflowsID)
	for _, name := range names {
		var resp struct {
			Values []struct {
				Statuses []struct {
					ID, Name string
				} `json:"statuses"`
				Transitions []struct {
					Name string   `json:"name"`
					From []string `json:"from"`
					To   string   `json:"to"`
					Type string   `json:"type"`
				} `json:"transitions"`
			} `json:"values"`
		}
		path := "/rest/api/3/workflow/search?expand=transitions,statuses&workflowName=" + url.QueryEscape(name)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		w := el("workflow", "name", name)
		for _, u := range uses[name] {
			w.Children = append(w.Children, el("scheme", "project", u.project, "type", u.typ))
		}
		if len(resp.Values) > 0 {
			v := resp.Values[0]
			for _, st := range v.Statuses {
				statusName[st.ID] = st.Name
				w.Children = append(w.Children, el("status", "name", st.Name, "category", category[st.ID]))
			}
			var ts []*xmltree.Node
			for _, t := range v.Transitions {
				if t.Type == "initial" {
					continue
				}
				n := el("transition", "name", t.Name, "to", statusName[t.To])
				var from []string
				for _, f := range t.From {
					from = append(from, statusName[f])
				}
				sort.Strings(from)
				for _, f := range from {
					n.Children = append(n.Children, textEl("from", f))
				}
				ts = append(ts, n)
			}
			sort.SliceStable(ts, func(i, j int) bool {
				a, _ := ts[i].Attr("name")
				b, _ := ts[j].Attr("name")
				return a < b
			})
			w.Children = append(w.Children, ts...)
		}
		root.Children = append(root.Children, w)
	}
	return root, nil
}

// statusWorkflows is what any user may read: statuses per issue type.
func (s *session) statusWorkflows(ctx context.Context) (*xmltree.Node, error) {
	root := el("workflows", "id", workflowsID)
	root.Children = append(root.Children, textEl("note",
		"transitions need the Administer Jira permission; run gfs actions with an issue file to see what you can do to it"))
	for _, p := range s.sorted() {
		var types []struct {
			ID, Name string
			Statuses []struct {
				Name           string
				StatusCategory struct{ Key string } `json:"statusCategory"`
			} `json:"statuses"`
		}
		if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(p.Key)+"/statuses", nil, &types); err != nil {
			return nil, err
		}
		sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
		for _, t := range types {
			w := el("workflow", "name", p.Key+" "+t.Name)
			w.Children = append(w.Children, el("scheme", "project", p.Key, "type", t.Name))
			for _, st := range t.Statuses {
				w.Children = append(w.Children, el("status", "name", st.Name, "category", st.StatusCategory.Key))
			}
			root.Children = append(root.Children, w)
		}
	}
	return root, nil
}
```

- [ ] **Step 11.6: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS. `TestListFullThenIncremental` proves the Review Focus rule "a pull with nothing new is one search request" for a changed issue: exactly one `POST /search/jql` and no metadata requests with a warm cache. `TestFullListingLimitMidPage` covers Review Focus item 5 (limit stops mid-page after 2 searches). `TestListRateLimit` covers item 4 at session level.

- [ ] **Step 11.7: Commit**

```bash
git add internal/adapter/jira/search.go internal/adapter/jira/session.go internal/adapter/jira/workflows.go internal/adapter/jira/session_test.go
git commit -m "jira: agent session lists, fetches and caches; .people.xml and .workflows.xml"
```

### Task 12: Field edits and transitions

Changed elements become Jira field values, sent in one `PUT`. A changed `<status>` becomes a one-step transition that takes the changed fields on its screen with it (spec §5.3 write column, §5.7, §7.2 steps 2–3, §7.3). The functions are tested directly here; Task 16 calls them from `Check` and `Apply`.

**Files:**
- Create: `internal/adapter/jira/encode.go`
- Create: `internal/adapter/jira/edit.go`
- Create: `internal/adapter/jira/transition.go`
- Create: `internal/adapter/jira/jtest/edit.go`
- Test: `internal/adapter/jira/edit_test.go`, `internal/adapter/jira/transition_test.go`

**Interfaces:**
- Consumes: Tasks 8–11 (`fieldSet`, `projectFieldSet`, `codecOf`, `elemFields`, `systemElems`, `nodesToADF`, `session`, `registry.byEmail`, `loadTypes`), `atlassian.APIError`, `atlassian.Code`.
- Produces:
  - `encode.go`: `child(root, name)`, `textOf(n)`, `attr(n, name)`, `elemName(fieldID)`, `items(n, item)`, `named(texts)`, `optionValue(o)`; `type encoder struct{ s *session; issueKey, project string }` with `user(ctx, n, base, assignable)`, `system(ctx, group, local, base)`, `field(ctx, m, n, base)`; `(s) resolveUser(ctx, text, issueKey, project string, assignable bool) (string, error)`
  - `edit.go`: `type issueCtx struct{ id, key string; p *projectMeta; t *typeMeta; set map[string]fieldMeta }`, `(s) dirList()`, `(s) projectFor(path) (*projectMeta, error)`, `(s) issueCtx(ctx, *adapter.Resource) (*issueCtx, error)`; `type editPlan struct{ put map[string]any; covers map[int][]string; errs map[int]error; status int; editable map[string]bool; from string; tr *apiTransition; trFields map[string]any }`; `type editInput struct{ local, base *xmltree.Node }`; `fieldsByID(root)`, `changedFields(base, local)`; `(s) planEdit(ctx, ic, req) (*editPlan, error)`, `(s) checkEditable`, `fieldError(err, ids)`, `(s) applyEdit(ctx, ic, plan, out []adapter.Result)`
  - `transition.go`: `type apiTransition struct{ ID, Name string; To struct{ Name string }; Fields map[string]struct{ Required bool; Name string; AllowedValues []json.RawMessage } }`, `(s) transitions(ctx, id) ([]apiTransition, error)`, `chooseTransition(ts, want) (apiTransition, error)`, `(s) planTransition(...)`, `(s) currentValue(...)`
  - `jtest/edit.go`: `GET /issue/{id}/editmeta`, `PUT /issue/{id}`, `GET` and `POST /issue/{id}/transitions`; helpers `(s) editFields(is)`, `(s) setField(is, f, raw) string`, `(s) available(is)`, `(s) fieldMeta(is, id)`
  - test helpers (in `edit_test.go`): `editSite(t)`, `change(t, s, id, edit, groups...) (adapter.ApplyRequest, []adapter.Result)`, `setText(root, name, text)`, `remove(root, name)`, `apply(t, s, req, out)`; (in `transition_test.go`): `tr(id, name, to)`, `supportFlow(srv)`

Rules implemented here, all from the spec:
- Clearing a field is removing its element (canon drops empty elements, so the spec's `<assignee/>` cannot exist in a file).
- A people element with `account` is sent by account. Without `account`, the text is resolved: a known email from the registry, else user search (`assignable/search` for the assignee). An email that is hidden but matches exactly one account resolves too. Editing only the text while keeping `account` is refused.
- `<option>` without `id` is sent as `{"value": …}`, with `id` as `{"id": …}`; changing only the text of an option that keeps its `id` is refused.
- An action whose value cannot be encoded fails alone; the other fields are still sent. Jira applies a `PUT` all or nothing, so when Jira refuses one field, the other actions of that `PUT` report `not sent: another field was refused (…)`.
- A `<status>` change fetches transitions, chooses one, moves every changed field that is on its screen into the transition request, and sends the screen's required fields from the file. A missing required field fails the transition before anything is sent.
- The edit screen is fetched only when there is something to `PUT`.

- [ ] **Step 12.1: Write the failing edit tests**

`internal/adapter/jira/edit_test.go`:

```go
package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// editSite is site() plus options for GEN's Team field, a raw field and two
// users who share a name.
func editSite(t *testing.T) (*jtest.Server, *session) {
	t.Helper()
	srv, now := site(t)
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "description", Name: "Description", Type: "string"},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "assignee", Name: "Assignee", Type: "user"},
		jtest.Field{ID: "labels", Name: "Labels", Type: "array", Items: "string"},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "customfield_10050", Name: "Team", Type: "option", Custom: typeSelect,
			Options: []jtest.Option{{ID: "7", Value: "Platform"}, {ID: "8", Value: "Core"}}},
		jtest.Field{ID: "customfield_10060", Name: "Squad", Type: "team", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:atlassian-team"})
	srv.EditHidden = []string{"resolution"}
	srv.AddUser(jtest.User{Account: "u:s1", Name: "Sam"})
	srv.AddUser(jtest.User{Account: "u:s2", Name: "Sam"})
	srv.AddUser(jtest.User{Account: "u:b", Name: "Bea", Email: "bea@x.com"})
	srv.AddUser(jtest.User{Account: "u:h", Name: "Hidden Email"}) // email not exposed
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Fields["customfield_10060"] = map[string]any{"id": "t1", "name": "Core"} })
	return srv, open(t, srv, now, selection{keys: []string{"GEN"}}, "")
}

// change fetches the issue, lets edit change a copy, and returns the request
// for the given update groups, with results primed for applyEdit.
func change(t *testing.T, s *session, id string, edit func(root *xmltree.Node), groups ...string) (adapter.ApplyRequest, []adapter.Result) {
	t.Helper()
	base, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	local := *base
	local.Root = base.Root.Clone()
	edit(local.Root)
	req := adapter.ApplyRequest{Local: &local, Base: base}
	for _, g := range groups {
		req.Actions = append(req.Actions, adapter.Action{Verb: "update", Group: g})
	}
	out := make([]adapter.Result, len(req.Actions))
	for i, a := range req.Actions {
		out[i].Action = a
	}
	return req, out
}

func setText(root *xmltree.Node, name, text string) {
	n := root.Child(name)
	if n == nil {
		n = el(name)
		root.Children = append(root.Children, n)
	}
	n.Attrs = nil
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

func remove(root *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range root.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	root.Children = kept
}

func apply(t *testing.T, s *session, req adapter.ApplyRequest, out []adapter.Result) []adapter.Result {
	t.Helper()
	ic, err := s.issueCtx(bg, req.Local)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planEdit(bg, ic, req)
	if err != nil {
		t.Fatal(err)
	}
	s.applyEdit(bg, ic, plan, out)
	return out
}

func TestEditFields(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "summary", "Migrate auth to OIDC")
		setText(r, "priority", "Low")
		r.Child("labels").Children = []*xmltree.Node{textEl("label", "backend"), textEl("label", "security")}
		setText(r, "assignee", "bea@x.com")
		team := fieldsByID(r)["customfield_10050"]
		team.Children = []*xmltree.Node{textEl("option", "Core")}
		desc := r.Child("description")
		desc.Children = []*xmltree.Node{textEl("paragraph", "Use Keycloak")}
	}, "summary", "priority", "assignee", "labels", "field", "description")
	for _, r := range apply(t, s, req, out) {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if srv.Count("PUT", "/rest/api/3/issue/") != 1 {
		t.Fatal("one PUT for all fields")
	}
	is := srv.Issue(id)
	if is.Summary != "Migrate auth to OIDC" || is.Assignee != "u:b" || is.Fields["customfield_10050"].(map[string]any)["id"] != "8" {
		t.Fatalf("%+v", is)
	}
	r, _ := s.Fetch(bg, id)
	got := xmltree.Print(r.Root, 0)
	for _, want := range []string{"<priority>Low</priority>", "<label>security</label>", "<paragraph>Use Keycloak</paragraph>", `<assignee account="u:b">Bea</assignee>`} {
		if !strings.Contains(got, want) {
			t.Errorf("after edit, missing %s", want)
		}
	}
}

func TestEditRefusals(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "type", "Bug")
		setText(r, "resolution", "Done")
		setText(r, "assignee", "Sam")
		fieldsByID(r)["customfield_10060"].Children[0].Text = `{"id":"t2"}`
		setText(r, "summary", "Still sent")
	}, "type", "resolution", "assignee", "field", "summary")
	apply(t, s, req, out)
	want := []string{
		"<type> is read-only: Jira cannot change it in place",
		"<resolution> can only be set together with a status change on this issue",
		`<assignee>: "Sam" matches 2 users: Sam (u:s1), Sam (u:s2); write account="…" (see .people.xml)`,
		`<field id="customfield_10060"> (Squad) is read-only in gfs`,
		"",
	}
	for i, r := range out {
		got := ""
		if r.Err != nil {
			got = r.Err.Error()
		}
		if got != want[i] {
			t.Errorf("%s: %q, want %q", r.Action.Group, got, want[i])
		}
	}
	if srv.Issue(id).Summary != "Still sent" {
		t.Fatal("the valid field must still be sent")
	}
}

func TestEditPeopleRules(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		r.Child("assignee").Children[0].Text = "Someone else" // account kept
	}, "assignee")
	apply(t, s, req, out)
	if out[0].Err == nil || !strings.Contains(out[0].Err.Error(), `to reassign, remove account="712020:a"`) {
		t.Fatalf("%v", out[0].Err)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) { remove(r, "assignee") }, "assignee")
	if apply(t, s, req, out)[0].Err != nil || srv.Issue(id).Assignee != "" {
		t.Fatalf("unassign: %v %q", out[0].Err, srv.Issue(id).Assignee)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) { setText(r, "assignee", "hidden@x.com") }, "assignee")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `<assignee>: no user matches "hidden@x.com"` {
		t.Fatalf("%v", out[0].Err)
	}
	req, out = change(t, s, id, func(r *xmltree.Node) {
		a := el("assignee", "account", "u:s2")
		a.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: "Sam"}}
		remove(r, "assignee")
		r.Children = append(r.Children, a)
	}, "assignee")
	if apply(t, s, req, out)[0].Err != nil || srv.Issue(id).Assignee != "u:s2" {
		t.Fatalf("by account: %v", out[0].Err)
	}
}

func TestEditJiraFieldError(t *testing.T) {
	srv, s := editSite(t)
	id := srv.ID("GEN-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		fieldsByID(r)["customfield_10050"].Children = []*xmltree.Node{textEl("option", "Nope")}
		setText(r, "summary", "Not sent")
	}, "summary", "field")
	apply(t, s, req, out)
	if out[1].Err == nil || out[1].Err.Error() != `<field id="customfield_10050">: Option value 'Nope' is not valid` || out[1].Code != "400" {
		t.Fatalf("%v %s", out[1].Err, out[1].Code)
	}
	if out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), "not sent: another field was refused") {
		t.Fatalf("summary: %v", out[0].Err)
	}
	if srv.Issue(id).Summary == "Not sent" {
		t.Fatal("Jira applies an edit all or nothing")
	}
}
```

- [ ] **Step 12.2: Write the failing transition tests**

`internal/adapter/jira/transition_test.go`:

```go
package jira

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func tr(id, name, to string) apiTransition {
	t := apiTransition{ID: id, Name: name}
	t.To.Name = to
	return t
}

func TestChooseTransition(t *testing.T) {
	ts := []apiTransition{tr("1", "Start", "In Progress"), tr("2", "Resolve this issue", "Closed"), tr("3", "Close as duplicate", "Closed")}
	cases := []struct{ want, id, err string }{
		{"in progress", "1", ""},
		{"Resolve this issue", "2", ""},
		{"close AS duplicate", "3", ""},
		{"Closed", "", `several transitions lead to "Closed": "Close as duplicate", "Resolve this issue"; write the transition name in <status> instead`},
		{"Done", "", `"Done" is not reachable from the current status in one step; reachable: "Closed", "In Progress"`},
	}
	for _, c := range cases {
		got, err := chooseTransition(ts, c.want)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%s: err %v", c.want, err)
			}
			continue
		}
		if err != nil || got.ID != c.id {
			t.Errorf("%s: got %+v %v", c.want, got, err)
		}
	}
	if _, err := chooseTransition(nil, "Done"); err == nil || !strings.Contains(err.Error(), "no transitions are available") {
		t.Fatal(err)
	}
}

// supportFlow gives SUP a workflow with two ways into Closed; resolving needs a resolution.
func supportFlow(srv *jtest.Server) {
	srv.SetFields("SUP", "3",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "customfield_10040", Name: "Users", Type: "string", Custom: typeText})
	srv.EditHidden = []string{"resolution"}
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "11", Name: "In Progress", Category: "indeterminate"},
			{ID: "12", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{
			{ID: "21", Name: "Start", To: "In Progress", From: []string{"Open"}},
			{ID: "31", Name: "Resolve this issue", To: "Closed", From: []string{"In Progress"},
				Screen: []jtest.ScreenField{{ID: "resolution", Required: true}, {ID: "customfield_10040"}, {ID: "comment"}}},
			{ID: "41", Name: "Close as duplicate", To: "Closed", From: []string{"In Progress"},
				Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
		}})
	srv.Edit(srv.ID("SUP-1"), func(is *jtest.Issue) { is.Status = "In Progress" })
}

func TestTransitionWithScreenFields(t *testing.T) {
	srv, now := site(t)
	supportFlow(srv)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")

	req, out := change(t, s, id, func(r *xmltree.Node) { setText(r, "status", "Closed") }, "status")
	apply(t, s, req, out)
	if out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `several transitions lead to "Closed"`) {
		t.Fatalf("ambiguous: %v", out[0].Err)
	}

	req, out = change(t, s, id, func(r *xmltree.Node) { setText(r, "status", "Resolve this issue") }, "status")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `transition "Resolve this issue" requires <resolution>; add it` {
		t.Fatalf("required: %v", out[0].Err)
	}
	if srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") != 0 {
		t.Fatal("nothing may be sent when a required field is missing")
	}

	req, out = change(t, s, id, func(r *xmltree.Node) {
		setText(r, "status", "Resolve this issue")
		setText(r, "resolution", "Done")
		setText(r, "priority", "High")
		fieldsByID(r)["customfield_10040"].Children[0].Text = "11-50"
	}, "status", "resolution", "priority", "field")
	apply(t, s, req, out)
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if out[0].Detail != "In Progress -> Closed (Resolve this issue)" {
		t.Fatal(out[0].Detail)
	}
	is := srv.Issue(id)
	if is.Status != "Closed" || is.Fields["resolution"].(map[string]any)["name"] != "Done" || is.Fields["customfield_10040"] != "11-50" ||
		is.Fields["priority"].(map[string]any)["name"] != "High" {
		t.Fatalf("%+v", is)
	}
	if srv.Count("PUT", "/rest/api/3/issue/"+id) != 1 || srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") != 1 {
		t.Fatal(srv.Requests)
	}
	r, _ := s.Fetch(bg, id)
	if got := xmltree.Print(r.Root, 0); !strings.Contains(got, "<status>Closed</status>") || !strings.Contains(got, "<resolution>Done</resolution>") {
		t.Fatalf("write-back:\n%s", got)
	}
}

func TestTransitionUnreachable(t *testing.T) {
	srv, now := site(t)
	supportFlow(srv)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")
	req, out := change(t, s, id, func(r *xmltree.Node) {
		setText(r, "status", "Open")
		setText(r, "priority", "Low")
	}, "status", "priority")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != `"Open" is not reachable from the current status in one step; reachable: "Closed"` {
		t.Fatalf("%v", out[0].Err)
	}
	if out[1].Err != nil || srv.Issue(id).Fields["priority"].(map[string]any)["name"] != "Low" {
		t.Fatalf("the edit still runs: %v", out[1].Err)
	}
}
```

- [ ] **Step 12.3: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestEdit|Transition' -v`
Expected: FAIL, `undefined: fieldsByID` (and the other new names).

- [ ] **Step 12.4: Add the fake server's edit and transition endpoints**

`internal/adapter/jira/jtest/edit.go`:

```go
package jtest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func init() { registrars = append(registrars, (*Server).editRoutes) }

// editRoutes serves field edits and transitions.
func (s *Server) editRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /rest/api/3/issue/{id}/editmeta", s.editmeta)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}", s.editIssue)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/transitions", s.getTransitions)
	mux.HandleFunc("POST /rest/api/3/issue/{id}/transitions", s.doTransition)
}

// editFields is the edit screen of is: its create screen minus EditHidden and
// the issue type, which the edit screen shows without operations.
func (s *Server) editFields(is *Issue) map[string]Field {
	out := map[string]Field{}
	for _, f := range s.fields[is.Project+"/"+is.Type] {
		if f.ID != "issuetype" && !contains(s.EditHidden, f.ID) {
			out[f.ID] = f
		}
	}
	return out
}

func (s *Server) editmeta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	out := map[string]any{}
	for id, f := range s.editFields(is) {
		m := fieldJSON(f)
		m["operations"] = []string{"set"}
		out[id] = m
	}
	writeJSON(w, map[string]any{"fields": out})
}

// setField stores one field value the way Jira would return it; a message
// is a field error.
func (s *Server) setField(is *Issue, f Field, raw json.RawMessage) string {
	var v any
	json.Unmarshal(raw, &v)
	switch f.ID {
	case "summary":
		str, _ := v.(string)
		if str == "" {
			return "You must specify a summary of the issue."
		}
		is.Summary = str
		return ""
	case "assignee", "reporter":
		acc := ""
		if m, ok := v.(map[string]any); ok {
			acc, _ = m["accountId"].(string)
			if s.users[acc] == nil {
				return fmt.Sprintf("User '%s' does not exist.", acc)
			}
		}
		if f.ID == "assignee" {
			is.Assignee = acc
		} else {
			is.Reporter = acc
		}
		return ""
	}
	if f.Type == "option" && v != nil {
		m, _ := v.(map[string]any)
		for _, o := range f.Options {
			if o.ID == m["id"] || o.Value == m["value"] {
				is.Fields[f.ID] = map[string]any{"id": o.ID, "value": o.Value}
				return ""
			}
		}
		return fmt.Sprintf("Option value '%v' is not valid", m["value"])
	}
	if v == nil {
		delete(is.Fields, f.ID)
	} else {
		is.Fields[f.ID] = v
	}
	return ""
}

func (s *Server) editIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	screen := s.editFields(is)
	errs := map[string]string{}
	for id := range body.Fields {
		if _, ok := screen[id]; !ok {
			errs[id] = fmt.Sprintf("Field '%s' cannot be set. It is not on the appropriate screen, or unknown.", id)
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	next := *is
	next.Fields = map[string]any{}
	for k, v := range is.Fields {
		next.Fields[k] = v
	}
	for id, raw := range body.Fields {
		if msg := s.setField(&next, screen[id], raw); msg != "" {
			errs[id] = msg
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	next.Updated = s.now()
	*is = next
	w.WriteHeader(http.StatusNoContent)
}

// available is the transitions of is's workflow that start from its status.
func (s *Server) available(is *Issue) []Transition {
	var out []Transition
	for _, t := range s.workflow(is).Transitions {
		if len(t.From) == 0 || contains(t.From, is.Status) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) fieldMeta(is *Issue, id string) Field {
	for _, f := range s.fields[is.Project+"/"+is.Type] {
		if f.ID == id {
			return f
		}
	}
	return Field{ID: id, Name: id}
}

func (s *Server) getTransitions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	var out []any
	for _, t := range s.available(is) {
		cat := "new"
		for _, st := range s.workflow(is).Statuses {
			if st.Name == t.To {
				cat = st.Category
			}
		}
		fields := map[string]any{}
		for _, sf := range t.Screen {
			f := s.fieldMeta(is, sf.ID)
			m := map[string]any{"required": sf.Required, "name": f.Name}
			if len(f.Options) > 0 {
				var vals []any
				for _, o := range f.Options {
					vals = append(vals, map[string]any{"id": o.ID, "name": o.Value, "value": o.Value})
				}
				m["allowedValues"] = vals
			}
			fields[sf.ID] = m
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "hasScreen": len(t.Screen) > 0,
			"to": map[string]any{"name": t.To, "statusCategory": map[string]any{"key": cat}}, "fields": fields})
	}
	writeJSON(w, map[string]any{"transitions": out})
}

func (s *Server) doTransition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	var tr *Transition
	for _, t := range s.available(is) {
		if t.ID == body.Transition.ID {
			tr = &t
		}
	}
	if tr == nil {
		jiraError(w, 400, "Transition id '"+body.Transition.ID+"' is not valid for this issue.", nil)
		return
	}
	onScreen := map[string]bool{}
	errs := map[string]string{}
	for _, sf := range tr.Screen {
		onScreen[sf.ID] = true
		if v, ok := body.Fields[sf.ID]; sf.Required && (!ok || string(v) == "null") {
			errs[sf.ID] = sf.ID + " is required."
		}
	}
	for id := range body.Fields {
		if !onScreen[id] {
			errs[id] = fmt.Sprintf("Field '%s' cannot be set. It is not on the appropriate screen, or unknown.", id)
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	for id, raw := range body.Fields {
		if msg := s.setField(is, s.fieldMeta(is, id), raw); msg != "" {
			jiraError(w, 400, "", map[string]string{id: msg})
			return
		}
	}
	is.Status = tr.To
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 12.5: Implement `encode.go`**

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// child is root's first child named name; nil-safe.
func child(root *xmltree.Node, name string) *xmltree.Node {
	if root == nil {
		return nil
	}
	return root.Child(name)
}

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.TextContent())
}

// elemName is how a field id is named in messages: its element.
func elemName(id string) string {
	if e, ok := systemElems[id]; ok {
		return "<" + e + ">"
	}
	return fmt.Sprintf("<field id=%q>", id)
}

// encoder turns file elements into Jira field values for one issue.
type encoder struct {
	s        *session
	issueKey string // "" on create
	project  string
}

// user is the value of a people element (jira spec §5.7): its account, or
// the account the text (an email or a unique display name) resolves to.
func (e encoder) user(ctx context.Context, n, base *xmltree.Node, assignable bool) (any, error) {
	if n == nil {
		return nil, nil
	}
	if acc, ok := n.Attr("account"); ok && acc != "" {
		if bacc, _ := attr(base, "account"); bacc == acc && textOf(base) != textOf(n) {
			return nil, fmt.Errorf("<%s>: to reassign, remove account=%q and write an email or a name", n.Name, acc)
		}
		return map[string]any{"accountId": acc}, nil
	}
	text := textOf(n)
	if text == "" {
		return nil, nil
	}
	acc, err := e.s.resolveUser(ctx, text, e.issueKey, e.project, assignable)
	if err != nil {
		return nil, fmt.Errorf("<%s>: %w", n.Name, err)
	}
	return map[string]any{"accountId": acc}, nil
}

func attr(n *xmltree.Node, name string) (string, bool) {
	if n == nil {
		return "", false
	}
	return n.Attr(name)
}

// resolveUser finds the account for an email or an exact display name.
func (s *session) resolveUser(ctx context.Context, text, issueKey, project string, assignable bool) (string, error) {
	if p, ok := s.reg.byEmail(text); ok {
		return p.Account, nil
	}
	path := "/rest/api/3/user/search?query=" + url.QueryEscape(text)
	if assignable {
		path = "/rest/api/3/user/assignable/search?query=" + url.QueryEscape(text)
		if issueKey != "" {
			path += "&issueKey=" + url.QueryEscape(issueKey)
		} else {
			path += "&project=" + url.QueryEscape(project)
		}
	}
	var us []apiUser
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &us); err != nil {
		return "", err
	}
	isEmail := strings.Contains(text, "@")
	var exact []apiUser
	for _, u := range us {
		if u.EmailAddress != "" && strings.EqualFold(u.EmailAddress, text) {
			s.reg.see(u)
			return u.AccountID, nil
		}
		if u.DisplayName == text {
			exact = append(exact, u)
		}
	}
	if len(exact) == 0 && isEmail && len(us) == 1 { // the email is hidden, but only one account matches it
		exact = us
	}
	switch len(exact) {
	case 1:
		s.reg.see(exact[0])
		return exact[0].AccountID, nil
	case 0:
		return "", fmt.Errorf("no user matches %q", text)
	}
	var cands []string
	for _, u := range exact {
		cands = append(cands, fmt.Sprintf("%s (%s)", u.DisplayName, u.AccountID))
	}
	sort.Strings(cands)
	return "", fmt.Errorf("%q matches %d users: %s; write account=\"…\" (see .people.xml)", text, len(exact), strings.Join(cands, ", "))
}

func items(n *xmltree.Node, item string) []string {
	out := []string{}
	if n == nil {
		return out
	}
	for _, c := range n.ChildrenNamed(item) {
		out = append(out, textOf(c))
	}
	return out
}

func named(texts []string) []any {
	out := []any{}
	for _, t := range texts {
		out = append(out, map[string]any{"name": t})
	}
	return out
}

// system is the value of a system field element; local and base are the
// issue roots (base nil on create).
func (e encoder) system(ctx context.Context, group string, local, base *xmltree.Node) (any, error) {
	n := child(local, group)
	switch group {
	case "summary":
		if textOf(n) == "" {
			return nil, fmt.Errorf("<summary> is required")
		}
		return textOf(n), nil
	case "priority", "resolution":
		if textOf(n) == "" {
			return nil, nil
		}
		return map[string]any{"name": textOf(n)}, nil
	case "assignee":
		return e.user(ctx, n, child(base, group), true)
	case "reporter":
		return e.user(ctx, n, child(base, group), false)
	case "parent":
		if textOf(n) == "" {
			return nil, nil
		}
		return map[string]any{"key": textOf(n)}, nil
	case "labels":
		return items(n, "label"), nil
	case "components":
		return named(items(n, "component")), nil
	case "fixVersions", "affectsVersions":
		return named(items(n, "version")), nil
	case "due":
		if textOf(n) == "" {
			return nil, nil
		}
		return textOf(n), nil
	case "timetracking":
		m := map[string]any{}
		if o := textOf(child(n, "original")); o != "" {
			m["originalEstimate"] = o
		}
		if r := textOf(child(n, "remaining")); r != "" {
			m["remainingEstimate"] = r
		}
		return m, nil
	case "environment", "description":
		if n == nil {
			return nil, nil
		}
		v, err := nodesToADF(n.Children)
		if err != nil {
			return nil, fmt.Errorf("<%s>: %w", group, err)
		}
		return v, nil
	case "type", "creator":
		return nil, fmt.Errorf("<%s> is read-only: Jira cannot change it in place", group)
	}
	return nil, fmt.Errorf("<%s> cannot be changed", group)
}

func optionValue(o *xmltree.Node) any {
	if o == nil {
		return nil
	}
	if id, ok := o.Attr("id"); ok && id != "" {
		return map[string]any{"id": id}
	}
	return map[string]any{"value": textOf(o)}
}

// field is the value of a <field> element (jira spec §5.3); n nil clears it.
func (e encoder) field(ctx context.Context, m fieldMeta, n, base *xmltree.Node) (any, error) {
	if n == nil {
		return nil, nil
	}
	label := fmt.Sprintf("<field id=%q>", m.ID)
	if t, _ := n.Attr("type"); t == "raw" || codecOf(m) == cRaw {
		return nil, fmt.Errorf("%s (%s) is read-only in gfs", label, m.Name)
	}
	text := textOf(n)
	switch codecOf(m) {
	case cText, cDate, cDateTime, cKey:
		if text == "" {
			return nil, nil
		}
		return text, nil
	case cNumber:
		if text == "" {
			return nil, nil
		}
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return nil, fmt.Errorf("%s: want a number, got %q", label, text)
		}
		return json.Number(text), nil
	case cADF:
		v, err := nodesToADF(n.Children)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		return v, nil
	case cOption:
		o := n.Child("option")
		if bo := child(base, "option"); o != nil && bo != nil {
			if id, _ := o.Attr("id"); id != "" {
				if bid, _ := bo.Attr("id"); bid == id && textOf(bo) != textOf(o) {
					return nil, fmt.Errorf("%s: to pick another option, remove id=%q and write its value", label, id)
				}
			}
		}
		return optionValue(o), nil
	case cOptions:
		out := []any{}
		for _, o := range n.ChildrenNamed("option") {
			out = append(out, optionValue(o))
		}
		return out, nil
	case cUser:
		return e.user(ctx, n.Child("user"), child(base, "user"), false)
	case cUsers:
		out := []any{}
		for _, u := range n.ChildrenNamed("user") {
			v, err := e.user(ctx, u, nil, false)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out = append(out, v)
			}
		}
		return out, nil
	case cLabels:
		return items(n, "label"), nil
	case cSprint:
		sps := n.ChildrenNamed("sprint")
		if len(sps) == 0 {
			return nil, nil
		}
		id, _ := sps[len(sps)-1].Attr("id")
		if _, err := strconv.Atoi(id); err != nil {
			return nil, fmt.Errorf("%s: <sprint> needs a numeric id", label)
		}
		return json.Number(id), nil
	}
	return nil, fmt.Errorf("%s (%s) is read-only in gfs", label, m.Name)
}
```

- [ ] **Step 12.6: Implement `edit.go`**

```go
package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// issueCtx is what applying changes to one issue file needs to know.
type issueCtx struct {
	id, key string
	p       *projectMeta
	t       *typeMeta // nil when the type has no create screen
	set     map[string]fieldMeta
}

func (s *session) dirList() string {
	var ds []string
	for _, p := range s.sorted() {
		ds = append(ds, p.dir())
	}
	return strings.Join(ds, ", ")
}

// projectFor maps a working path's folder to its project.
func (s *session) projectFor(path string) (*projectMeta, error) {
	dir, _, ok := strings.Cut(path, "/")
	if !ok {
		return nil, fmt.Errorf("issues must live in a project folder (%s)", s.dirList())
	}
	for _, p := range s.projects {
		if p.dir() == dir {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project %q in this tree; gfs does not create projects (projects: %s)", dir, s.dirList())
}

// issueCtx resolves the project, type and field set of res, a file whose
// root carries (for an existing issue) id and key.
func (s *session) issueCtx(ctx context.Context, res *adapter.Resource) (*issueCtx, error) {
	p, err := s.projectFor(res.Path)
	if err != nil {
		return nil, err
	}
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, err
		}
	}
	ic := &issueCtx{id: res.ID, p: p}
	ic.key, _ = res.Root.Attr("key")
	typeName := textOf(child(res.Root, "type"))
	if ic.t = p.typeNamed(typeName); ic.t != nil {
		ic.set = fieldSet(ic.t)
	} else {
		ic.set = projectFieldSet(p)
	}
	return ic, nil
}

// editPlan is the field changes of one file, before anything is sent.
type editPlan struct {
	put      map[string]any   // field id -> value, for PUT /issue
	covers   map[int][]string // action index -> field ids it changes
	errs     map[int]error    // action index -> why it cannot run
	status   int              // index of the <status> action, -1 if none
	editable map[string]bool  // the issue's edit screen (editmeta); nil when not fetched
	from     string           // status before the change
	tr       *apiTransition   // the chosen transition, nil if none
	trFields map[string]any   // field id -> value sent with the transition
}

// editInput is the two versions of the file being applied.
type editInput struct{ local, base *xmltree.Node }

func fieldsByID(root *xmltree.Node) map[string]*xmltree.Node {
	out := map[string]*xmltree.Node{}
	if root == nil {
		return out
	}
	for _, f := range root.ChildrenNamed("field") {
		if id, _ := f.Attr("id"); id != "" {
			out[id] = f
		}
	}
	return out
}

// changedFields is the ids of <field> elements that differ between base and local.
func changedFields(base, local *xmltree.Node) []string {
	b, l := fieldsByID(base), fieldsByID(local)
	seen := map[string]bool{}
	var out []string
	for id := range b {
		seen[id] = true
	}
	for id := range l {
		seen[id] = true
	}
	for id := range seen {
		switch {
		case b[id] == nil || l[id] == nil:
			out = append(out, id)
		case xmltree.Print(b[id], 0) != xmltree.Print(l[id], 0):
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// planEdit turns the update actions of req into field values. It reads the
// edit screen and resolves people, but writes nothing.
func (s *session) planEdit(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest) (*editPlan, error) {
	plan := &editPlan{put: map[string]any{}, covers: map[int][]string{}, errs: map[int]error{}, status: -1}
	enc := encoder{s: s, issueKey: ic.key, project: ic.p.Key}
	var local, base *xmltree.Node
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
	}
	for i, a := range req.Actions {
		if a.Verb != "update" || a.Target != "" || a.IsAttachment() {
			continue
		}
		switch a.Group {
		case "status":
			plan.status = i
		case "field":
			for _, id := range changedFields(base, local) {
				m, ok := ic.set[id]
				if !ok {
					plan.errs[i] = fmt.Errorf("<field id=%q>: not a field of %s issues in %s", id, textOf(child(local, "type")), ic.p.Key)
					break
				}
				v, err := enc.field(ctx, m, fieldsByID(local)[id], fieldsByID(base)[id])
				if err != nil {
					plan.errs[i] = err
					break
				}
				plan.put[id] = v
				plan.covers[i] = append(plan.covers[i], id)
			}
		default:
			id, ok := elemFields[a.Group]
			if !ok {
				plan.errs[i] = fmt.Errorf("<%s> cannot be changed", a.Group)
				continue
			}
			v, err := enc.system(ctx, a.Group, local, base)
			if err != nil {
				plan.errs[i] = err
				continue
			}
			plan.put[id] = v
			plan.covers[i] = []string{id}
		}
	}
	for i, err := range plan.errs { // a failed action sends none of its fields
		if err != nil {
			for _, id := range plan.covers[i] {
				delete(plan.put, id)
			}
		}
	}
	if plan.status >= 0 {
		plan.from = textOf(child(base, "status"))
		s.planTransition(ctx, ic, enc, plan, textOf(child(local, "status")), plan.from, editInput{local, base})
	}
	if len(plan.put) > 0 {
		if err := s.checkEditable(ctx, ic, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// checkEditable refuses fields that are not on the issue's edit screen.
func (s *session) checkEditable(ctx context.Context, ic *issueCtx, plan *editPlan) error {
	var em struct {
		Fields map[string]struct{} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/editmeta", nil, &em); err != nil {
		return fmt.Errorf("edit screen of %s: %w", ic.key, err)
	}
	plan.editable = map[string]bool{}
	for id := range em.Fields {
		plan.editable[id] = true
	}
	for i, ids := range plan.covers {
		if plan.errs[i] != nil {
			continue
		}
		for _, id := range ids {
			if _, withTransition := plan.trFields[id]; withTransition || plan.editable[id] {
				continue
			}
			switch {
			case id == "resolution" && plan.tr != nil:
				plan.errs[i] = fmt.Errorf("<resolution> is on neither the screen of transition %q nor the edit screen", plan.tr.Name)
			case id == "resolution":
				plan.errs[i] = errors.New("<resolution> can only be set together with a status change on this issue")
			default:
				plan.errs[i] = fmt.Errorf("%s is not on the edit screen of %s", elemName(id), ic.key)
			}
			for _, id := range ids {
				delete(plan.put, id)
			}
			break
		}
	}
	return nil
}

// fieldError is err narrowed to the fields of one action, when Jira named
// the failing fields.
func fieldError(err error, ids []string) error {
	var ae *atlassian.APIError
	if !errors.As(err, &ae) || len(ae.Fields) == 0 {
		return err
	}
	var parts []string
	for _, id := range ids {
		if msg, ok := ae.Fields[id]; ok {
			parts = append(parts, elemName(id)+": "+msg)
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("not sent: another field was refused (%s)", ae.Message())
	}
	return errors.New(strings.Join(parts, "; "))
}

// applyEdit sends plan.put in one PUT, then the transition with its screen
// fields, and records each action's outcome in out (jira spec §7.2, §7.3).
func (s *session) applyEdit(ctx context.Context, ic *issueCtx, plan *editPlan, out []adapter.Result) {
	var errPut, errTr error
	if len(plan.put) > 0 {
		errPut = s.c.Do(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(ic.id), map[string]any{"fields": plan.put}, nil)
	}
	if plan.tr != nil && plan.errs[plan.status] == nil {
		body := map[string]any{"transition": map[string]any{"id": plan.tr.ID}}
		if len(plan.trFields) > 0 {
			body["fields"] = plan.trFields
		}
		errTr = s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/transitions", body, nil)
		var ids []string
		for id := range plan.trFields {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if errTr != nil {
			out[plan.status].Err, out[plan.status].Code = fieldError(errTr, ids), atlassian.Code(errTr)
		} else {
			out[plan.status].Detail = fmt.Sprintf("%s -> %s (%s)", plan.from, plan.tr.To.Name, plan.tr.Name)
		}
	}
	for i, ids := range plan.covers {
		if plan.errs[i] != nil {
			continue
		}
		for _, id := range ids {
			err := errPut
			if _, withTransition := plan.trFields[id]; withTransition {
				err = errTr
			}
			if err != nil {
				out[i].Err, out[i].Code = fieldError(err, ids), atlassian.Code(err)
				break
			}
		}
	}
	for i, e := range plan.errs {
		out[i].Err = e
	}
}
```

- [ ] **Step 12.7: Implement `transition.go`**

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type apiTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
	Fields map[string]struct {
		Required      bool              `json:"required"`
		Name          string            `json:"name"`
		AllowedValues []json.RawMessage `json:"allowedValues"`
	} `json:"fields"`
}

// transitions is what the user can do from the issue's status now, with
// each transition's screen fields.
func (s *session) transitions(ctx context.Context, id string) ([]apiTransition, error) {
	var resp struct {
		Transitions []apiTransition `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(id) + "/transitions?expand=transitions.fields"
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("transitions: %w", err)
	}
	return resp.Transitions, nil
}

// chooseTransition picks the transition for the new <status> text (jira spec
// §7.3): the one transition to a status of that name, else the one
// transition of that name.
func chooseTransition(ts []apiTransition, want string) (apiTransition, error) {
	var byTo, byName []apiTransition
	for _, t := range ts {
		if strings.EqualFold(t.To.Name, want) {
			byTo = append(byTo, t)
		}
		if strings.EqualFold(t.Name, want) {
			byName = append(byName, t)
		}
	}
	switch {
	case len(byTo) == 1:
		return byTo[0], nil
	case len(byName) == 1:
		return byName[0], nil
	case len(byTo) > 1:
		var names []string
		for _, t := range byTo {
			names = append(names, fmt.Sprintf("%q", t.Name))
		}
		sort.Strings(names)
		return apiTransition{}, fmt.Errorf("several transitions lead to %q: %s; write the transition name in <status> instead",
			want, strings.Join(names, ", "))
	}
	seen := map[string]bool{}
	var reach []string
	for _, t := range ts {
		if !seen[t.To.Name] {
			seen[t.To.Name] = true
			reach = append(reach, fmt.Sprintf("%q", t.To.Name))
		}
	}
	sort.Strings(reach)
	if len(reach) == 0 {
		return apiTransition{}, fmt.Errorf("%q is not reachable: no transitions are available to you from the current status", want)
	}
	return apiTransition{}, fmt.Errorf("%q is not reachable from the current status in one step; reachable: %s", want, strings.Join(reach, ", "))
}

// planTransition fills plan's transition part: the chosen transition, the
// changed fields that travel with it, and the screen's required fields.
func (s *session) planTransition(ctx context.Context, ic *issueCtx, enc encoder, plan *editPlan, localStatus, baseStatus string, req editInput) {
	if strings.EqualFold(localStatus, baseStatus) {
		return // only the case changed: nothing to do
	}
	ts, err := s.transitions(ctx, ic.id)
	if err != nil {
		plan.errs[plan.status] = err
		return
	}
	tr, err := chooseTransition(ts, localStatus)
	if err != nil {
		plan.errs[plan.status] = err
		return
	}
	plan.tr = &tr
	plan.trFields = map[string]any{}
	for id := range tr.Fields {
		if id == "comment" {
			continue // new comments go through the comment API (jira spec §7.3)
		}
		if v, changed := plan.put[id]; changed {
			plan.trFields[id] = v
			delete(plan.put, id)
			continue
		}
		if !tr.Fields[id].Required {
			continue
		}
		v, present, err := s.currentValue(ctx, ic, enc, id, req)
		switch {
		case err != nil:
			plan.errs[plan.status] = err
			return
		case !present:
			plan.errs[plan.status] = fmt.Errorf("transition %q requires %s; add it", tr.Name, elemName(id))
			return
		}
		plan.trFields[id] = v
	}
}

// currentValue is the file's value of field id, for a required screen field
// the user did not change.
func (s *session) currentValue(ctx context.Context, ic *issueCtx, enc encoder, id string, req editInput) (any, bool, error) {
	if e, ok := systemElems[id]; ok {
		if child(req.local, e) == nil {
			return nil, false, nil
		}
		v, err := enc.system(ctx, e, req.local, req.base)
		return v, err == nil && v != nil, err
	}
	n := fieldsByID(req.local)[id]
	m, ok := ic.set[id]
	if n == nil || !ok {
		return nil, false, nil
	}
	v, err := enc.field(ctx, m, n, fieldsByID(req.base)[id])
	return v, err == nil && v != nil, err
}
```

- [ ] **Step 12.8: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS.

- [ ] **Step 12.9: Commit**

```bash
git add internal/adapter/jira/encode.go internal/adapter/jira/edit.go internal/adapter/jira/transition.go internal/adapter/jira/jtest/edit.go internal/adapter/jira/edit_test.go internal/adapter/jira/transition_test.go
git commit -m "jira: field edits in one PUT; status changes as transitions with their screen fields"
```

### Task 13: Comments, worklogs and links

The sub-resources of spec §5.5, §5.6 and §7.6, as one function that plans (checks and builds) a request and one that sends it. The internal/public rule of JSM projects lives here.

**Files:**
- Create: `internal/adapter/jira/subs.go`
- Modify: `internal/adapter/jira/session.go` (one field in `session`)
- Create: `internal/adapter/jira/jtest/subs.go`
- Test: `internal/adapter/jira/subs_test.go`

**Interfaces:**
- Consumes: Task 12 (`issueCtx`, `textOf`, `attr`, `child`, `change`, `setText` test helpers), `changes.ParseTarget`, `validate.FindSub`, `nodesToADF`.
- Produces:
  - `type linkType struct{ ID, Name, Inward, Outward string }` and the session field `linkTypes []linkType`
  - `func nthNew(root *xmltree.Node, name, idAttr string, nth int) *xmltree.Node`: the element a `name[n]` create action means
  - `(s) loadLinkTypes(ctx)`, `(s) linkBody(ctx, this, phrase, other) (map[string]any, error)`
  - `visibility(c, p) ([]any, error)`, `sameAttr(a, b, name)`, `commentBody(c)`, `worklogBody(w)`
  - `type subPlan struct{ method, path string; body any }`, `(s) planSub(ctx, ic, req, a) (*subPlan, error)` (no writes; Task 16's `Check` uses it), `(s) sub(ctx, ic, req, a) error`
  - `jtest/subs.go`: comment, worklog and link endpoints; `const Author = "me"` (the fake's caller, whose comments are editable)
  - test helpers `subChange`, `adfEl`, `add` (in `subs_test.go`)

- [ ] **Step 13.1: Write the failing tests**

`internal/adapter/jira/subs_test.go`:

```go
package jira

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// subChange is change() for sub-resource actions.
func subChange(t *testing.T, s *session, id string, edit func(root *xmltree.Node), targets ...adapter.Action) (adapter.ApplyRequest, *issueCtx) {
	t.Helper()
	req, _ := change(t, s, id, edit)
	req.Actions = targets
	ic, err := s.issueCtx(bg, req.Local)
	if err != nil {
		t.Fatal(err)
	}
	return req, ic
}

func adfEl(name, text string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	n.Children = []*xmltree.Node{textEl("paragraph", text)}
	return n
}

func add(root *xmltree.Node, n *xmltree.Node) { root.Children = append(root.Children, n) }

func TestCommentsInServiceProject(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")
	create := adapter.Action{Verb: "create", Target: "comment[1]"}

	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "hello")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != `SUP is a service project: mark the comment internal="true" or public="true"` {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "checked", "internal", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "codes attached", "public", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Fetch(bg, id)
	cs := r.Root.ChildrenNamed("comment")
	if len(cs) != 2 {
		t.Fatalf("%d comments", len(cs))
	}
	if v, _ := cs[0].Attr("internal"); v != "true" {
		t.Fatalf("first comment must be internal:\n%s", xmltree.Print(cs[0], 0))
	}
	if v, _ := cs[1].Attr("public"); v != "true" {
		t.Fatalf("second comment must be public:\n%s", xmltree.Print(cs[1], 0))
	}

	cid, _ := cs[0].Attr("id")
	upd := adapter.Action{Verb: "update", Target: "comment[id=" + cid + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		c := r.ChildrenNamed("comment")[0]
		c.DelAttr("internal")
		c.SetAttr("public", "true")
	}, upd)
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.Contains(err.Error(), "visibility of comment "+cid+" cannot be changed") {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		r.ChildrenNamed("comment")[0].Children = []*xmltree.Node{textEl("paragraph", "checked twice")}
	}, upd)
	if err := s.sub(bg, ic, req, upd); err != nil {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "comment[id=" + cid + "]"}
	req, ic = subChange(t, s, id, func(*xmltree.Node) {}, del)
	if err := s.sub(bg, ic, req, del); err != nil || len(srv.Issue(id).Comments) != 1 {
		t.Fatalf("%v %d", err, len(srv.Issue(id).Comments))
	}
}

func TestCommentsElsewhere(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "comment[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, adfEl("comment", "x", "internal", "true")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || !strings.Contains(err.Error(), "GEN is not one") {
		t.Fatalf("%v", err)
	}
	srv.AddComment(id, jtest.Comment{Author: "712020:a", Body: json.RawMessage(doc(`{"type":"paragraph","content":[{"type":"text","text":"theirs"}]}`))})
	r, _ := s.Fetch(bg, id)
	theirs, _ := r.Root.Child("comment").Attr("id")
	upd := adapter.Action{Verb: "update", Target: "comment[id=" + theirs + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		r.Child("comment").Children = []*xmltree.Node{textEl("paragraph", "mine now")}
	}, upd)
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("someone else's comment: %v", err)
	}
}

func TestWorklogs(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "worklog[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, el("worklog")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != "<worklog> needs <started> and <spent>" {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) {
		w := el("worklog")
		w.Children = []*xmltree.Node{textEl("started", "2026-09-29T09:00:00.000+0200"), textEl("spent", "2h"),
			adfEl("comment", "pairing", "type", adfType)}
		add(r, w)
	}, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	wl := srv.Issue(id).Worklogs[0]
	if wl.Spent != "2h" || !strings.Contains(string(wl.Comment), "pairing") {
		t.Fatalf("%+v", wl)
	}
	upd := adapter.Action{Verb: "update", Target: "worklog[id=" + wl.ID + "]"}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { r.Child("worklog").Child("spent").Children[0].Text = "3h" }, upd)
	if err := s.sub(bg, ic, req, upd); err != nil || srv.Issue(id).Worklogs[0].Spent != "3h" {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "worklog[id=" + wl.ID + "]"}
	req, ic = subChange(t, s, id, func(*xmltree.Node) {}, del)
	if err := s.sub(bg, ic, req, del); err != nil || len(srv.Issue(id).Worklogs) != 0 {
		t.Fatal(err)
	}
}

func TestLinks(t *testing.T) {
	srv, now := site(t)
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	srv.AddLinkType(jtest.LinkType{ID: "2", Name: "Relates", Inward: "relates to", Outward: "relates to"})
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	create := adapter.Action{Verb: "create", Target: "link[1]"}
	req, ic := subChange(t, s, id, func(r *xmltree.Node) { add(r, textEl2("link", "GEN-2", "type", "depends on")) }, create)
	if err := s.sub(bg, ic, req, create); err == nil || err.Error() != `unknown link type "depends on"; use one of: "blocks", "is blocked by", "relates to", "relates to"` {
		t.Fatalf("%v", err)
	}
	req, ic = subChange(t, s, id, func(r *xmltree.Node) { add(r, textEl2("link", "GEN-2", "type", "is blocked by")) }, create)
	if err := s.sub(bg, ic, req, create); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Fetch(bg, id)
	l := r.Root.Child("link")
	if typ, _ := l.Attr("type"); typ != "is blocked by" || textOf(l) != "GEN-2" {
		t.Fatalf("GEN-1 after linking:\n%s", xmltree.Print(r.Root, 0))
	}
	other, _ := s.Fetch(bg, srv.ID("GEN-2"))
	if typ, _ := other.Root.Child("link").Attr("type"); typ != "blocks" {
		t.Fatalf("GEN-2 must show the other direction:\n%s", xmltree.Print(other.Root, 0))
	}
	lid, _ := l.Attr("id")
	upd := adapter.Action{Verb: "update", Target: "link[id=" + lid + "]"}
	if err := s.sub(bg, ic, req, upd); err == nil || !strings.HasPrefix(err.Error(), "links cannot be edited") {
		t.Fatal(err)
	}
	del := adapter.Action{Verb: "delete", Target: "link[id=" + lid + "]"}
	if err := s.sub(bg, ic, req, del); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Fetch(bg, id); r.Root.Child("link") != nil {
		t.Fatal("link still there")
	}
}
```

- [ ] **Step 13.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestComments|TestWorklogs|TestLinks' -v`
Expected: FAIL, `undefined: nthNew`.

- [ ] **Step 13.3: Add the link-type cache to the session**

In `internal/adapter/jira/session.go`, add the last field to `session`:

```go
	now       func() time.Time
	linkTypes []linkType // issue link types, read on first use (subs.go)
}
```

- [ ] **Step 13.4: Add the fake server's endpoints**

`internal/adapter/jira/jtest/subs.go`:

```go
package jtest

import (
	"encoding/json"
	"net/http"
	"slices"
)

func init() { registrars = append(registrars, (*Server).subRoutes) }

// subRoutes serves comments, worklogs and links.
func (s *Server) subRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue/{id}/comment", s.addComment)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}/comment/{cid}", s.editComment)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}/comment/{cid}", s.deleteComment)
	mux.HandleFunc("POST /rest/api/3/issue/{id}/worklog", s.addWorklog)
	mux.HandleFunc("PUT /rest/api/3/issue/{id}/worklog/{wid}", s.editWorklog)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}/worklog/{wid}", s.deleteWorklog)
	mux.HandleFunc("POST /rest/api/3/issueLink", s.addLink)
	mux.HandleFunc("DELETE /rest/api/3/issueLink/{lid}", s.deleteLink)
}

// Author is the account the fake treats as the caller.
const Author = "me"

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body       json.RawMessage `json:"body"`
		Properties []struct {
			Key   string `json:"key"`
			Value struct {
				Internal bool `json:"internal"`
			} `json:"value"`
		} `json:"properties"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	pub := true
	for _, p := range body.Properties {
		if p.Key == "sd.public.comment" && p.Value.Internal {
			pub = false
		}
	}
	c := &Comment{ID: s.nextID(), Author: Author, Body: body.Body, Created: s.now(), Updated: s.now(), Public: &pub}
	is.Comments = append(is.Comments, c)
	is.Updated = s.now()
	writeJSON(w, s.commentJSON(is, c))
}

func (s *Server) findComment(w http.ResponseWriter, r *http.Request) (*Issue, int) {
	is := s.lookup(r.PathValue("id"))
	if is != nil {
		for i, c := range is.Comments {
			if c.ID == r.PathValue("cid") {
				if c.Author != Author {
					jiraError(w, 403, "You do not have the permission to edit this comment.", nil)
					return nil, -1
				}
				return is, i
			}
		}
	}
	jiraError(w, 404, "no comment", nil)
	return nil, -1
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body json.RawMessage `json:"body"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findComment(w, r)
	if is == nil {
		return
	}
	is.Comments[i].Body, is.Comments[i].Updated = body.Body, s.now()
	is.Updated = s.now()
	writeJSON(w, s.commentJSON(is, is.Comments[i]))
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findComment(w, r)
	if is == nil {
		return
	}
	is.Comments = slices.Delete(is.Comments, i, i+1)
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}

type worklogBody struct {
	Started   string          `json:"started"`
	TimeSpent string          `json:"timeSpent"`
	Comment   json.RawMessage `json:"comment"`
}

func (s *Server) addWorklog(w http.ResponseWriter, r *http.Request) {
	var body worklogBody
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	if r.URL.Query().Get("adjustEstimate") != "auto" {
		jiraError(w, 400, "gfs always sends adjustEstimate=auto", nil)
		return
	}
	wl := &Worklog{ID: s.nextID(), Author: Author, Started: body.Started, Spent: body.TimeSpent, Comment: body.Comment,
		Created: s.now(), Updated: s.now()}
	is.Worklogs = append(is.Worklogs, wl)
	is.Updated = s.now()
	writeJSON(w, s.worklogJSON(wl))
}

func (s *Server) findWorklog(w http.ResponseWriter, r *http.Request) (*Issue, int) {
	if is := s.lookup(r.PathValue("id")); is != nil {
		for i, wl := range is.Worklogs {
			if wl.ID == r.PathValue("wid") {
				return is, i
			}
		}
	}
	jiraError(w, 404, "no worklog", nil)
	return nil, -1
}

func (s *Server) editWorklog(w http.ResponseWriter, r *http.Request) {
	var body worklogBody
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findWorklog(w, r)
	if is == nil {
		return
	}
	wl := is.Worklogs[i]
	wl.Started, wl.Spent, wl.Comment, wl.Updated = body.Started, body.TimeSpent, body.Comment, s.now()
	is.Updated = s.now()
	writeJSON(w, s.worklogJSON(wl))
}

func (s *Server) deleteWorklog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, i := s.findWorklog(w, r)
	if is == nil {
		return
	}
	is.Worklogs = slices.Delete(is.Worklogs, i, i+1)
	is.Updated = s.now()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type         struct{ Name string } `json:"type"`
		InwardIssue  struct{ Key string }  `json:"inwardIssue"`
		OutwardIssue struct{ Key string }  `json:"outwardIssue"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to := s.lookup(body.InwardIssue.Key), s.lookup(body.OutwardIssue.Key)
	if from == nil || to == nil {
		jiraError(w, 404, "Issue does not exist", nil)
		return
	}
	s.links = append(s.links, &Link{ID: s.nextID(), Type: body.Type.Name, From: from.Key, To: to.Key})
	from.Updated, to.Updated = s.now(), s.now()
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.ID == r.PathValue("lid") {
			for _, k := range []string{l.From, l.To} {
				if is := s.lookup(k); is != nil {
					is.Updated = s.now()
				}
			}
			s.links = slices.Delete(s.links, i, i+1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	jiraError(w, 404, "no link", nil)
}
```

- [ ] **Step 13.5: Implement `subs.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type linkType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// nthNew is root's nth (1-based) child named name without idAttr: the
// sub-resource a "name[n]" create action means.
func nthNew(root *xmltree.Node, name, idAttr string, nth int) *xmltree.Node {
	if root == nil {
		return nil
	}
	i := 0
	for _, c := range root.ChildrenNamed(name) {
		if _, has := c.Attr(idAttr); has {
			continue
		}
		if i++; i == nth {
			return c
		}
	}
	return nil
}

func (s *session) loadLinkTypes(ctx context.Context) ([]linkType, error) {
	if s.linkTypes != nil {
		return s.linkTypes, nil
	}
	var resp struct {
		IssueLinkTypes []linkType `json:"issueLinkTypes"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issueLinkType", nil, &resp); err != nil {
		return nil, fmt.Errorf("link types: %w", err)
	}
	s.linkTypes = resp.IssueLinkTypes
	return s.linkTypes, nil
}

// linkBody is the request that makes "this <phrase> other". A link reads
// "inwardIssue <outward phrase> outwardIssue", the way Jira shows it on the
// inward issue's page (spec §11.4 checks the direction on a real site).
func (s *session) linkBody(ctx context.Context, this, phrase, other string) (map[string]any, error) {
	lts, err := s.loadLinkTypes(ctx)
	if err != nil {
		return nil, err
	}
	for _, lt := range lts {
		switch {
		case strings.EqualFold(lt.Outward, phrase):
			return map[string]any{"type": map[string]any{"name": lt.Name},
				"inwardIssue": map[string]any{"key": this}, "outwardIssue": map[string]any{"key": other}}, nil
		case strings.EqualFold(lt.Inward, phrase):
			return map[string]any{"type": map[string]any{"name": lt.Name},
				"inwardIssue": map[string]any{"key": other}, "outwardIssue": map[string]any{"key": this}}, nil
		}
	}
	var phrases []string
	for _, lt := range lts {
		phrases = append(phrases, fmt.Sprintf("%q", lt.Outward), fmt.Sprintf("%q", lt.Inward))
	}
	sort.Strings(phrases)
	return nil, fmt.Errorf("unknown link type %q; use one of: %s", phrase, strings.Join(phrases, ", "))
}

// visibility checks internal/public on a new comment (jira spec §5.5) and
// returns the comment properties to send.
func visibility(c *xmltree.Node, p *projectMeta) ([]any, error) {
	internal, _ := c.Attr("internal")
	public, _ := c.Attr("public")
	switch {
	case !p.JSM && (internal != "" || public != ""):
		return nil, fmt.Errorf("<comment>: internal and public are for service projects and %s is not one; remove the attribute", p.Key)
	case !p.JSM:
		return nil, nil
	case internal == "true" && public == "true":
		return nil, fmt.Errorf("<comment>: mark it internal or public, not both")
	case internal == "true":
		return []any{map[string]any{"key": "sd.public.comment", "value": map[string]any{"internal": true}}}, nil
	case public == "true":
		return nil, nil
	}
	return nil, fmt.Errorf(`%s is a service project: mark the comment internal="true" or public="true"`, p.Key)
}

func sameAttr(a, b *xmltree.Node, name string) bool {
	x, _ := attr(a, name)
	y, _ := attr(b, name)
	return x == y
}

func commentBody(c *xmltree.Node) (any, error) {
	body, err := nodesToADF(c.Children)
	if err != nil {
		return nil, fmt.Errorf("<comment>: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("<comment> is empty")
	}
	return body, nil
}

func worklogBody(w *xmltree.Node) (map[string]any, error) {
	started, spent := textOf(child(w, "started")), textOf(child(w, "spent"))
	if started == "" || spent == "" {
		return nil, fmt.Errorf("<worklog> needs <started> and <spent>")
	}
	body := map[string]any{"started": started, "timeSpent": spent}
	if c := child(w, "comment"); c != nil {
		v, err := nodesToADF(c.Children)
		if err != nil {
			return nil, fmt.Errorf("<worklog><comment>: %w", err)
		}
		if v != nil {
			body["comment"] = v
		}
	}
	return body, nil
}

// subPlan is one sub-resource request, built by planSub and sent by sub.
type subPlan struct {
	method, path string
	body         any
}

// planSub checks one comment, worklog or link action and builds its
// request (jira spec §7.6). It writes nothing.
func (s *session) planSub(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, a adapter.Action) (*subPlan, error) {
	name, id, nth, ok := changes.ParseTarget(a.Target)
	if !ok {
		return nil, fmt.Errorf("jira has no sub-resource %q", a.Target)
	}
	issue := "/rest/api/3/issue/" + url.PathEscape(ic.id)
	var local, base *xmltree.Node
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
	}
	switch name + " " + a.Verb {
	case "comment create":
		c := nthNew(local, "comment", "id", nth)
		if c == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		props, err := visibility(c, ic.p)
		if err != nil {
			return nil, err
		}
		body, err := commentBody(c)
		if err != nil {
			return nil, err
		}
		m := map[string]any{"body": body}
		if props != nil {
			m["properties"] = props
		}
		return &subPlan{http.MethodPost, issue + "/comment", m}, nil
	case "comment update":
		c, b := validate.FindSub(local, "comment", "id", id), validate.FindSub(base, "comment", "id", id)
		if c == nil || b == nil {
			return nil, fmt.Errorf("%s not found", a.Target)
		}
		if !sameAttr(c, b, "internal") || !sameAttr(c, b, "public") {
			return nil, fmt.Errorf("the visibility of comment %s cannot be changed; add a new comment instead", id)
		}
		body, err := commentBody(c)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPut, issue + "/comment/" + url.PathEscape(id), map[string]any{"body": body}}, nil
	case "comment delete":
		return &subPlan{http.MethodDelete, issue + "/comment/" + url.PathEscape(id), nil}, nil
	case "worklog create":
		w := nthNew(local, "worklog", "id", nth)
		if w == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		body, err := worklogBody(w)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPost, issue + "/worklog?adjustEstimate=auto", body}, nil
	case "worklog update":
		w := validate.FindSub(local, "worklog", "id", id)
		if w == nil {
			return nil, fmt.Errorf("%s not found", a.Target)
		}
		body, err := worklogBody(w)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPut, issue + "/worklog/" + url.PathEscape(id) + "?adjustEstimate=auto", body}, nil
	case "worklog delete":
		return &subPlan{http.MethodDelete, issue + "/worklog/" + url.PathEscape(id) + "?adjustEstimate=auto", nil}, nil
	case "link create":
		l := nthNew(local, "link", "id", nth)
		if l == nil {
			return nil, fmt.Errorf("%s not found in the file", a.Target)
		}
		phrase, _ := l.Attr("type")
		other := textOf(l)
		if phrase == "" || other == "" {
			return nil, fmt.Errorf(`<link> needs type="<phrase>" and the other issue's key, e.g. <link type="blocks">ABC-1</link>`)
		}
		body, err := s.linkBody(ctx, ic.key, phrase, other)
		if err != nil {
			return nil, err
		}
		return &subPlan{http.MethodPost, "/rest/api/3/issueLink", body}, nil
	case "link update":
		return nil, fmt.Errorf("links cannot be edited; delete the <link> and add a new one")
	case "link delete":
		return &subPlan{http.MethodDelete, "/rest/api/3/issueLink/" + url.PathEscape(id), nil}, nil
	}
	return nil, fmt.Errorf("jira cannot %s %s", a.Verb, a.Target)
}

// sub runs one sub-resource action.
func (s *session) sub(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, a adapter.Action) error {
	p, err := s.planSub(ctx, ic, req, a)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, p.method, p.path, p.body, nil)
}
```

About link direction: Jira returns a link on issue A as `outwardIssue: B` with the type's outward phrase when "A blocks B". `linkBody` assumes the create request mirrors that (`inwardIssue: A, outwardIssue: B` makes "A blocks B"). The fake follows the same rule. If the real-site check in Task 21 shows the opposite, swap the two keys in `linkBody`; nothing else depends on it.

- [ ] **Step 13.6: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS.

- [ ] **Step 13.7: Commit**

```bash
git add internal/adapter/jira/subs.go internal/adapter/jira/subs_test.go internal/adapter/jira/session.go internal/adapter/jira/jtest/subs.go
git commit -m "jira: comments with internal/public on service projects, worklogs, links"
```

### Task 14: Attachments

Uploads and deletes (spec §7.6). Download is already in Task 11. Changed bytes never reach the adapter: the schema allows `create delete`, so the generic change computation marks an edited attachment file `!` with "update of attachments is not supported".

**Files:**
- Create: `internal/adapter/jira/attachments.go`
- Create: `internal/adapter/jira/jtest/attach.go`
- Test: `internal/adapter/jira/attachments_test.go`

**Interfaces:**
- Consumes: `atlassian.Client.Upload` (Task 1), `adapter.ParseAttachmentTarget`, `ApplyRequest.Open`.
- Produces: `(s) attachment(ctx, issueID string, req adapter.ApplyRequest, a adapter.Action) (id string, err error)`; `jtest` upload and delete endpoints (uploads require `X-Atlassian-Token: no-check`).

- [ ] **Step 14.1: Write the failing test**

`internal/adapter/jira/attachments_test.go`:

```go
package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestAttachments(t *testing.T) {
	srv, now := site(t)
	s := open(t, srv, now, selection{keys: []string{"GEN"}}, "")
	id := srv.ID("GEN-1")
	sidecar := "gen/GEN-1 Migrate auth.files/"
	create := adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "notes.txt"), File: sidecar + "notes.txt"}
	req := adapter.ApplyRequest{Open: func(rel string) (io.ReadCloser, error) {
		if rel != sidecar+"notes.txt" {
			t.Fatalf("opened %s", rel)
		}
		return io.NopCloser(strings.NewReader("hello")), nil
	}}
	attID, err := s.attachment(bg, id, req, create)
	if err != nil || attID == "" {
		t.Fatal(attID, err)
	}
	r, _ := s.Fetch(bg, id)
	a := r.Root.Child("attachment")
	if name, _ := a.Attr("name"); name != "notes.txt" {
		t.Fatalf("%s", xmltree.Print(r.Root, 0))
	}
	var b strings.Builder
	if _, err := s.Download(bg, id, attID, &b); err != nil || b.String() != "hello" {
		t.Fatal(err, b.String())
	}
	del := adapter.Action{Verb: "delete", Target: adapter.AttachmentTarget("attachment", attID), File: sidecar + "notes.txt"}
	if _, err := s.attachment(bg, id, adapter.ApplyRequest{}, del); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Fetch(bg, id); r.Root.Child("attachment") != nil {
		t.Fatal("still attached")
	}
	upd := adapter.Action{Verb: "update", Target: adapter.AttachmentTarget("attachment", attID), File: sidecar + "notes.txt"}
	if _, err := s.attachment(bg, id, adapter.ApplyRequest{}, upd); err == nil || !strings.Contains(err.Error(), "delete the file and add it again") {
		t.Fatal(err)
	}
	if !issueSchema.Attachment().Allows("create") || issueSchema.Attachment().Allows("update") {
		t.Fatal("the schema must offer create and delete only")
	}
}
```

- [ ] **Step 14.2: Run it to make sure it fails**

Run: `go test ./internal/adapter/jira/ -run TestAttachments -v`
Expected: FAIL, `s.attachment undefined`.

- [ ] **Step 14.3: Add the fake server's endpoints**

`internal/adapter/jira/jtest/attach.go`:

```go
package jtest

import (
	"io"
	"net/http"
	"slices"
)

func init() { registrars = append(registrars, (*Server).attachRoutes) }

func (s *Server) attachRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue/{id}/attachments", s.upload)
	mux.HandleFunc("DELETE /rest/api/3/attachment/{aid}", s.deleteAttachment)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		jiraError(w, 403, "XSRF check failed", nil)
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	data, _ := io.ReadAll(f)
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	a := &Attachment{ID: s.nextID(), Filename: h.Filename, Mime: h.Header.Get("Content-Type"), Author: Author, Data: data, Created: s.now()}
	is.Attachments = append(is.Attachments, a)
	is.Updated = s.now()
	writeJSON(w, []any{map[string]any{"id": a.ID, "filename": a.Filename, "size": len(data), "mimeType": a.Mime}})
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		for i, a := range is.Attachments {
			if a.ID == r.PathValue("aid") {
				is.Attachments = slices.Delete(is.Attachments, i, i+1)
				is.Updated = s.now()
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}
```

- [ ] **Step 14.4: Implement `attachments.go`**

```go
package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// attachment runs one attachment action on issue issueID (jira spec §7.6):
// upload a new file or delete one. Jira attachments have no versions, so the
// schema allows no update and the engine never asks for one.
func (s *session) attachment(ctx context.Context, issueID string, req adapter.ApplyRequest, a adapter.Action) (string, error) {
	attID, _, ok := adapter.ParseAttachmentTarget(a.Target)
	if !ok {
		return "", fmt.Errorf("jira has no attachment target %q", a.Target)
	}
	switch a.Verb {
	case "create":
		if req.Open == nil {
			return "", errors.New("jira: no file reader for uploads")
		}
		f, err := req.Open(a.File)
		if err != nil {
			return "", err
		}
		defer f.Close()
		var resp []struct {
			ID string `json:"id"`
		}
		if err := s.c.Upload(ctx, "/rest/api/3/issue/"+url.PathEscape(issueID)+"/attachments", path.Base(a.File), f, nil, &resp); err != nil {
			return "", err
		}
		if len(resp) == 0 {
			return "", errors.New("jira: upload returned no attachment")
		}
		return resp[0].ID, nil
	case "delete":
		return attID, s.c.Do(ctx, http.MethodDelete, "/rest/api/3/attachment/"+url.PathEscape(attID), nil, nil)
	}
	return "", fmt.Errorf("jira cannot %s attachments; delete the file and add it again", a.Verb)
}
```

- [ ] **Step 14.5: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS.

- [ ] **Step 14.6: Commit**

```bash
git add internal/adapter/jira/attachments.go internal/adapter/jira/attachments_test.go internal/adapter/jira/jtest/attach.go
git commit -m "jira: attachment upload and delete"
```

### Task 15: Create and delete

A new file creates an issue, then moves it to the file's `<status>`, then adds its comments, worklogs, links and attachments. A removed file deletes the issue unless it has sub-tasks (spec §7.4, §7.5).

**Files:**
- Create: `internal/adapter/jira/create.go`
- Create: `internal/adapter/jira/jtest/create.go`
- Modify: `internal/adapter/jira/edit.go` (extract `sendTransition`; `fieldError` keeps the HTTP status)
- Modify: `internal/adapter/jira/jtest/server.go` (`issueJSON` reports `subtasks`)
- Test: `internal/adapter/jira/create_test.go`

**Interfaces:**
- Consumes: Tasks 12–14 (`encoder`, `planTransition`, `sub`, `attachment`, `nthNew`).
- Produces:
  - `type createPlan struct{ ic *issueCtx; fields map[string]any; status string }`, `(s) planCreate(ctx, req) (*createPlan, error)` (no writes), `(s) create(ctx, req) []adapter.Result`, `(s) createStatus(ctx, ic, req, want) error`
  - `(s) checkDelete(ctx, id) error` (no writes), `(s) deleteIssue(ctx, id) error`
  - `(s) sendTransition(ctx, ic, plan) error` in `edit.go`; `type narrowed` (an error restated for some fields that still unwraps to the API error)
  - `jtest`: `POST /rest/api/3/issue` (checks required and off-screen fields like Jira), `DELETE /rest/api/3/issue/{id}` (refuses when sub-tasks exist, unless `deleteSubtasks=true`)
  - test helper `newFile(t, path, x) adapter.ApplyRequest`

- [ ] **Step 15.1: Write the failing tests**

`internal/adapter/jira/create_test.go`:

```go
package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

func newFile(t *testing.T, path, x string) adapter.ApplyRequest {
	t.Helper()
	return adapter.ApplyRequest{Local: &adapter.Resource{Path: path, Root: parse(t, x)},
		Actions: []adapter.Action{{Verb: "create"}}}
}

func TestPlanCreateRefusals(t *testing.T) {
	srv, s := editSite(t)
	srv.SetFields("GEN", "2",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink", Required: true})
	cases := map[string]string{
		`<issue><summary>x</summary></issue>`:                                                     "<type> is required; GEN has Sub-task, Task",
		`<issue><type>Epic</type><summary>x</summary></issue>`:                                    `GEN has no issue type "Epic"; types: Sub-task, Task`,
		`<issue><type>Sub-task</type><summary>x</summary></issue>`:                                "a new Sub-task in GEN needs <parent>",
		`<issue><type>Sub-task</type><summary>x</summary><priority>High</priority></issue>`:       "<priority> is not on the create screen of Sub-task in GEN",
		`<issue><type>Task</type></issue>`:                                                        "a new Task in GEN needs <summary>",
		`<issue><type>Task</type><summary>x</summary><field id="customfield_9">1</field></issue>`: `<field id="customfield_9"> is not on the create screen of Task in GEN`,
	}
	for x, want := range cases {
		_, err := s.planCreate(bg, newFile(t, "gen/New.xml", x))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", x, err, want)
		}
	}
	if _, err := s.planCreate(bg, newFile(t, "ops/New.xml", `<issue><type>Task</type><summary>x</summary></issue>`)); err == nil ||
		err.Error() != `no project "ops" in this tree; gfs does not create projects (projects: gen)` {
		t.Fatal(err)
	}
}

func TestCreateEverything(t *testing.T) {
	srv, s := editSite(t)
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	req := newFile(t, "gen/Rate limiter drops burst traffic.xml", `<issue>
  <summary>Rate limiter drops burst traffic</summary>
  <type>Task</type>
  <status>In Progress</status>
  <priority>High</priority>
  <assignee>bea@x.com</assignee>
  <labels><label>backend</label></labels>
  <field id="customfield_10050"><option>Core</option></field>
  <description type="application/vnd.atlassian.adf+xml"><paragraph>Bursts above 200 rps are dropped.</paragraph></description>
  <link type="blocks">GEN-1</link>
  <comment><paragraph>Found in load test.</paragraph></comment>
  <worklog><started>2026-09-29T09:00:00.000+0200</started><spent>1h</spent></worklog>
</issue>`)
	req.Actions = append(req.Actions, adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "hey.txt"),
		File: "gen/Rate limiter drops burst traffic.files/hey.txt"})
	req.Open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hey -z 10s")), nil }
	out := s.create(bg, req)
	if len(out) != 2 || out[0].Err != nil || out[0].Detail != "GEN-3" || out[1].Err != nil || out[1].ID == "" || out[1].Version != "-" {
		t.Fatalf("%+v", out)
	}
	is := srv.Issue(out[0].ID)
	if is.Status != "In Progress" || is.Assignee != "u:b" || is.Fields["customfield_10050"].(map[string]any)["value"] != "Core" ||
		len(is.Comments) != 1 || len(is.Worklogs) != 1 || len(is.Attachments) != 1 {
		t.Fatalf("%+v", is)
	}
	r, _ := s.Fetch(bg, out[0].ID)
	if r.Path != "gen/GEN-3 Rate limiter drops burst traffic.xml" || r.Root.Child("link") == nil {
		t.Fatalf("%s %v", r.Path, r.Root.Child("link"))
	}
}

func TestCreateKeepsGoingAfterStatusFails(t *testing.T) {
	srv, s := editSite(t)
	out := s.create(bg, newFile(t, "gen/X.xml", `<issue><type>Task</type><summary>X</summary><status>Shipped</status>
  <comment><paragraph>still added</paragraph></comment></issue>`))
	if len(out) != 2 || out[0].Err != nil || out[1].Action.Group != "status" ||
		out[1].Err.Error() != `"Shipped" is not reachable from the current status in one step; reachable: "Done", "In Progress", "To Do"` {
		t.Fatalf("%+v", out)
	}
	if is := srv.Issue(out[0].ID); is == nil || len(is.Comments) != 1 {
		t.Fatal("the issue and its comment exist")
	}
}

func TestDeleteIssue(t *testing.T) {
	srv, s := editSite(t)
	if err := s.deleteIssue(bg, srv.ID("GEN-1")); err == nil || err.Error() != "delete or re-parent its sub-tasks first (GEN-2)" {
		t.Fatal(err)
	}
	if err := s.deleteIssue(bg, srv.ID("GEN-2")); err != nil || srv.Issue(srv.ID("GEN-2")) != nil {
		t.Fatal(err)
	}
	if err := s.deleteIssue(bg, srv.ID("GEN-1")); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 15.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'Create|Delete' -v`
Expected: FAIL, `s.planCreate undefined`.

- [ ] **Step 15.3: Report sub-tasks in the fake**

In `internal/adapter/jira/jtest/server.go`, `issueJSON`, just before `cp := page(cs, climit)`:

```go
	subtasks := []any{}
	for _, c := range s.issues {
		if par, ok := c.Fields["parent"].(map[string]any); ok && par["key"] == is.Key {
			subtasks = append(subtasks, map[string]any{"id": c.ID, "key": c.Key})
		}
	}
	f["subtasks"] = subtasks
```

- [ ] **Step 15.4: Add the fake's create and delete endpoints**

`internal/adapter/jira/jtest/create.go`:

```go
package jtest

import (
	"encoding/json"
	"net/http"
)

func init() { registrars = append(registrars, (*Server).createRoutes) }

func (s *Server) createRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rest/api/3/issue", s.createIssue)
	mux.HandleFunc("DELETE /rest/api/3/issue/{id}", s.deleteIssue)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	var project struct{ Key string }
	var itype struct{ ID string }
	json.Unmarshal(body.Fields["project"], &project)
	json.Unmarshal(body.Fields["issuetype"], &itype)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projects[project.Key] == nil {
		jiraError(w, 400, "", map[string]string{"project": "valid project is required"})
		return
	}
	screen := map[string]Field{}
	for _, f := range s.fields[project.Key+"/"+itype.ID] {
		screen[f.ID] = f
	}
	if len(screen) == 0 {
		jiraError(w, 400, "", map[string]string{"issuetype": "valid issue type is required"})
		return
	}
	errs := map[string]string{}
	for id, f := range screen {
		if _, ok := body.Fields[id]; f.Required && !ok && id != "project" && id != "issuetype" {
			errs[id] = f.Name + " is required."
		}
	}
	for id := range body.Fields {
		if _, ok := screen[id]; !ok && id != "project" && id != "issuetype" {
			errs[id] = "Field '" + id + "' cannot be set. It is not on the appropriate screen, or unknown."
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	is := &Issue{Project: project.Key, Type: itype.ID, Reporter: Author, Fields: map[string]any{}}
	for id, raw := range body.Fields {
		if id == "project" || id == "issuetype" {
			continue
		}
		if msg := s.setField(is, screen[id], raw); msg != "" {
			errs[id] = msg
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	is = s.addIssue(*is)
	writeJSON(w, map[string]any{"id": is.ID, "key": is.Key})
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	for _, c := range s.issues {
		if par, ok := c.Fields["parent"].(map[string]any); ok && par["key"] == is.Key && r.URL.Query().Get("deleteSubtasks") != "true" {
			jiraError(w, 400, "The issue has subtasks and cannot be deleted without them.", nil)
			return
		}
	}
	delete(s.issues, is.ID)
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 15.5: Extract `sendTransition` and keep status codes in `edit.go`**

Replace `fieldError` with this version and the `narrowed` type:

```go
// narrowed is an API error restated for some fields; it still unwraps to
// the API error, so its HTTP status is kept.
type narrowed struct {
	msg string
	err error
}
```

```go
// fieldError is err narrowed to the fields of one action, when Jira named
// the failing fields.
func fieldError(err error, ids []string) error {
	var ae *atlassian.APIError
	if !errors.As(err, &ae) || len(ae.Fields) == 0 {
		return err
	}
	var parts []string
	for _, id := range ids {
		if msg, ok := ae.Fields[id]; ok {
			parts = append(parts, elemName(id)+": "+msg)
		}
	}
	if len(parts) == 0 {
		return &narrowed{"not sent: another field was refused (" + ae.Message() + ")", err}
	}
	return &narrowed{strings.Join(parts, "; "), err}
}
```

In `applyEdit`, the transition block becomes:

```go
	if plan.tr != nil && plan.errs[plan.status] == nil {
		if errTr = s.sendTransition(ctx, ic, plan); errTr != nil {
			out[plan.status].Err, out[plan.status].Code = errTr, atlassian.Code(errTr)
		} else {
			out[plan.status].Detail = fmt.Sprintf("%s -> %s (%s)", plan.from, plan.tr.To.Name, plan.tr.Name)
		}
	}
```

and add at the end of the file:

```go
// sendTransition runs plan's transition with its screen fields.
func (s *session) sendTransition(ctx context.Context, ic *issueCtx, plan *editPlan) error {
	body := map[string]any{"transition": map[string]any{"id": plan.tr.ID}}
	if len(plan.trFields) > 0 {
		body["fields"] = plan.trFields
	}
	err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/transitions", body, nil)
	if err == nil {
		return nil
	}
	ids := make([]string, 0, len(plan.trFields))
	for id := range plan.trFields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fieldError(err, ids)
}
```

- [ ] **Step 15.6: Implement `create.go`**

```go
package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
)

// createPlan is a new issue before it is sent.
type createPlan struct {
	ic     *issueCtx
	fields map[string]any
	status string // the <status> the file asks for; "" keeps the initial one
}

// skipOnCreate are elements a new file may carry that are not create fields.
var skipOnCreate = map[string]bool{"type": true, "status": true, "creator": true}

// planCreate checks a new file and builds the create request (jira spec §7.4).
func (s *session) planCreate(ctx context.Context, req adapter.ApplyRequest) (*createPlan, error) {
	root := req.Local.Root
	p, err := s.projectFor(req.Local.Path)
	if err != nil {
		return nil, err
	}
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, err
		}
	}
	var names []string
	for _, t := range p.Types {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	typeName := textOf(child(root, "type"))
	if typeName == "" {
		return nil, fmt.Errorf("<type> is required; %s has %s", p.Key, strings.Join(names, ", "))
	}
	t := p.typeNamed(typeName)
	if t == nil {
		return nil, fmt.Errorf("%s has no issue type %q; types: %s", p.Key, typeName, strings.Join(names, ", "))
	}
	ic := &issueCtx{p: p, t: t, set: fieldSet(t)}
	enc := encoder{s: s, project: p.Key}
	fields := map[string]any{"project": map[string]any{"key": p.Key}, "issuetype": map[string]any{"id": t.ID}}
	elems := make([]string, 0, len(elemFields))
	for e := range elemFields {
		elems = append(elems, e)
	}
	sort.Strings(elems)
	for _, e := range elems {
		id := elemFields[e]
		if skipOnCreate[e] || child(root, e) == nil {
			continue
		}
		if _, on := t.Fields[id]; !on {
			return nil, fmt.Errorf("%s is not on the create screen of %s in %s", elemName(id), t.Name, p.Key)
		}
		v, err := enc.system(ctx, e, root, nil)
		if err != nil {
			return nil, err
		}
		fields[id] = v
	}
	custom := fieldsByID(root)
	ids := make([]string, 0, len(custom))
	for id := range custom {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m, on := t.Fields[id]
		if !on {
			return nil, fmt.Errorf("%s is not on the create screen of %s in %s", elemName(id), t.Name, p.Key)
		}
		v, err := enc.field(ctx, m, custom[id], nil)
		if err != nil {
			return nil, err
		}
		fields[id] = v
	}
	var missing []string
	for id, m := range t.Fields {
		if m.Required && fields[id] == nil && id != "reporter" {
			missing = append(missing, elemName(id))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("a new %s in %s needs %s", t.Name, p.Key, strings.Join(missing, ", "))
	}
	return &createPlan{ic: ic, fields: fields, status: textOf(child(root, "status"))}, nil
}

// create makes the issue of a new file, then its status, new comments,
// worklogs, links and attachments (jira spec §7.4). The first result is the
// create; later ones report the extras that failed, and every attachment.
func (s *session) create(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	plan, err := s.planCreate(ctx, req)
	if err != nil {
		return []adapter.Result{{Action: act, Err: err}}
	}
	var resp struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue", map[string]any{"fields": plan.fields}, &resp); err != nil {
		return []adapter.Result{{Action: act, Err: err, Code: atlassian.Code(err)}}
	}
	ic := plan.ic
	ic.id, ic.key = resp.ID, resp.Key
	out := []adapter.Result{{Action: act, ID: resp.ID, Detail: resp.Key}}
	failed := func(a adapter.Action, err error) {
		out = append(out, adapter.Result{Action: a, Err: err, Code: atlassian.Code(err)})
	}
	root := req.Local.Root
	if plan.status != "" {
		if err := s.createStatus(ctx, ic, req, plan.status); err != nil {
			failed(adapter.Action{Verb: "update", Group: "status", Class: "transition"}, err)
		}
	}
	for _, name := range []string{"comment", "worklog", "link"} {
		for n := 1; nthNew(root, name, "id", n) != nil; n++ {
			a := adapter.Action{Verb: "create", Target: fmt.Sprintf("%s[%d]", name, n)}
			if err := s.sub(ctx, ic, req, a); err != nil {
				failed(a, err)
			}
		}
	}
	for _, a := range req.Actions {
		if a.IsAttachment() {
			id, err := s.attachment(ctx, resp.ID, req, a)
			out = append(out, adapter.Result{Action: a, ID: id, Version: "-", Err: err, Code: atlassian.Code(err)})
		}
	}
	return out
}

// createStatus moves a new issue from its initial status to want.
func (s *session) createStatus(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, want string) error {
	var cur struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"?fields=status", nil, &cur); err != nil {
		return err
	}
	plan := &editPlan{put: map[string]any{}, covers: map[int][]string{}, errs: map[int]error{}, status: 0}
	enc := encoder{s: s, issueKey: ic.key, project: ic.p.Key}
	s.planTransition(ctx, ic, enc, plan, want, cur.Fields.Status.Name, editInput{local: req.Local.Root})
	if err := plan.errs[0]; err != nil {
		return err
	}
	if plan.tr == nil {
		return nil // already there
	}
	return s.sendTransition(ctx, ic, plan)
}

// checkDelete refuses to delete an issue that has sub-tasks (jira spec §7.5).
func (s *session) checkDelete(ctx context.Context, id string) error {
	var is struct {
		Fields struct {
			Subtasks []issueRef `json:"subtasks"`
		} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(id)+"?fields=subtasks", nil, &is); err != nil {
		return err
	}
	if n := len(is.Fields.Subtasks); n > 0 {
		var keys []string
		for _, st := range is.Fields.Subtasks {
			keys = append(keys, st.Key)
		}
		return fmt.Errorf("delete or re-parent its sub-tasks first (%s)", strings.Join(keys, ", "))
	}
	return nil
}

func (s *session) deleteIssue(ctx context.Context, id string) error {
	if err := s.checkDelete(ctx, id); err != nil {
		return err
	}
	return s.c.Do(ctx, http.MethodDelete, "/rest/api/3/issue/"+url.PathEscape(id), nil, nil)
}
```

- [ ] **Step 15.7: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS (the Task 12 tests still pass after the refactor).

- [ ] **Step 15.8: Commit**

```bash
git add internal/adapter/jira/create.go internal/adapter/jira/create_test.go internal/adapter/jira/edit.go internal/adapter/jira/jtest
git commit -m "jira: create issues with status and sub-resources; delete issues without sub-tasks"
```

### Task 16: Check, Apply, Describe, Available; the adapter goes live

The pieces of Tasks 11–15 become an `adapter.Session`, the adapter registers `jira://`, and the CLI can clone and commit Jira (spec §7.2, §7.7, §7.8, §8). A dry run shows the transition `Check` chose, which needs a one-line engine change.

**Files:**
- Create: `internal/adapter/jira/apply.go`
- Create: `internal/adapter/jira/advise.go`
- Create: `internal/adapter/jira/adapter.go`
- Modify: `internal/engine/commit.go` (`dryRunFile`: prefer a `Check` result's `Detail`)
- Modify: `internal/adapter/fake/fake.go` (`Remote.CheckDetail`)
- Modify: `internal/cli/root.go` (import the adapter)
- Test: `internal/adapter/jira/apply_test.go`, `internal/engine/commit_test.go`, `internal/cli/jira_e2e_test.go` (create)

**Interfaces:**
- Consumes: everything from Tasks 11–15; `adapter.Advisor` (Task 5).
- Produces:
  - `(s *session) Apply(ctx, req) []adapter.Result`, `(s *session) Check(ctx, req) []adapter.Result`, `(s *session) Available(ctx, id, local) (adapter.Advice, error)`
  - `results(acts)`, `failAll(out, err)`, `resourceVerb(acts) (int, string)`, `generated(req) error`, `transitionDetail(plan) string`, `(s) checkCreate(ctx, req) error`
  - `elemTag(fieldID) string`, `allowedNames([]json.RawMessage) []string`
  - `type Adapter struct{}` registered for scheme `jira`: `Name` `jira`, `Schema` `issueSchema`, `PathModel` `adapter.Flat`, `Normalize`, `DefaultDir`, `Open`, `Describe`; `describeSub`, `attrIs`, `topDir`
  - compile-time checks that `*session` is an `adapter.Session`, `Cacher`, `Reporter`, `Advisor` and `Identified`
  - `fake.Remote.CheckDetail map[string]string` (key `"<verb> <group>"`)
  - CLI test helper `jiraSite(t) *jtest.Server` (in `jira_e2e_test.go`, extended by Task 19)

Order inside `Apply` for one existing issue: the `PUT` of changed fields, the transition, then comment/worklog/link actions in the order the engine lists them (creates, updates, deletes), then attachments. A create or a delete is the file's only resource action and is handled alone. `Check` runs the same planning code, so `--dry-run` reports unresolved people, unknown options, unreachable statuses, missing required screen fields, comment visibility and unknown link types, and writes nothing.

- [ ] **Step 16.1: Write the failing engine test**

Append to `internal/engine/commit_test.go`:

```go

// A dry run shows what Check resolved an action to, when Check says.
func TestDryRunShowsCheckDetail(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.CheckDetail = map[string]string{"update title": "rename to One v2 (resolved remotely)"}
	t.Cleanup(func() { ad.Remote.CheckDetail = nil })
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>One v2</title>")
	out.Reset()
	commit(t, env, CommitOpts{DryRun: true})
	if !strings.Contains(out.String(), "would run  rename to One v2 (resolved remotely)") {
		t.Fatal(out.String())
	}
}
```

- [ ] **Step 16.2: Run it to make sure it fails**

Run: `go test ./internal/engine/ -run TestDryRunShowsCheckDetail -v`
Expected: FAIL, `ad.Remote.CheckDetail undefined`.

- [ ] **Step 16.3: Let the fake report check details, and the engine print them**

In `internal/adapter/fake/fake.go`, add to `Remote` (after `Advice`):

```go
	CheckDetail map[string]string // "verb group" -> Result.Detail from Check
```

and in `session.Check` the loop body becomes:

```go
		out = append(out, adapter.Result{Action: a, Err: s.fault(a), Detail: s.r.CheckDetail[a.Verb+" "+a.Group]})
```

In `internal/engine/commit.go`, `dryRunFile`, the last line of the loop becomes:

```go
		detail := a.Detail
		if i < len(checks) && checks[i].Detail != "" {
			detail = checks[i].Detail // what Check resolved the action to
		}
		e.line(a.Verb, actPath(fc, a), a.To, "would run", strings.TrimSpace(detail+"  "+mark))
```

Run: `go test ./internal/engine/`
Expected: PASS.

- [ ] **Step 16.4: Write the failing adapter tests**

`internal/adapter/jira/apply_test.go`:

```go
package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func writes(srv *jtest.Server) int {
	n := 0
	for _, r := range srv.Requests {
		if !strings.HasPrefix(r, "GET ") && !strings.HasPrefix(r, "POST /rest/api/3/search") {
			n++
		}
	}
	return n
}

// wholeFile is GEN-1 with a new summary and status, a comment, a worklog,
// an unknown option and an attachment, in the engine's action order.
func wholeFile(t *testing.T, s *session, srv *jtest.Server, team string) adapter.ApplyRequest {
	t.Helper()
	req, _ := change(t, s, srv.ID("GEN-1"), func(r *xmltree.Node) {
		setText(r, "summary", "Migrate auth to OIDC")
		setText(r, "status", "Done")
		fieldsByID(r)["customfield_10050"].Children = []*xmltree.Node{textEl("option", team)}
		add(r, adfEl("comment", "done"))
		w := el("worklog")
		w.Children = []*xmltree.Node{textEl("started", "2026-09-29T09:00:00.000+0200"), textEl("spent", "30m")}
		add(r, w)
	})
	req.Actions = []adapter.Action{
		{Verb: "update", Group: "summary"}, {Verb: "update", Group: "status"}, {Verb: "update", Group: "field"},
		{Verb: "create", Target: "comment[1]"}, {Verb: "create", Target: "worklog[1]"},
		{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "a.txt"), File: "gen/GEN-1 Migrate auth.files/a.txt"},
	}
	req.Open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a")), nil }
	return req
}

func TestApplyWholeFile(t *testing.T) {
	srv, s := editSite(t)
	out := s.Apply(bg, wholeFile(t, s, srv, "Core"))
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%+v: %v", r.Action, r.Err)
		}
	}
	if out[1].Detail != "To Do -> Done (Done)" || out[5].ID == "" || out[5].Version != "-" {
		t.Fatalf("%+v", out)
	}
	is := srv.Issue(srv.ID("GEN-1"))
	if is.Status != "Done" || is.Summary != "Migrate auth to OIDC" || len(is.Comments) != 1 || len(is.Worklogs) != 1 || len(is.Attachments) != 1 {
		t.Fatalf("%+v", is)
	}
}

func TestCheckWritesNothing(t *testing.T) {
	srv, s := editSite(t)
	req := wholeFile(t, s, srv, "Core")
	setText(req.Local.Root, "status", "Shipped")
	add(req.Local.Root, adfEl("comment", "second", "public", "true"))
	req.Actions = append(req.Actions, adapter.Action{Verb: "create", Target: "comment[2]"})
	srv.Requests = nil
	out := s.Check(bg, req)
	if writes(srv) != 0 {
		t.Fatalf("Check wrote: %v", srv.Requests)
	}
	if out[1].Err == nil || !strings.Contains(out[1].Err.Error(), `"Shipped" is not reachable`) || out[0].Err != nil || out[2].Err != nil ||
		out[6].Err == nil || !strings.Contains(out[6].Err.Error(), "GEN is not one") {
		t.Fatalf("%+v", out)
	}
	req = wholeFile(t, s, srv, "Core")
	if out := s.Check(bg, req); out[1].Detail != "transition To Do -> Done (Done)" || out[1].Err != nil {
		t.Fatalf("%+v", out[1])
	}
}

func TestCheckCreateAndDelete(t *testing.T) {
	srv, s := editSite(t)
	srv.Requests = nil
	out := s.Check(bg, newFile(t, "gen/New.xml", `<issue><type>Task</type><summary>N</summary><link type="nope">GEN-1</link></issue>`))
	if out[0].Err == nil || !strings.Contains(out[0].Err.Error(), `unknown link type "nope"`) || writes(srv) != 0 {
		t.Fatalf("%v %v", out[0].Err, srv.Requests)
	}
	base, _ := s.Fetch(bg, srv.ID("GEN-1"))
	out = s.Check(bg, adapter.ApplyRequest{Base: base, Actions: []adapter.Action{{Verb: "delete"}}})
	if out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "sub-tasks first") {
		t.Fatal(out[0].Err)
	}
	out = s.Apply(bg, adapter.ApplyRequest{Base: base, Actions: []adapter.Action{{Verb: "delete"}}})
	if out[0].Err == nil || srv.Issue(srv.ID("GEN-1")) == nil {
		t.Fatal("refused delete must not delete")
	}
}

func TestGeneratedFilesRefused(t *testing.T) {
	_, s := editSite(t)
	p, _ := s.Fetch(bg, peopleID)
	req := adapter.ApplyRequest{Local: p, Base: p, Actions: []adapter.Action{{Verb: "update", Group: "person"}}}
	for _, out := range [][]adapter.Result{s.Check(bg, req), s.Apply(bg, req)} {
		if out[0].Err == nil || out[0].Err.Error() != ".people.xml is generated by gfs and read-only; changes to it are never sent" {
			t.Fatal(out[0].Err)
		}
	}
}

func TestDescribe(t *testing.T) {
	root := parse(t, `<issue><type>Bug</type><status>Closed</status>
  <comment public="true">a</comment><comment internal="true">b</comment><comment>c</comment>
  <worklog><started>s</started><spent>2h</spent></worklog><link type="blocks">GEN-9</link></issue>`)
	local := &adapter.Resource{Path: "gen/New.xml", Root: root}
	cases := []struct {
		a             adapter.Action
		class, detail string
	}{
		{adapter.Action{Verb: "create"}, "create", "create Bug in GEN"},
		{adapter.Action{Verb: "delete"}, "delete", "delete issue"},
		{adapter.Action{Verb: "update", Group: "status"}, "transition", "transition to Closed"},
		{adapter.Action{Verb: "update", Group: "field"}, "update", "update custom fields"},
		{adapter.Action{Verb: "update", Group: "priority"}, "update", "update priority"},
		{adapter.Action{Verb: "create", Target: "comment[1]"}, "reply", "add public reply (emails the customer)"},
		{adapter.Action{Verb: "create", Target: "comment[2]"}, "comment", "add internal comment"},
		{adapter.Action{Verb: "create", Target: "comment[3]"}, "comment", "add comment"},
		{adapter.Action{Verb: "update", Target: "comment[id=5]"}, "comment", "edit comment 5"},
		{adapter.Action{Verb: "delete", Target: "comment[id=5]"}, "delete", "delete comment 5"},
		{adapter.Action{Verb: "create", Target: "worklog[1]"}, "worklog", "log 2h"},
		{adapter.Action{Verb: "create", Target: "link[1]"}, "link", "link: blocks GEN-9"},
		{adapter.Action{Verb: "delete", Target: "link[id=7]"}, "delete", "delete link 7"},
		{adapter.Action{Verb: "create", Target: "attachment[file=a.png]", File: "gen/New.files/a.png"}, "create", "attach a.png"},
	}
	for _, c := range cases {
		a := c.a
		(&Adapter{}).Describe(&a, local)
		if a.Class != c.class || a.Detail != c.detail {
			t.Errorf("%+v: %s %q, want %s %q", c.a, a.Class, a.Detail, c.class, c.detail)
		}
	}
}

func TestAvailable(t *testing.T) {
	srv, now := site(t)
	supportFlow(srv)
	srv.SetFields("SUP", "3",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution",
			Options: []jtest.Option{{ID: "1", Value: "Done"}, {ID: "2", Value: "Won't Do"}}},
		jtest.Field{ID: "customfield_10040", Name: "Users", Type: "string", Custom: typeText})
	s := open(t, srv, now, selection{keys: []string{"SUP"}}, "")
	id := srv.ID("SUP-1")
	local, _ := s.Fetch(bg, id)
	setText(local.Root, "status", "Closed")
	adv, err := s.Available(bg, id, local)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "status: In Progress" || len(adv.Items) != 2 || adv.Items[0].Name != "Resolve this issue" {
		t.Fatalf("%+v", adv)
	}
	f := adv.Items[0].Fields
	if len(f) != 2 || f[0].Element != "resolution" || !f[0].Required || strings.Join(f[0].Allowed, "|") != "Done|Won't Do" ||
		f[1].Element != `field id="customfield_10040"` || f[1].Required {
		t.Fatalf("%+v", f)
	}
	if !strings.HasPrefix(adv.Note, `several transitions lead to "Closed"`) {
		t.Fatal(adv.Note)
	}
}
```

- [ ] **Step 16.5: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'Apply|Check|Generated|Describe|Available' -v`
Expected: FAIL, `s.Apply undefined`.

- [ ] **Step 16.6: Implement `apply.go`**

```go
package jira

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
)

func results(acts []adapter.Action) []adapter.Result {
	out := make([]adapter.Result, len(acts))
	for i, a := range acts {
		out[i].Action = a
	}
	return out
}

func failAll(out []adapter.Result, err error) []adapter.Result {
	for i := range out {
		out[i].Err, out[i].Code = err, atlassian.Code(err)
	}
	return out
}

// resourceVerb is the verb of the file's own create or delete action, if any.
func resourceVerb(acts []adapter.Action) (int, string) {
	for i, a := range acts {
		if a.Target == "" && !a.IsAttachment() && (a.Verb == "create" || a.Verb == "delete") {
			return i, a.Verb
		}
	}
	return -1, ""
}

// generated refuses changes to .people.xml and .workflows.xml.
func generated(req adapter.ApplyRequest) error {
	for _, r := range []*adapter.Resource{req.Local, req.Base} {
		if r != nil && (r.ID == peopleID || r.ID == workflowsID) {
			return fmt.Errorf("%s is generated by gfs and read-only; changes to it are never sent", r.Path)
		}
	}
	return nil
}

// Apply runs one file's actions (jira spec §7.2): a create or delete alone,
// else the field edits and transition, then comments, worklogs and links in
// the engine's order, then attachments.
func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		return s.create(ctx, req)
	case "delete":
		err := s.deleteIssue(ctx, req.Base.ID)
		out[i].Err, out[i].Code = err, atlassian.Code(err)
		return out
	}
	ic, err := s.issueCtx(ctx, req.Local)
	if err != nil {
		return failAll(out, err)
	}
	plan, err := s.planEdit(ctx, ic, req)
	if err != nil {
		return failAll(out, err)
	}
	s.applyEdit(ctx, ic, plan, out)
	for i, a := range req.Actions {
		if a.Target != "" && !a.IsAttachment() {
			err := s.sub(ctx, ic, req, a)
			out[i].Err, out[i].Code = err, atlassian.Code(err)
		}
	}
	for i, a := range req.Actions {
		if a.IsAttachment() {
			out[i].ID, out[i].Err = s.attachment(ctx, ic.id, req, a)
			out[i].Code = atlassian.Code(out[i].Err)
			if out[i].Err == nil && a.Verb == "create" {
				out[i].Version = "-"
			}
		}
	}
	return out
}

// Check resolves one file's actions without writing: people, options,
// transitions, required fields, visibility and link types (jira spec §7.8).
func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		out[i].Err = s.checkCreate(ctx, req)
		return out
	case "delete":
		out[i].Err = s.checkDelete(ctx, req.Base.ID)
		return out
	}
	ic, err := s.issueCtx(ctx, req.Local)
	if err != nil {
		return failAll(out, err)
	}
	plan, err := s.planEdit(ctx, ic, req)
	if err != nil {
		return failAll(out, err)
	}
	for i, e := range plan.errs {
		out[i].Err = e
	}
	if plan.tr != nil && plan.errs[plan.status] == nil {
		out[plan.status].Detail = transitionDetail(plan)
	}
	for i, a := range req.Actions {
		if a.Target != "" && !a.IsAttachment() {
			_, out[i].Err = s.planSub(ctx, ic, req, a)
		}
	}
	return out
}

// transitionDetail is how a dry run shows the chosen transition:
// "transition In Progress -> Closed (Resolve this issue) with <resolution>".
func transitionDetail(plan *editPlan) string {
	d := fmt.Sprintf("transition %s -> %s (%s)", plan.from, plan.tr.To.Name, plan.tr.Name)
	var with []string
	for id := range plan.trFields {
		with = append(with, elemName(id))
	}
	sort.Strings(with)
	if len(with) > 0 {
		d += " with " + strings.Join(with, ", ")
	}
	return d
}

// checkCreate is the dry run of a new file: the create request and the
// comments, worklogs and links that would follow it.
func (s *session) checkCreate(ctx context.Context, req adapter.ApplyRequest) error {
	plan, err := s.planCreate(ctx, req)
	if err != nil {
		return err
	}
	ic := &issueCtx{id: "new", key: plan.ic.p.Key + "-new", p: plan.ic.p, t: plan.ic.t, set: plan.ic.set}
	var errs []error
	for _, name := range []string{"comment", "worklog", "link"} {
		for n := 1; nthNew(req.Local.Root, name, "id", n) != nil; n++ {
			if _, err := s.planSub(ctx, ic, req, adapter.Action{Verb: "create", Target: fmt.Sprintf("%s[%d]", name, n)}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 16.7: Implement `advise.go`**

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// elemTag is a field's element as gfs actions prints it inside <…>.
func elemTag(id string) string {
	if e, ok := systemElems[id]; ok {
		return e
	}
	return fmt.Sprintf("field id=%q", id)
}

// allowedNames reads a screen field's allowed values: their names or values.
func allowedNames(raw []json.RawMessage) []string {
	var out []string
	for _, r := range raw {
		var v struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		json.Unmarshal(r, &v)
		switch {
		case v.Name != "":
			out = append(out, v.Name)
		case v.Value != "":
			out = append(out, v.Value)
		}
	}
	return out
}

// Available lists the transitions the user can take on the issue now, with
// their screen fields, and what the file's own <status> would resolve to
// (jira spec §8).
func (s *session) Available(ctx context.Context, id string, local *adapter.Resource) (adapter.Advice, error) {
	if id == peopleID || id == workflowsID {
		return adapter.Advice{State: "generated by gfs, read-only"}, nil
	}
	var cur struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(id)+"?fields=status", nil, &cur); err != nil {
		return adapter.Advice{}, err
	}
	ts, err := s.transitions(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	status := cur.Fields.Status.Name
	adv := adapter.Advice{State: "status: " + status}
	for _, t := range ts {
		it := adapter.Available{Verb: "transition", Name: t.Name, To: t.To.Name}
		ids := make([]string, 0, len(t.Fields))
		for fid := range t.Fields {
			if fid != "comment" {
				ids = append(ids, fid)
			}
		}
		sort.Slice(ids, func(i, j int) bool { // required first, then by element
			if t.Fields[ids[i]].Required != t.Fields[ids[j]].Required {
				return t.Fields[ids[i]].Required
			}
			return elemTag(ids[i]) < elemTag(ids[j])
		})
		for _, fid := range ids {
			f := t.Fields[fid]
			it.Fields = append(it.Fields, adapter.AvailableField{Element: elemTag(fid), Required: f.Required, Allowed: allowedNames(f.AllowedValues)})
		}
		adv.Items = append(adv.Items, it)
	}
	if local != nil {
		if want := textOf(child(local.Root, "status")); want != "" && !strings.EqualFold(want, status) {
			if tr, err := chooseTransition(ts, want); err != nil {
				adv.Note = err.Error()
			} else {
				adv.Note = fmt.Sprintf("local <status> %s -> %q", want, tr.Name)
			}
		}
	}
	return adv, nil
}
```

- [ ] **Step 16.8: Implement `adapter.go`**

```go
// Package jira mirrors Jira Cloud as XML files: every project a user sees
// (jira://), or the requests a customer raised on a service desk
// (jira+customer://).
package jira

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func init() { adapter.Register(&Adapter{}) }

// Adapter is agent mode: jira://<site>.
type Adapter struct{}

var (
	_ adapter.Normalizer = (*Adapter)(nil)
	_ adapter.Session    = (*session)(nil)
	_ adapter.Cacher     = (*session)(nil)
	_ adapter.Reporter   = (*session)(nil)
	_ adapter.Advisor    = (*session)(nil)
	_ adapter.Identified = (*session)(nil)
)

func (*Adapter) Name() string                 { return "jira" }
func (*Adapter) Schemes() []string            { return []string{"jira"} }
func (*Adapter) Schema() *schema.Schema       { return issueSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openSession(ctx, t)
}

func topDir(p string) string {
	dir, _, _ := strings.Cut(p, "/")
	return dir
}

// Describe says what an action will do and its policy class (jira spec §7.7).
func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	var root *xmltree.Node
	p := ""
	if local != nil {
		root, p = local.Root, local.Path
	}
	switch {
	case a.IsAttachment():
		switch a.Verb {
		case "create":
			a.Detail = "attach " + path.Base(a.File)
		case "delete":
			a.Detail = "delete attachment " + path.Base(a.File)
		default:
			a.Detail = a.Verb + " attachment (Jira attachments have no versions; delete and re-add)"
		}
	case a.Target != "":
		describeSub(a, root)
	case a.Verb == "create":
		typ := textOf(child(root, "type"))
		if typ == "" {
			typ = "issue"
		}
		a.Detail = fmt.Sprintf("create %s in %s", typ, strings.ToUpper(topDir(p)))
	case a.Verb == "delete":
		a.Detail = "delete issue"
	case a.Verb == "update" && a.Group == "status":
		a.Class, a.Detail = "transition", "transition to "+textOf(child(root, "status"))
	case a.Verb == "update" && a.Group == "field":
		a.Detail = "update custom fields"
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	default:
		a.Detail = a.Verb + " (not supported by jira)"
	}
}

func describeSub(a *adapter.Action, root *xmltree.Node) {
	name, id, nth, _ := changes.ParseTarget(a.Target)
	switch name + " " + a.Verb {
	case "comment create":
		c := nthNew(root, "comment", "id", nth)
		switch {
		case attrIs(c, "public", "true"):
			a.Class, a.Detail = "reply", "add public reply (emails the customer)"
		case attrIs(c, "internal", "true"):
			a.Class, a.Detail = "comment", "add internal comment"
		default:
			a.Class, a.Detail = "comment", "add comment"
		}
	case "comment update":
		a.Class, a.Detail = "comment", "edit comment "+id
	case "worklog create":
		a.Class, a.Detail = "worklog", "log "+textOf(child(nthNew(root, "worklog", "id", nth), "spent"))
	case "worklog update":
		a.Class, a.Detail = "worklog", "edit worklog "+id
	case "link create":
		l := nthNew(root, "link", "id", nth)
		typ, _ := attr(l, "type")
		a.Class, a.Detail = "link", fmt.Sprintf("link: %s %s", typ, textOf(l))
	case "link update":
		a.Detail = "edit link " + id + " (links cannot be edited; delete and add)"
	case "comment delete", "worklog delete", "link delete":
		a.Class, a.Detail = "delete", "delete "+name+" "+id
	default:
		a.Detail = a.Verb + " " + a.Target
	}
}

func attrIs(n *xmltree.Node, name, want string) bool {
	v, _ := attr(n, name)
	return v == want
}
```

- [ ] **Step 16.9: Run the adapter tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS.

- [ ] **Step 16.10: Write the failing CLI round trip**

`internal/cli/jira_e2e_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

// jiraSite is a fake Jira with one project GEN (Task) holding GEN-1.
func jiraSite(t *testing.T) *jtest.Server {
	t.Helper()
	srv := jtest.New()
	t.Cleanup(srv.Close)
	srv.AddProject(jtest.Project{Key: "GEN", ID: "10017"}, jtest.IssueType{ID: "1", Name: "Task"})
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Migrate auth", Reporter: "me", Updated: time.Now().Add(-time.Hour)})
	t.Setenv("GFS_JIRA_TOKEN", "t")
	t.Setenv("GFS_JIRA_EMAIL", "me@x.com")
	return srv
}

func TestJiraCloneEditCommit(t *testing.T) {
	srv := jiraSite(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustContain(t, mustRun(t, 0, "clone", "jira://acme.atlassian.net/GEN?base="+srv.URL), "Cloned 3 resources")
	t.Chdir(filepath.Join(dir, "gen"))
	for _, p := range []string{"gen/GEN-1 Migrate auth.xml", ".people.xml", ".workflows.xml", ".gfs/cache/jira/meta.json"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	file := "gen/GEN-1 Migrate auth.xml"
	replaceIn(t, file, "<summary>Migrate auth</summary>", "<summary>Migrate auth to OIDC</summary>")
	replaceIn(t, file, "<status>To Do</status>", "<status>In Progress</status>")
	mustContain(t, mustRun(t, 0, "status"), "update summary", "transition to In Progress")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "would run  transition To Do -> In Progress (Start)")
	mustRun(t, 0, "commit")
	if _, err := os.Stat("gen/GEN-1 Migrate auth to OIDC.xml"); err != nil {
		t.Fatalf("the file follows the summary: %v", err)
	}
	if is := srv.Issue(srv.ID("GEN-1")); is.Status != "In Progress" || is.Summary != "Migrate auth to OIDC" {
		t.Fatalf("%+v", is)
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
}
```

- [ ] **Step 16.11: Run it to make sure it fails**

Run: `go test ./internal/cli/ -run TestJiraCloneEditCommit -v`
Expected: FAIL, `no adapter for scheme "jira"`.

- [ ] **Step 16.12: Register the adapter in the CLI**

In `internal/cli/root.go`, next to the Confluence import:

```go
	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence" // registers confluence://
	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/jira"       // registers jira://
```

- [ ] **Step 16.13: Run everything**

Run: `go vet ./... && go test ./...`
Expected: PASS. `TestJiraCloneEditCommit` shows the whole path: clone writes the issue, `.people.xml`, `.workflows.xml` and the metadata cache; `status` names the actions; `commit --dry-run` prints `transition To Do -> In Progress (Start)`; commit renames the file after its new summary; `status` is clean afterwards.

- [ ] **Step 16.14: Commit**

```bash
git add internal/adapter/jira internal/engine/commit.go internal/engine/commit_test.go internal/adapter/fake/fake.go internal/cli/root.go internal/cli/jira_e2e_test.go
git commit -m "jira: Check and Apply, gfs actions per issue, jira:// registered; dry runs show Check's detail"
```

### Task 17: Customer mode, read side

A customer's requests on someone else's service desk, through `/rest/servicedeskapi` only (spec §5.10, §9.1). The fake portal is a separate server: a customer-only site answers 404 to the Jira platform API, and the fake does the same.

**Files:**
- Create: `internal/adapter/jira/customer.go`
- Create: `internal/adapter/jira/jtest/portal.go`
- Test: `internal/adapter/jira/customer_test.go`

**Interfaces:**
- Consumes: `atlassian.Client` (with header `X-ExperimentalApi: opt-in`), `registry`, `issuePath`, `rawField`, `textEl2`, `userNode`, `requestSchema`, `wikiType`, `parseTarget` (customer URLs), `plural`, `topDir`.
- Produces:
  - `type CustomerAdapter struct{}` with `Name` `jira-customer`, `Schemes` `jira+customer`, `Schema` `requestSchema`, `PathModel` `Flat`, `Verbs`, `DefaultDir`, `Normalize`, `Describe` (registration and `Open` come in Task 18, once the session has `Check`/`Apply`)
  - `type desk struct{ ID, Key, Name string }` with `dir()`
  - `type custSession struct{ c *atlassian.Client; t target; desks map[string]*desk; reg *registry; cacheDir string; report func(adapter.Progress) }`
  - `openCustomer(ctx, target)`, `(s) servicePages(ctx, path, each)`, `resolveDesks`, `sorted`, `Identity`, `SetProgress`, `UseCache`, `Close`, `List`, `Fetch`, `Download`, `getRequest(ctx, id) (apiRequest, *desk, error)`, `attachments`, `approvals`, `resource`, `requestNode`
  - types `apiRequest` (with `field(id)`, `summary()`, `stamp()`), `isoTime`, `apiCustComment`, `apiCustAttachment` (with `id()`), `apiApproval`; `const requestExpand`; `requestPath(d, r)`
  - `jtest.Portal`: `NewPortal()`, `AddDesk`, `SetFields`, `AddUser`, `AddRequest`, `Edit`, `SetStatus`, `AddComment`, `Request(id)`, `Count(method, prefix)`; types `Desk`, `RequestType`, `RequestField`, `Request`, `CustomerComment`, `Approval`, `CustomerTransition`; `var portalRegistrars`
  - test helpers `portal(t)`, `openCust(t, p, sel)` (in `customer_test.go`)

The listing rule (spec §9.1): every listing is `Full: true`. An empty cursor (clone, `pull --full`) fetches every request in full and rebuilds `.people.xml`. Otherwise open requests come in full (new comments show up), and closed ones as stubs whose version is `statusDate/<status history length>`. The engine fetches a stub only when that stamp changed. Known limit: a new comment on a closed request with no status change appears after `pull --full`.

- [ ] **Step 17.1: Write the failing tests**

`internal/adapter/jira/customer_test.go`:

```go
package jira

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// portal is a fake customer portal with two desks: ECOHELP (34) holding an
// open and a resolved request of ours, and DS (24) with one shared request.
func portal(t *testing.T) *jtest.Portal {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	p := jtest.NewPortal()
	t.Cleanup(p.Close)
	p.Clock = func() time.Time { return now }
	p.AddDesk(jtest.Desk{ID: "34", Key: "ECOHELP", Name: "Developer and Marketplace Support"}, jtest.RequestType{ID: "4180", Name: "Marketplace listing"})
	p.AddDesk(jtest.Desk{ID: "24", Key: "DS", Name: "Design System Support"}, jtest.RequestType{ID: "90", Name: "Question"})
	p.AddUser(jtest.User{Account: "a:sher", Name: "Sherica"})
	p.AddRequest(jtest.Request{ID: "164494", Key: "ECOHELP-164494", Desk: "34", Type: "4180", Summary: "App listing rejected",
		Description: "Our listing was *rejected*.", Reporter: "me", Mine: true, Fields: map[string]any{"customfield_19404": 1216382},
		Created: now.Add(-72 * time.Hour)})
	p.AddRequest(jtest.Request{ID: "159249", Key: "ECOHELP-159249", Desk: "34", Type: "4180", Summary: "Old question", Reporter: "me",
		Mine: true, Status: "Resolved", Category: "DONE", Created: now.Add(-240 * time.Hour)})
	p.AddRequest(jtest.Request{ID: "900", Key: "DS-900", Desk: "24", Type: "90", Summary: "Shared with my org", Reporter: "a:sher",
		Participants: []string{"me"}, Created: now.Add(-24 * time.Hour)})
	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "Please fix the icon.", Public: true})
	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "agents only", Public: false})
	return p
}

func openCust(t *testing.T, p *jtest.Portal, sel custSelection) *custSession {
	t.Helper()
	if sel.ownership == "" {
		sel.ownership = "all"
	}
	if sel.status == "" {
		sel.status = "all"
	}
	s, err := openCustomer(bg, target{base: p.URL, email: "me@x.com", token: "t", csel: sel})
	if err != nil {
		t.Fatal(err)
	}
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func TestCustomerListing(t *testing.T) {
	p := portal(t)
	s := openCust(t, p, custSelection{})
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := ".people.xml|ds/DS-900 Shared with my org.xml|ecohelp/ECOHELP-159249 Old question.xml|ecohelp/ECOHELP-164494 App listing rejected.xml"
	if got := strings.Join(paths(l), "|"); got != want || !l.Full {
		t.Fatalf("%s", got)
	}
	got := xmltree.Print(find(t, l, "ecohelp/ECOHELP-164494 App listing rejected.xml").Root, 0)
	for _, part := range []string{
		`<request id="164494" key="ECOHELP-164494" desk="34" type="4180"`,
		`<requestType>Marketplace listing</requestType>`,
		`<status category="indeterminate">Waiting for support</status>`,
		`<field id="customfield_19404" name="customfield_19404">1216382</field>`,
		`<description type="text/x-jira-wiki">Our listing was *rejected*.</description>`,
		`author="Sherica"`, `Please fix the icon.`,
	} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %s in\n%s", part, got)
		}
	}
	if strings.Contains(got, "agents only") {
		t.Error("a customer never sees internal comments")
	}
	if p.Count("GET", "/rest/api/") != 0 {
		t.Fatal("customer mode must not call the Jira platform API")
	}

	// a later pull: the resolved request comes as a stub, open ones in full
	p.Requests = nil
	l, err = s.List(bg, l.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	old := find(t, l, "ecohelp/ECOHELP-159249 Old question.xml")
	if old.Root != nil || p.Count("GET", "/rest/servicedeskapi/request/159249/comment") != 0 {
		t.Fatal("a closed request is a stub")
	}
	if find(t, l, "ecohelp/ECOHELP-164494 App listing rejected.xml").Root == nil {
		t.Fatal("an open request is listed in full")
	}
	stamp := old.Version
	p.SetStatus("159249", "Reopened", "INDETERMINATE")
	l, _ = s.List(bg, l.Cursor)
	if r := find(t, l, "ecohelp/ECOHELP-159249 Old question.xml"); r.Version == stamp || r.Root == nil {
		t.Fatal("a status change moves the stamp and refetches")
	}
}

func TestCustomerFilters(t *testing.T) {
	p := portal(t)
	l, _ := openCust(t, p, custSelection{ownership: "owned", status: "open"}).List(bg, "")
	if got := strings.Join(paths(l), "|"); got != ".people.xml|ecohelp/ECOHELP-164494 App listing rejected.xml" {
		t.Fatal(got)
	}
	l, _ = openCust(t, p, custSelection{desks: []string{"24"}}).List(bg, "")
	if got := strings.Join(paths(l), "|"); got != ".people.xml|ds/DS-900 Shared with my org.xml" {
		t.Fatal(got)
	}
	if _, err := openCustomer(bg, target{base: p.URL, csel: custSelection{desks: []string{"77"}}}); err == nil ||
		err.Error() != "service desk 77 not found or not visible" {
		t.Fatal(err)
	}
}

func TestCustomerFetchAndDownload(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Attachments = append(r.Attachments, &jtest.Attachment{ID: "555", Filename: "icon.png", Mime: "image/png", Data: []byte("png"), Author: "me"})
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	r, err := s.Fetch(bg, "164494")
	if err != nil || r.Root.Child("attachment") == nil {
		t.Fatalf("%v", err)
	}
	if id, _ := r.Root.Child("attachment").Attr("id"); id != "555" {
		t.Fatal(id)
	}
	var b strings.Builder
	if info, err := s.Download(bg, "164494", "555", &b); err != nil || b.String() != "png" || info.Version != "-" {
		t.Fatal(info, err)
	}
	if _, err := s.Fetch(bg, "900"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("a request on a desk outside the tree: %v", err)
	}
}
```

- [ ] **Step 17.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run Customer -v`
Expected: FAIL, `undefined: jtest.NewPortal`.

- [ ] **Step 17.3: Add the fake portal**

`internal/adapter/jira/jtest/portal.go`:

```go
package jtest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Portal is an in-memory JSM customer API (/rest/servicedeskapi) as a
// customer sees it: no Jira platform API at all.
type Portal struct {
	*httptest.Server
	Clock    func() time.Time
	Requests []string // "METHOD /path"

	mu       sync.Mutex
	desks    []Desk
	types    map[string][]RequestType  // by desk id
	fields   map[string][]RequestField // by "desk/type"
	users    map[string]*User
	requests map[string]*Request // by id
	temps    map[string]*Attachment
	seq      int
}

type Desk struct{ ID, Key, Name string }

type RequestType struct{ ID, Name string }

type RequestField struct {
	ID, Name string
	Required bool
}

type CustomerComment struct {
	ID, Author, Body string
	Public           bool
	Created          time.Time
}

type Approval struct {
	ID, Name  string
	Decision  string // "pending", "approved", "declined"
	CanAnswer bool
}

// CustomerTransition is a transition a customer may take; To is the status it leads to.
type CustomerTransition struct{ ID, Name, To, Category string }

type Request struct {
	ID, Key, Desk, Type  string
	Summary, Description string
	Status, Category     string // category: NEW, INDETERMINATE, DONE
	StatusDate           time.Time
	History              int // length of the status history
	Reporter             string
	Participants         []string
	Fields               map[string]any // other request field values
	Created              time.Time
	Comments             []*CustomerComment
	Attachments          []*Attachment
	Approvals            []*Approval
	Transitions          []CustomerTransition
	Mine                 bool // raised by the caller (ownership=owned)
}

func NewPortal() *Portal {
	p := &Portal{types: map[string][]RequestType{}, fields: map[string][]RequestField{}, users: map[string]*User{
		"me": {Account: "me", Name: "Me", Email: "me@x.com"}}, requests: map[string]*Request{}, temps: map[string]*Attachment{}, seq: 20000}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk", p.getDesks)
	mux.HandleFunc("GET /rest/servicedeskapi/request", p.listRequests)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}", p.getRequest)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/comment", p.getComments)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/attachment", p.getAttachments)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/approval", p.getApprovals)
	mux.HandleFunc("GET /secure/attachment/{aid}/{name}", p.content)
	for _, register := range portalRegistrars {
		register(p, mux)
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.Requests = append(p.Requests, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/rest/api/") {
			jiraError(w, 404, "customers cannot use the Jira platform API", nil)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return p
}

// portalRegistrars add the write endpoints (portal_write.go).
var portalRegistrars []func(*Portal, *http.ServeMux)

func (p *Portal) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func (p *Portal) nextID() string {
	p.seq++
	return strconv.Itoa(p.seq)
}

func (p *Portal) AddDesk(d Desk, types ...RequestType) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.desks = append(p.desks, d)
	p.types[d.ID] = types
}

func (p *Portal) SetFields(desk, typeID string, fs ...RequestField) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fields[desk+"/"+typeID] = fs
}

func (p *Portal) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[u.Account] = &u
}

// AddRequest stores r; empty ID, Key, times and status are filled in.
func (p *Portal) AddRequest(r Request) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addRequest(r)
}

func (p *Portal) addRequest(r Request) *Request {
	if r.ID == "" {
		r.ID = p.nextID()
	}
	if r.Key == "" {
		for _, d := range p.desks {
			if d.ID == r.Desk {
				r.Key = fmt.Sprintf("%s-%s", d.Key, r.ID)
			}
		}
	}
	if r.Created.IsZero() {
		r.Created = p.now()
	}
	if r.StatusDate.IsZero() {
		r.StatusDate = r.Created
	}
	if r.Status == "" {
		r.Status, r.Category = "Waiting for support", "INDETERMINATE"
	}
	if r.History == 0 {
		r.History = 1
	}
	if r.Fields == nil {
		r.Fields = map[string]any{}
	}
	p.requests[r.ID] = &r
	return &r
}

// Edit changes a request as the service desk would.
func (p *Portal) Edit(id string, f func(*Request)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p.requests[id])
}

// SetStatus moves a request as an agent would: a new status history entry.
func (p *Portal) SetStatus(id, status, category string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.requests[id]
	r.Status, r.Category, r.StatusDate = status, category, p.now()
	r.History++
}

// AddComment adds a comment as an agent would; the request's status does not change.
func (p *Portal) AddComment(id string, c CustomerComment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.ID == "" {
		c.ID = p.nextID()
	}
	if c.Created.IsZero() {
		c.Created = p.now()
	}
	p.requests[id].Comments = append(p.requests[id].Comments, &c)
}

func (p *Portal) Request(id string) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.requests[id]; r != nil {
		c := *r
		return &c
	}
	return nil
}

// Count is how many requests were "METHOD /path" with path starting with prefix.
func (p *Portal) Count(method, prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.Requests {
		if strings.HasPrefix(r, method+" "+prefix) {
			n++
		}
	}
	return n
}

func iso(t time.Time) map[string]any {
	return map[string]any{"iso8601": t.Format("2006-01-02T15:04:05-0700")}
}

func (p *Portal) user(account string) any {
	u := p.users[account]
	if u == nil {
		u = &User{Account: account, Name: account}
	}
	m := map[string]any{"accountId": u.Account, "displayName": u.Name, "active": true}
	if u.Email != "" {
		m["emailAddress"] = u.Email
	}
	return m
}

func (p *Portal) typeName(r *Request) string {
	for _, t := range p.types[r.Desk] {
		if t.ID == r.Type {
			return t.Name
		}
	}
	return ""
}

func (p *Portal) requestJSON(r *Request) map[string]any {
	vals := []any{
		map[string]any{"fieldId": "summary", "label": "Summary", "value": r.Summary},
		map[string]any{"fieldId": "description", "label": "Description", "value": r.Description},
	}
	ids := make([]string, 0, len(r.Fields))
	for id := range r.Fields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		label := id
		for _, f := range p.fields[r.Desk+"/"+r.Type] {
			if f.ID == id {
				label = f.Name
			}
		}
		vals = append(vals, map[string]any{"fieldId": id, "label": label, "value": r.Fields[id]})
	}
	var parts []any
	for _, a := range r.Participants {
		parts = append(parts, p.user(a))
	}
	history := make([]any, r.History)
	for i := range history {
		history[i] = map[string]any{"status": r.Status}
	}
	return map[string]any{"issueId": r.ID, "issueKey": r.Key, "requestTypeId": r.Type, "serviceDeskId": r.Desk,
		"createdDate": iso(r.Created), "reporter": p.user(r.Reporter), "requestFieldValues": vals,
		"currentStatus": map[string]any{"status": r.Status, "statusCategory": r.Category, "statusDate": iso(r.StatusDate)},
		"requestType":   map[string]any{"id": r.Type, "name": p.typeName(r)},
		"participant":   map[string]any{"values": parts}, "status": map[string]any{"values": history}}
}

func startLimit(r *http.Request, def int) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = def
	}
	return start, limit
}

func pageOf(items []any, start, limit int) map[string]any {
	w := window(items, start, limit)
	if w == nil {
		w = []any{}
	}
	return map[string]any{"values": w, "start": start, "limit": limit, "size": len(w), "isLastPage": start+limit >= len(items)}
}

func (p *Portal) getDesks(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, d := range p.desks {
		out = append(out, map[string]any{"id": d.ID, "projectKey": d.Key, "projectName": d.Name})
	}
	start, limit := startLimit(r, 50)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	var rs []*Request
	for _, x := range p.requests {
		switch {
		case q.Get("serviceDeskId") != "" && x.Desk != q.Get("serviceDeskId"):
		case q.Get("requestOwnership") == "OWNED_REQUESTS" && !x.Mine:
		case q.Get("requestStatus") == "OPEN_REQUESTS" && x.Category == "DONE":
		default:
			rs = append(rs, x)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Created.After(rs[j].Created) })
	var out []any
	for _, x := range rs {
		out = append(out, p.requestJSON(x))
	}
	start, limit := startLimit(r, 50)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) find(w http.ResponseWriter, id string) *Request {
	if x := p.requests[id]; x != nil {
		return x
	}
	for _, x := range p.requests {
		if x.Key == id {
			return x
		}
	}
	jiraError(w, 404, "request not found", nil)
	return nil
}

func (p *Portal) getRequest(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if x := p.find(w, r.PathValue("id")); x != nil {
		writeJSON(w, p.requestJSON(x))
	}
}

func (p *Portal) getComments(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, c := range x.Comments {
		if c.Public {
			out = append(out, map[string]any{"id": c.ID, "body": c.Body, "public": true, "author": p.user(c.Author), "created": iso(c.Created)})
		}
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) attachmentJSON(a *Attachment) map[string]any {
	return map[string]any{"filename": a.Filename, "author": p.user(a.Author), "created": iso(a.Created), "size": len(a.Data),
		"mimeType": a.Mime, "_links": map[string]any{"jiraRest": p.URL + "/rest/api/2/attachment/" + a.ID,
			"content": p.URL + "/secure/attachment/" + a.ID + "/" + a.Filename}}
}

func (p *Portal) getAttachments(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, a := range x.Attachments {
		out = append(out, p.attachmentJSON(a))
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) getApprovals(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, a := range x.Approvals {
		out = append(out, map[string]any{"id": a.ID, "name": a.Name, "finalDecision": a.Decision, "canAnswerApproval": a.CanAnswer})
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) content(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.requests {
		for _, a := range x.Attachments {
			if a.ID == r.PathValue("aid") {
				io.WriteString(w, string(a.Data))
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}
```

- [ ] **Step 17.4: Implement `customer.go`**

```go
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// CustomerAdapter is customer mode: jira+customer://<site> (jira spec §9).
// It is registered, with Open, in customer_apply.go.
type CustomerAdapter struct{}

var (
	_ adapter.Normalizer = (*CustomerAdapter)(nil)
	_ adapter.Cacher     = (*custSession)(nil)
	_ adapter.Reporter   = (*custSession)(nil)
	_ adapter.Identified = (*custSession)(nil)
)

func (*CustomerAdapter) Name() string                 { return "jira-customer" }
func (*CustomerAdapter) Schemes() []string            { return []string{"jira+customer"} }
func (*CustomerAdapter) Schema() *schema.Schema       { return requestSchema }
func (*CustomerAdapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*CustomerAdapter) Verbs() []adapter.Verb        { return nil }
func (*CustomerAdapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

func (*CustomerAdapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

// Describe says what a customer-mode action will do (jira spec §9.2).
func (*CustomerAdapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	var root *xmltree.Node
	p := ""
	if local != nil {
		root, p = local.Root, local.Path
	}
	name, id, _, _ := changes.ParseTarget(a.Target)
	switch {
	case a.IsAttachment() && a.Verb == "create":
		a.Detail = "attach " + path.Base(a.File)
	case a.IsAttachment():
		a.Detail = a.Verb + " attachment (customers can only add attachments)"
	case name == "comment" && a.Verb == "create":
		a.Class, a.Detail = "reply", "add reply (emails the service desk)"
	case name == "approval" && a.Verb == "update":
		a.Class, a.Detail = "approve", "answer approval "+id
	case a.Target != "":
		a.Detail = a.Verb + " " + a.Target + " (not allowed for customers)"
	case a.Verb == "create":
		a.Detail = fmt.Sprintf("raise %q on %s", textOf(child(root, "requestType")), strings.ToUpper(topDir(p)))
	case a.Verb == "update" && a.Group == "status":
		a.Class, a.Detail = "transition", "transition: "+textOf(child(root, "status"))
	case a.Verb == "update" && a.Group == "participant":
		a.Detail = "update participants"
	default:
		a.Detail = a.Verb + " " + a.Group + " (not allowed for customers)"
	}
}

type desk struct{ ID, Key, Name string }

func (d *desk) dir() string { return strings.ToLower(d.Key) }

// custSession is the requests of one customer on one site (jira spec §9).
type custSession struct {
	c        *atlassian.Client
	t        target
	desks    map[string]*desk // by id
	reg      *registry
	cacheDir string
	report   func(adapter.Progress)
}

func openCustomer(ctx context.Context, t target) (*custSession, error) {
	s := &custSession{c: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Jira"), t: t,
		desks: map[string]*desk{}, reg: newRegistry(), report: func(adapter.Progress) {}}
	s.c.Header.Set("X-ExperimentalApi", "opt-in")
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	if err := s.resolveDesks(ctx); err != nil {
		return nil, authHint(err, t)
	}
	return s, nil
}

// servicePages calls each for every item of a paged /rest/servicedeskapi list.
func (s *custSession) servicePages(ctx context.Context, path string, each func(json.RawMessage) error) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for start := 0; ; {
		var resp struct {
			Values     []json.RawMessage `json:"values"`
			IsLastPage bool              `json:"isLastPage"`
		}
		if err := s.c.Do(ctx, http.MethodGet, fmt.Sprintf("%s%sstart=%d&limit=50", path, sep, start), nil, &resp); err != nil {
			return err
		}
		for _, v := range resp.Values {
			if err := each(v); err != nil {
				return err
			}
		}
		start += len(resp.Values)
		if resp.IsLastPage || len(resp.Values) == 0 {
			return nil
		}
	}
}

func (s *custSession) resolveDesks(ctx context.Context) error {
	want := map[string]bool{}
	for _, d := range s.t.csel.desks {
		want[d] = true
	}
	err := s.servicePages(ctx, "/rest/servicedeskapi/servicedesk", func(raw json.RawMessage) error {
		var d struct {
			ID          string `json:"id"`
			ProjectKey  string `json:"projectKey"`
			ProjectName string `json:"projectName"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if len(want) == 0 || want[d.ID] {
			s.desks[d.ID] = &desk{ID: d.ID, Key: d.ProjectKey, Name: d.ProjectName}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for _, d := range s.t.csel.desks {
		if s.desks[d] == nil {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("service desk %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *custSession) sorted() []*desk {
	out := make([]*desk, 0, len(s.desks))
	for _, d := range s.desks {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (s *custSession) Identity() string                     { return s.t.email }
func (s *custSession) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *custSession) cachePath() string                    { return filepath.Join(s.cacheDir, "people.json") }
func (s *custSession) UseCache(dir string) {
	s.cacheDir = filepath.Join(dir, "jira")
	s.reg.load(s.cachePath())
}
func (s *custSession) Close() error {
	if s.cacheDir != "" && s.reg.dirty {
		return s.reg.save(s.cachePath())
	}
	return nil
}

type isoTime struct {
	ISO8601 string `json:"iso8601"`
}

type apiRequest struct {
	IssueID            string  `json:"issueId"`
	IssueKey           string  `json:"issueKey"`
	RequestTypeID      string  `json:"requestTypeId"`
	ServiceDeskID      string  `json:"serviceDeskId"`
	CreatedDate        isoTime `json:"createdDate"`
	Reporter           apiUser `json:"reporter"`
	RequestFieldValues []struct {
		FieldID string          `json:"fieldId"`
		Label   string          `json:"label"`
		Value   json.RawMessage `json:"value"`
	} `json:"requestFieldValues"`
	CurrentStatus struct {
		Status         string  `json:"status"`
		StatusCategory string  `json:"statusCategory"`
		StatusDate     isoTime `json:"statusDate"`
	} `json:"currentStatus"`
	RequestType struct {
		Name string `json:"name"`
	} `json:"requestType"`
	Participant struct {
		Values []apiUser `json:"values"`
	} `json:"participant"`
	Status struct {
		Values []json.RawMessage `json:"values"`
	} `json:"status"`
}

func (r apiRequest) field(id string) json.RawMessage {
	for _, f := range r.RequestFieldValues {
		if f.FieldID == id {
			return f.Value
		}
	}
	return nil
}

func (r apiRequest) summary() string {
	var s string
	json.Unmarshal(r.field("summary"), &s)
	return s
}

// stamp is the listing version of a request: it moves when the status does
// (jira spec §9.1). New comments on a closed request do not move it.
func (r apiRequest) stamp() string {
	return r.CurrentStatus.StatusDate.ISO8601 + "/" + strconv.Itoa(len(r.Status.Values))
}

const requestExpand = "participant,status,requestType"

// List reports every selected request. Open requests and requests whose
// status moved come in full; closed ones as stubs, which pull fetches only
// when their stamp changed. An empty cursor (clone, pull --full) fetches all.
func (s *custSession) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	full := cursor == ""
	if full {
		s.reg.reset()
	}
	own := map[string]string{"owned": "OWNED_REQUESTS", "all": "ALL_REQUESTS"}[s.t.csel.ownership]
	status := map[string]string{"open": "OPEN_REQUESTS", "all": "ALL_REQUESTS"}[s.t.csel.status]
	l := adapter.Listing{Full: true, Cursor: "requests"}
	desks := s.sorted()
	for i, d := range desks {
		n := 0
		path := fmt.Sprintf("/rest/servicedeskapi/request?serviceDeskId=%s&requestOwnership=%s&requestStatus=%s&expand=%s",
			url.QueryEscape(d.ID), own, status, requestExpand)
		err := s.servicePages(ctx, path, func(raw json.RawMessage) error {
			var r apiRequest
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			n++
			if !full && r.CurrentStatus.StatusCategory == "DONE" {
				l.Resources = append(l.Resources, adapter.Resource{ID: r.IssueID, Version: r.stamp(), Path: requestPath(d, r)})
				return nil
			}
			res, err := s.resource(ctx, r, d)
			if err != nil {
				return err
			}
			l.Resources = append(l.Resources, *res)
			return nil
		})
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("service desk %s: %w", d.Key, err)
		}
		s.report(adapter.Progress{Phase: "pages", Done: i + 1, Total: len(desks), Item: fmt.Sprintf("%s (%s)", d.Key, plural(n, "request"))})
	}
	l.Resources = append(l.Resources, s.reg.resource())
	return l, nil
}

func requestPath(d *desk, r apiRequest) string { return issuePath(d.dir(), r.IssueKey, r.summary()) }

type apiCustComment struct {
	ID      string  `json:"id"`
	Body    string  `json:"body"`
	Public  bool    `json:"public"`
	Author  apiUser `json:"author"`
	Created isoTime `json:"created"`
}

type apiCustAttachment struct {
	Filename string  `json:"filename"`
	Author   apiUser `json:"author"`
	Created  isoTime `json:"created"`
	Size     int64   `json:"size"`
	MimeType string  `json:"mimeType"`
	Links    struct {
		JiraRest string `json:"jiraRest"`
		Content  string `json:"content"`
	} `json:"_links"`
}

// id is the attachment id, the last segment of its jiraRest link.
func (a apiCustAttachment) id() string { return path.Base(a.Links.JiraRest) }

type apiApproval struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	FinalDecision string `json:"finalDecision"`
	CanAnswer     bool   `json:"canAnswerApproval"`
}

func (s *custSession) attachments(ctx context.Context, id string) ([]apiCustAttachment, error) {
	var out []apiCustAttachment
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/attachment", func(raw json.RawMessage) error {
		var a apiCustAttachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (s *custSession) approvals(ctx context.Context, id string) ([]apiApproval, error) {
	var out []apiApproval
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/approval", func(raw json.RawMessage) error {
		var a apiApproval
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

// resource fetches a request's comments, attachments and approvals and
// renders it as <request> (jira spec §5.10).
func (s *custSession) resource(ctx context.Context, r apiRequest, d *desk) (*adapter.Resource, error) {
	var comments []apiCustComment
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(r.IssueID)+"/comment", func(raw json.RawMessage) error {
		var c apiCustComment
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		comments = append(comments, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s comments: %w", r.IssueKey, err)
	}
	atts, err := s.attachments(ctx, r.IssueID)
	if err != nil {
		return nil, fmt.Errorf("%s attachments: %w", r.IssueKey, err)
	}
	apps, err := s.approvals(ctx, r.IssueID)
	if err != nil {
		return nil, fmt.Errorf("%s approvals: %w", r.IssueKey, err)
	}
	root := s.requestNode(r, comments, atts, apps)
	return &adapter.Resource{ID: r.IssueID, Version: r.stamp(), Path: requestPath(d, r), At: r.CurrentStatus.StatusDate.ISO8601, Root: root}, nil
}

func (s *custSession) requestNode(r apiRequest, comments []apiCustComment, atts []apiCustAttachment, apps []apiApproval) *xmltree.Node {
	root := el("request", "id", r.IssueID, "key", r.IssueKey, "desk", r.ServiceDeskID, "type", r.RequestTypeID,
		"created", r.CreatedDate.ISO8601, "status-date", r.CurrentStatus.StatusDate.ISO8601)
	add := func(n *xmltree.Node) { root.Children = append(root.Children, n) }
	add(textEl("summary", r.summary()))
	add(textEl("requestType", r.RequestType.Name))
	add(textEl2("status", r.CurrentStatus.Status, "category", strings.ToLower(r.CurrentStatus.StatusCategory)))
	s.reg.see(r.Reporter)
	add(userNode("reporter", r.Reporter))
	for _, u := range r.Participant.Values {
		s.reg.see(u)
		add(userNode("participant", u))
	}
	for _, f := range r.RequestFieldValues {
		switch f.FieldID {
		case "summary", "description", "attachment":
			continue
		}
		if isEmptyJSON(f.Value) {
			continue
		}
		var str string
		var num json.Number
		switch {
		case json.Unmarshal(f.Value, &str) == nil:
			add(textEl2("field", str, "id", f.FieldID, "name", f.Label))
		case json.Unmarshal(f.Value, &num) == nil:
			add(textEl2("field", num.String(), "id", f.FieldID, "name", f.Label))
		default:
			add(rawField(fieldMeta{ID: f.FieldID, Name: f.Label}, f.Value))
		}
	}
	var desc string
	json.Unmarshal(r.field("description"), &desc)
	if strings.TrimSpace(desc) != "" {
		add(textEl2("description", desc, "type", wikiType))
	}
	for _, a := range apps {
		add(el("approval", "id", a.ID, "name", a.Name, "status", a.FinalDecision))
	}
	for _, a := range atts {
		s.reg.see(a.Author)
		add(el("attachment", "id", a.id(), "name", a.Filename, "size", strconv.FormatInt(a.Size, 10), "mime", a.MimeType,
			"created", a.Created.ISO8601, "author", a.Author.DisplayName))
	}
	for _, c := range comments {
		s.reg.see(c.Author)
		add(textEl2("comment", c.Body, "id", c.ID, "account", c.Author.AccountID, "author", c.Author.DisplayName, "created", c.Created.ISO8601))
	}
	return root
}

func (s *custSession) getRequest(ctx context.Context, id string) (apiRequest, *desk, error) {
	var r apiRequest
	if err := s.c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"?expand="+requestExpand, nil, &r); err != nil {
		return r, nil, err
	}
	d := s.desks[r.ServiceDeskID]
	if d == nil {
		return r, nil, fmt.Errorf("%w: %s is on a service desk outside this tree", adapter.ErrNotFound, r.IssueKey)
	}
	return r, d, nil
}

func (s *custSession) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if id == peopleID {
		r := s.reg.resource()
		return &r, nil
	}
	r, d, err := s.getRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resource(ctx, r, d)
}

// Download streams an attachment through its content link.
func (s *custSession) Download(ctx context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	atts, err := s.attachments(ctx, resID)
	if err != nil {
		return adapter.AttachmentInfo{}, err
	}
	for _, a := range atts {
		if a.id() == attID {
			link := strings.TrimPrefix(a.Links.Content, s.t.base)
			n, err := s.c.Download(ctx, link, w)
			return adapter.AttachmentInfo{Version: "-", Size: n}, err
		}
	}
	return adapter.AttachmentInfo{}, fmt.Errorf("%w: attachment %s", adapter.ErrNotFound, attID)
}
```

- [ ] **Step 17.5: Run the tests**

Run: `go vet ./internal/adapter/jira/... && go test ./internal/adapter/jira/...`
Expected: PASS.

- [ ] **Step 17.6: Commit**

```bash
git add internal/adapter/jira/customer.go internal/adapter/jira/customer_test.go internal/adapter/jira/jtest/portal.go
git commit -m "jira: customer mode lists and fetches your service desk requests"
```

### Task 18: Customer mode, write side

What a customer can do (spec §9.2, §9.3): public replies, customer transitions by name, participants by account, answering approvals, attachments, raising a request from a request type's form. Everything else is refused with a reason. `jira+customer://` is registered here.

**Files:**
- Create: `internal/adapter/jira/customer_apply.go`
- Create: `internal/adapter/jira/jtest/portal_write.go`
- Test: `internal/adapter/jira/customer_apply_test.go`

**Interfaces:**
- Consumes: Task 17; `results`, `failAll`, `resourceVerb`, `generated` (Task 16); `nthNew`, `fieldsByID`, `validate.FindSub`.
- Produces:
  - registration of `CustomerAdapter` and its `Open`; compile-time checks that `*custSession` is an `adapter.Session` and `adapter.Advisor`
  - `(s *custSession) Check`, `Apply`, `Available`
  - `type custTransition struct{ ID, Name string }`, `(s) transitions(ctx, id)`, `transitionNames(ts)`
  - `type custStep struct{ send func(ctx) (string, error); detail string }`, `(s) plan(ctx, req, a) (custStep, error)` (no writes), `(s) post(path, body)`, `(s) attach(ctx, deskID, reqID, req, a)`
  - `type raisePlan struct{ d *desk; typeID string; values map[string]any }`, `(s) planRaise(ctx, req)`, `(s) raise(ctx, req)`
  - `accounts(root)`, `sortedKeys(m)`
  - `jtest.Portal` write endpoints (transitions, participants, comments, approvals, temporary files, attachments, request types and their fields, raising requests)
  - test helpers `custChange`, `ok`

Rules: customers cannot search users, so a new `<participant>` must carry `account` (copied from `.people.xml`). Customer transitions have names but no target status, so `<status>` is edited with a transition name, and write-back shows the status the desk chose. A comment is always public (policy class `reply`); answering an approval is class `approve`. Attachments go through `attachTemporaryFile`, which needs `X-ExperimentalApi: opt-in` (set on every customer-mode request).

- [ ] **Step 18.1: Write the failing tests**

`internal/adapter/jira/customer_apply_test.go`:

```go
package jira

import (
	"io"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// custChange fetches request id, lets edit change a copy, and returns the request.
func custChange(t *testing.T, s *custSession, id string, edit func(*xmltree.Node), acts ...adapter.Action) adapter.ApplyRequest {
	t.Helper()
	base, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	local := *base
	local.Root = base.Root.Clone()
	edit(local.Root)
	return adapter.ApplyRequest{Local: &local, Base: base, Actions: acts}
}

func ok(t *testing.T, out []adapter.Result) {
	t.Helper()
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%+v: %v", r.Action, r.Err)
		}
	}
}

func TestCustomerReplyTransitionParticipants(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Transitions = []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}}
	})
	p.AddUser(jtest.User{Account: "a:bo", Name: "Bo"})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	req := custChange(t, s, "164494", func(r *xmltree.Node) {
		add(r, textEl("comment", "Icon replaced, please re-review."))
		setText(r, "status", "Mark as resolved")
		add(r, textEl2("participant", "Bo", "account", "a:bo"))
	}, adapter.Action{Verb: "update", Group: "status"}, adapter.Action{Verb: "update", Group: "participant"},
		adapter.Action{Verb: "create", Target: "comment[1]"})
	if out := s.Check(bg, req); out[0].Err != nil || out[0].Detail != "transition Mark as resolved" || p.Count("POST", "/") != 0 {
		t.Fatalf("%+v %v", out, p.Requests)
	}
	ok(t, s.Apply(bg, req))
	r := p.Request("164494")
	if r.Status != "Resolved" || len(r.Participants) != 1 || r.Participants[0] != "a:bo" || r.Comments[len(r.Comments)-1].Body != "Icon replaced, please re-review." {
		t.Fatalf("%+v", r)
	}

	req = custChange(t, s, "164494", func(r *xmltree.Node) { add(r, textEl("participant", "bo@x.com")) },
		adapter.Action{Verb: "update", Group: "participant"})
	if out := s.Check(bg, req); out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "customers cannot look people up") {
		t.Fatal(out[0].Err)
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { remove(r, "participant") }, adapter.Action{Verb: "update", Group: "participant"})
	ok(t, s.Apply(bg, req))
	if len(p.Request("164494").Participants) != 0 {
		t.Fatal("participant not removed")
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { setText(r, "status", "Escalate") }, adapter.Action{Verb: "update", Group: "status"})
	if out := s.Check(bg, req); out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `no transition "Escalate" for you on this request; available:`) {
		t.Fatal(out[0].Err)
	}
}

func TestCustomerApprovalsAndRefusals(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Approvals = []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}}
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	answer := adapter.Action{Verb: "update", Target: "approval[id=12]"}
	req := custChange(t, s, "164494", func(r *xmltree.Node) { r.Child("approval").SetAttr("decision", "maybe") }, answer)
	if out := s.Check(bg, req); out[0].Err == nil || !strings.Contains(out[0].Err.Error(), `decision="approve" or decision="decline"`) {
		t.Fatal(out[0].Err)
	}
	req = custChange(t, s, "164494", func(r *xmltree.Node) { r.Child("approval").SetAttr("decision", "approve") }, answer)
	ok(t, s.Apply(bg, req))
	if p.Request("164494").Approvals[0].Decision != "approved" {
		t.Fatal("approval not answered")
	}
	for _, c := range []struct {
		a    adapter.Action
		want string
	}{
		{adapter.Action{Verb: "update", Group: "summary"}, "<summary> is read-only for customers"},
		{adapter.Action{Verb: "update", Target: "comment[id=1]"}, "customers cannot edit or delete comments"},
		{adapter.Action{Verb: "delete"}, "customers cannot delete requests"},
	} {
		req := custChange(t, s, "164494", func(*xmltree.Node) {}, c.a)
		if out := s.Apply(bg, req); out[0].Err == nil || out[0].Err.Error() != c.want {
			t.Errorf("%+v: %v", c.a, out[0].Err)
		}
	}
}

func TestCustomerRaiseWithAttachment(t *testing.T) {
	p := portal(t)
	p.SetFields("34", "4180", jtest.RequestField{ID: "summary", Name: "Summary", Required: true},
		jtest.RequestField{ID: "description", Name: "Description"}, jtest.RequestField{ID: "customfield_19404", Name: "Partner ID", Required: true})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	x := `<request><summary>New listing</summary><requestType>Marketplace listing</requestType>
  <description type="text/x-jira-wiki">Please review.</description><comment>More soon.</comment></request>`
	req := adapter.ApplyRequest{Local: &adapter.Resource{Path: "ecohelp/New listing.xml", Root: parse(t, x)}, Actions: []adapter.Action{{Verb: "create"}}}
	if out := s.Check(bg, req); out[0].Err == nil || out[0].Err.Error() != `a new "Marketplace listing" request needs Partner ID` {
		t.Fatal(out[0].Err)
	}
	req.Local.Root = parse(t, strings.Replace(x, "<comment>", `<field id="customfield_19404">1216382</field><comment>`, 1))
	req.Actions = append(req.Actions, adapter.Action{Verb: "create", Target: adapter.NewAttachmentTarget("attachment", "shot.png"), File: "ecohelp/New listing.files/shot.png"})
	req.Open = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("png")), nil }
	out := s.Apply(bg, req)
	ok(t, out)
	r := p.Request(out[0].ID)
	if r == nil || r.Summary != "New listing" || r.Fields["customfield_19404"] != "1216382" || len(r.Comments) != 1 || len(r.Attachments) != 1 || out[1].ID == "" {
		t.Fatalf("%+v %+v", out, r)
	}
	bad := adapter.ApplyRequest{Local: &adapter.Resource{Path: "ecohelp/X.xml", Root: parse(t, `<request><summary>x</summary><requestType>Nope</requestType></request>`)},
		Actions: []adapter.Action{{Verb: "create"}}}
	if out := s.Check(bg, bad); out[0].Err == nil || out[0].Err.Error() != `<requestType> "Nope" is not offered by ECOHELP; request types: Marketplace listing` {
		t.Fatal(out[0].Err)
	}
}

func TestCustomerAvailable(t *testing.T) {
	p := portal(t)
	p.Edit("164494", func(r *jtest.Request) {
		r.Transitions = []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}}
		r.Approvals = []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}, {ID: "13", Name: "Finance", Decision: "approved"}}
	})
	s := openCust(t, p, custSelection{desks: []string{"34"}})
	local, _ := s.Fetch(bg, "164494")
	setText(local.Root, "status", "mark as resolved")
	adv, err := s.Available(bg, "164494", local)
	if err != nil || adv.State != "status: Waiting for support" || len(adv.Items) != 2 || adv.Items[1].Name != "Legal" ||
		adv.Note != `local <status> mark as resolved -> "Mark as resolved"` {
		t.Fatalf("%+v %v", adv, err)
	}
}

func TestCustomerDescribe(t *testing.T) {
	local := &adapter.Resource{Path: "ecohelp/New.xml", Root: parse(t, `<request><requestType>Bug</requestType><status>Mark as resolved</status></request>`)}
	for _, c := range []struct {
		a             adapter.Action
		class, detail string
	}{
		{adapter.Action{Verb: "create"}, "create", `raise "Bug" on ECOHELP`},
		{adapter.Action{Verb: "create", Target: "comment[1]"}, "reply", "add reply (emails the service desk)"},
		{adapter.Action{Verb: "update", Target: "approval[id=12]"}, "approve", "answer approval 12"},
		{adapter.Action{Verb: "update", Group: "status"}, "transition", "transition: Mark as resolved"},
		{adapter.Action{Verb: "update", Group: "participant"}, "update", "update participants"},
		{adapter.Action{Verb: "update", Group: "summary"}, "update", "update summary (not allowed for customers)"},
	} {
		a := c.a
		(&CustomerAdapter{}).Describe(&a, local)
		if a.Class != c.class || a.Detail != c.detail {
			t.Errorf("%+v: %s %q", c.a, a.Class, a.Detail)
		}
	}
}
```

- [ ] **Step 18.2: Run them to make sure they fail**

Run: `go test ./internal/adapter/jira/ -run 'TestCustomer(Reply|Approvals|Raise|Available|Describe)' -v`
Expected: FAIL, `s.Check undefined`.

- [ ] **Step 18.3: Add the fake portal's write endpoints**

`internal/adapter/jira/jtest/portal_write.go`:

```go
package jtest

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
)

func init() { portalRegistrars = append(portalRegistrars, (*Portal).writeRoutes) }

func (p *Portal) writeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/transition", p.getTransitions)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/transition", p.doTransition)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/participant", p.participants(true))
	mux.HandleFunc("DELETE /rest/servicedeskapi/request/{id}/participant", p.participants(false))
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/comment", p.addComment)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/approval/{aid}", p.answer)
	mux.HandleFunc("POST /rest/servicedeskapi/servicedesk/{desk}/attachTemporaryFile", p.tempFile)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/attachment", p.attach)
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk/{desk}/requesttype", p.getTypes)
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk/{desk}/requesttype/{t}/field", p.getTypeFields)
	mux.HandleFunc("POST /rest/servicedeskapi/request", p.raise)
}

func (p *Portal) getTransitions(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, t := range x.Transitions {
		out = append(out, map[string]any{"id": t.ID, "name": t.Name})
	}
	writeJSON(w, pageOf(out, 0, 50))
}

func (p *Portal) doTransition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	for _, t := range x.Transitions {
		if t.ID == body.ID {
			x.Status, x.Category, x.StatusDate = t.To, t.Category, p.now()
			x.History++
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	jiraError(w, 400, "The transition is not valid for this request.", nil)
}

func (p *Portal) participants(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AccountIDs []string `json:"accountIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		defer p.mu.Unlock()
		x := p.find(w, r.PathValue("id"))
		if x == nil {
			return
		}
		for _, a := range body.AccountIDs {
			switch i := slices.Index(x.Participants, a); {
			case add && i < 0:
				x.Participants = append(x.Participants, a)
			case !add && i >= 0:
				x.Participants = slices.Delete(x.Participants, i, i+1)
			}
		}
		writeJSON(w, map[string]any{"values": []any{}})
	}
}

func (p *Portal) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body   string `json:"body"`
		Public bool   `json:"public"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	if !body.Public {
		jiraError(w, 400, "Customers can only add public comments.", nil)
		return
	}
	c := &CustomerComment{ID: p.nextID(), Author: "me", Body: body.Body, Public: true, Created: p.now()}
	x.Comments = append(x.Comments, c)
	writeJSON(w, map[string]any{"id": c.ID})
}

func (p *Portal) answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision string `json:"decision"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	for _, a := range x.Approvals {
		if a.ID == r.PathValue("aid") {
			if !a.CanAnswer || a.Decision != "pending" {
				jiraError(w, 403, "You cannot answer this approval.", nil)
				return
			}
			a.Decision = map[string]string{"approve": "approved", "decline": "declined"}[body.Decision]
			writeJSON(w, map[string]any{"id": a.ID, "finalDecision": a.Decision})
			return
		}
	}
	jiraError(w, 404, "no approval", nil)
}

func (p *Portal) tempFile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" || r.Header.Get("X-ExperimentalApi") != "opt-in" {
		jiraError(w, 403, "missing X-Atlassian-Token or X-ExperimentalApi", nil)
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	data, _ := io.ReadAll(f)
	p.mu.Lock()
	defer p.mu.Unlock()
	id := "temp-" + p.nextID()
	p.temps[id] = &Attachment{Filename: h.Filename, Mime: h.Header.Get("Content-Type"), Data: data}
	writeJSON(w, map[string]any{"temporaryAttachments": []any{map[string]any{"temporaryAttachmentId": id, "fileName": h.Filename}}})
}

func (p *Portal) attach(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string `json:"temporaryAttachmentIds"`
		Public bool     `json:"public"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, id := range body.IDs {
		t := p.temps[id]
		if t == nil {
			jiraError(w, 400, "unknown temporary attachment "+id, nil)
			return
		}
		a := &Attachment{ID: p.nextID(), Filename: t.Filename, Mime: t.Mime, Data: t.Data, Author: "me", Created: p.now()}
		x.Attachments = append(x.Attachments, a)
		out = append(out, p.attachmentJSON(a))
	}
	writeJSON(w, map[string]any{"attachments": map[string]any{"values": out}})
}

func (p *Portal) getTypes(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, t := range p.types[r.PathValue("desk")] {
		out = append(out, map[string]any{"id": t.ID, "name": t.Name})
	}
	writeJSON(w, pageOf(out, 0, 50))
}

func (p *Portal) getTypeFields(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, f := range p.fields[r.PathValue("desk")+"/"+r.PathValue("t")] {
		out = append(out, map[string]any{"fieldId": f.ID, "name": f.Name, "required": f.Required})
	}
	writeJSON(w, map[string]any{"requestTypeFields": out})
}

func (p *Portal) raise(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceDeskID      string         `json:"serviceDeskId"`
		RequestTypeID      string         `json:"requestTypeId"`
		RequestFieldValues map[string]any `json:"requestFieldValues"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := map[string]string{}
	allowed := map[string]bool{}
	for _, f := range p.fields[body.ServiceDeskID+"/"+body.RequestTypeID] {
		allowed[f.ID] = true
		if _, ok := body.RequestFieldValues[f.ID]; f.Required && !ok {
			errs[f.ID] = f.Name + " is required."
		}
	}
	for id := range body.RequestFieldValues {
		if !allowed[id] {
			errs[id] = "Field '" + id + "' is not on the request type."
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	x := Request{Desk: body.ServiceDeskID, Type: body.RequestTypeID, Reporter: "me", Mine: true, Fields: map[string]any{}}
	for id, v := range body.RequestFieldValues {
		switch id {
		case "summary":
			x.Summary, _ = v.(string)
		case "description":
			x.Description, _ = v.(string)
		default:
			x.Fields[id] = v
		}
	}
	nx := p.addRequest(x)
	writeJSON(w, map[string]any{"issueId": nx.ID, "issueKey": nx.Key})
}
```

- [ ] **Step 18.4: Implement `customer_apply.go`**

```go
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func init() { adapter.Register(&CustomerAdapter{}) }

var (
	_ adapter.Session = (*custSession)(nil)
	_ adapter.Advisor = (*custSession)(nil)
)

func (*CustomerAdapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openCustomer(ctx, t)
}

// custTransition is a transition a customer may take; the API does not say
// where it leads.
type custTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *custSession) transitions(ctx context.Context, id string) ([]custTransition, error) {
	var out []custTransition
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/transition", func(raw json.RawMessage) error {
		var t custTransition
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

func transitionNames(ts []custTransition) string {
	var names []string
	for _, t := range ts {
		names = append(names, fmt.Sprintf("%q", t.Name))
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// custStep is one planned request; send is nil when nothing needs sending.
type custStep struct {
	send   func(ctx context.Context) (id string, err error)
	detail string
}

func (s *custSession) post(path string, body any) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return "", s.c.Do(ctx, http.MethodPost, path, body, nil) }
}

func accounts(root *xmltree.Node) map[string]bool {
	out := map[string]bool{}
	if root == nil {
		return out
	}
	for _, p := range root.ChildrenNamed("participant") {
		if acc, _ := p.Attr("account"); acc != "" {
			out[acc] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// plan checks one action on an existing request and prepares its request
// (jira spec §9.2). It writes nothing.
func (s *custSession) plan(ctx context.Context, req adapter.ApplyRequest, a adapter.Action) (custStep, error) {
	id := req.Base.ID
	rp := "/rest/servicedeskapi/request/" + url.PathEscape(id)
	local, base := req.Local.Root, req.Base.Root
	name, sid, nth, _ := changes.ParseTarget(a.Target)
	switch {
	case a.IsAttachment() && a.Verb == "create":
		deskID, _ := base.Attr("desk")
		return custStep{send: func(ctx context.Context) (string, error) { return s.attach(ctx, deskID, id, req, a) }}, nil
	case a.IsAttachment():
		return custStep{}, errors.New("customers can only add attachments")
	case a.Target == "" && a.Verb == "update" && a.Group == "status":
		want := textOf(child(local, "status"))
		ts, err := s.transitions(ctx, id)
		if err != nil {
			return custStep{}, err
		}
		for _, t := range ts {
			if strings.EqualFold(t.Name, want) {
				return custStep{send: s.post(rp+"/transition", map[string]any{"id": t.ID}), detail: "transition " + t.Name}, nil
			}
		}
		return custStep{}, fmt.Errorf("no transition %q for you on this request; available: %s (write a transition name in <status>)", want, transitionNames(ts))
	case a.Target == "" && a.Verb == "update" && a.Group == "participant":
		for _, p := range local.ChildrenNamed("participant") {
			if acc, _ := p.Attr("account"); acc == "" {
				return custStep{}, fmt.Errorf(`<participant>%s</participant>: write account="…" from .people.xml; customers cannot look people up`, textOf(p))
			}
		}
		have, want := accounts(base), accounts(local)
		var add, drop []string
		for acc := range want {
			if !have[acc] {
				add = append(add, acc)
			}
		}
		for acc := range have {
			if !want[acc] {
				drop = append(drop, acc)
			}
		}
		sort.Strings(add)
		sort.Strings(drop)
		return custStep{send: func(ctx context.Context) (string, error) {
			if len(add) > 0 {
				if err := s.c.Do(ctx, http.MethodPost, rp+"/participant", map[string]any{"accountIds": add}, nil); err != nil {
					return "", err
				}
			}
			if len(drop) > 0 {
				return "", s.c.Do(ctx, http.MethodDelete, rp+"/participant", map[string]any{"accountIds": drop}, nil)
			}
			return "", nil
		}}, nil
	case a.Target == "":
		return custStep{}, fmt.Errorf("<%s> is read-only for customers", a.Group)
	case name == "comment" && a.Verb == "create":
		c := nthNew(local, "comment", "id", nth)
		if textOf(c) == "" {
			return custStep{}, errors.New("<comment> is empty")
		}
		return custStep{send: s.post(rp+"/comment", map[string]any{"body": textOf(c), "public": true})}, nil
	case name == "comment":
		return custStep{}, errors.New("customers cannot edit or delete comments")
	case name == "approval" && a.Verb == "update":
		ap, bp := validate.FindSub(local, "approval", "id", sid), validate.FindSub(base, "approval", "id", sid)
		decision, _ := attr(ap, "decision")
		if decision != "approve" && decision != "decline" {
			return custStep{}, fmt.Errorf(`approval %s: write decision="approve" or decision="decline"`, sid)
		}
		if st, _ := attr(bp, "status"); st != "pending" {
			return custStep{}, fmt.Errorf("approval %s is %s, not pending", sid, st)
		}
		return custStep{send: s.post(rp+"/approval/"+url.PathEscape(sid), map[string]any{"decision": decision})}, nil
	}
	return custStep{}, fmt.Errorf("customers cannot %s %s", a.Verb, a.Target)
}

// attach uploads a temporary file to the desk, then attaches it publicly.
func (s *custSession) attach(ctx context.Context, deskID, reqID string, req adapter.ApplyRequest, a adapter.Action) (string, error) {
	if req.Open == nil {
		return "", errors.New("jira: no file reader for uploads")
	}
	f, err := req.Open(a.File)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var tmp struct {
		Temporary []struct {
			ID string `json:"temporaryAttachmentId"`
		} `json:"temporaryAttachments"`
	}
	if err := s.c.Upload(ctx, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(deskID)+"/attachTemporaryFile", path.Base(a.File), f, nil, &tmp); err != nil {
		return "", err
	}
	if len(tmp.Temporary) == 0 {
		return "", errors.New("jira: upload returned no temporary attachment")
	}
	var resp struct {
		Attachments struct {
			Values []apiCustAttachment `json:"values"`
		} `json:"attachments"`
	}
	body := map[string]any{"temporaryAttachmentIds": []string{tmp.Temporary[0].ID}, "public": true}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/servicedeskapi/request/"+url.PathEscape(reqID)+"/attachment", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Attachments.Values) == 0 {
		return "", errors.New("jira: attach returned no attachment")
	}
	return resp.Attachments.Values[0].id(), nil
}

// raisePlan is a new request before it is sent.
type raisePlan struct {
	d      *desk
	typeID string
	values map[string]any
}

// planRaise checks a new file against its request type's form (jira spec §9.2).
func (s *custSession) planRaise(ctx context.Context, req adapter.ApplyRequest) (*raisePlan, error) {
	root := req.Local.Root
	dir := topDir(req.Local.Path)
	var d *desk
	var dirs []string
	for _, x := range s.sorted() {
		dirs = append(dirs, x.dir())
		if x.dir() == dir {
			d = x
		}
	}
	if d == nil || !strings.Contains(req.Local.Path, "/") {
		return nil, fmt.Errorf("new requests go in a service desk folder (%s)", strings.Join(dirs, ", "))
	}
	want := textOf(child(root, "requestType"))
	var typeID string
	var names []string
	err := s.servicePages(ctx, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(d.ID)+"/requesttype", func(raw json.RawMessage) error {
		var t struct{ ID, Name string }
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		names = append(names, t.Name)
		if strings.EqualFold(t.Name, want) {
			typeID = t.ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if typeID == "" {
		return nil, fmt.Errorf("<requestType> %q is not offered by %s; request types: %s", want, d.Key, strings.Join(names, ", "))
	}
	var form struct {
		Fields []struct {
			FieldID  string `json:"fieldId"`
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"requestTypeFields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(d.ID)+"/requesttype/"+url.PathEscape(typeID)+"/field", nil, &form); err != nil {
		return nil, err
	}
	onForm := map[string]bool{}
	for _, f := range form.Fields {
		onForm[f.FieldID] = true
	}
	values := map[string]any{}
	if t := textOf(child(root, "summary")); t != "" {
		values["summary"] = t
	}
	if t := textOf(child(root, "description")); t != "" {
		values["description"] = t
	}
	for id, f := range fieldsByID(root) {
		if !onForm[id] {
			return nil, fmt.Errorf("<field id=%q> is not on the %q form of %s", id, want, d.Key)
		}
		values[id] = textOf(f)
	}
	var missing []string
	for _, f := range form.Fields {
		if _, ok := values[f.FieldID]; f.Required && !ok {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("a new %q request needs %s", want, strings.Join(missing, ", "))
	}
	return &raisePlan{d: d, typeID: typeID, values: values}, nil
}

// raise creates the request of a new file, then its comments and attachments.
func (s *custSession) raise(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	plan, err := s.planRaise(ctx, req)
	if err != nil {
		return []adapter.Result{{Action: act, Err: err}}
	}
	var resp struct {
		IssueID  string `json:"issueId"`
		IssueKey string `json:"issueKey"`
	}
	body := map[string]any{"serviceDeskId": plan.d.ID, "requestTypeId": plan.typeID, "requestFieldValues": plan.values}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/servicedeskapi/request", body, &resp); err != nil {
		return []adapter.Result{{Action: act, Err: err, Code: atlassian.Code(err)}}
	}
	out := []adapter.Result{{Action: act, ID: resp.IssueID, Detail: resp.IssueKey}}
	rp := "/rest/servicedeskapi/request/" + url.PathEscape(resp.IssueID)
	for n := 1; nthNew(req.Local.Root, "comment", "id", n) != nil; n++ {
		c := nthNew(req.Local.Root, "comment", "id", n)
		if err := s.c.Do(ctx, http.MethodPost, rp+"/comment", map[string]any{"body": textOf(c), "public": true}, nil); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "create", Target: fmt.Sprintf("comment[%d]", n), Class: "reply"}, Err: err})
		}
	}
	for _, a := range req.Actions {
		if a.IsAttachment() {
			id, err := s.attach(ctx, plan.d.ID, resp.IssueID, req, a)
			out = append(out, adapter.Result{Action: a, ID: id, Version: "-", Err: err, Code: atlassian.Code(err)})
		}
	}
	return out
}

func (s *custSession) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		_, out[i].Err = s.planRaise(ctx, req)
		return out
	case "delete":
		out[i].Err = errors.New("customers cannot delete requests")
		return out
	}
	for i, a := range req.Actions {
		step, err := s.plan(ctx, req, a)
		out[i].Err, out[i].Detail = err, step.detail
	}
	return out
}

func (s *custSession) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		return s.raise(ctx, req)
	case "delete":
		out[i].Err = errors.New("customers cannot delete requests")
		return out
	}
	for i, a := range req.Actions {
		step, err := s.plan(ctx, req, a)
		if err == nil && step.send != nil {
			out[i].ID, err = step.send(ctx)
		}
		out[i].Err, out[i].Code, out[i].Detail = err, atlassian.Code(err), step.detail
		if err == nil && a.IsAttachment() {
			out[i].Version = "-"
		}
	}
	return out
}

// Available lists the customer transitions and the approvals the user can
// answer (jira spec §9.3).
func (s *custSession) Available(ctx context.Context, id string, local *adapter.Resource) (adapter.Advice, error) {
	if id == peopleID {
		return adapter.Advice{State: "generated by gfs, read-only"}, nil
	}
	r, _, err := s.getRequest(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	ts, err := s.transitions(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	adv := adapter.Advice{State: "status: " + r.CurrentStatus.Status}
	for _, t := range ts {
		adv.Items = append(adv.Items, adapter.Available{Verb: "transition", Name: t.Name, To: "(the service desk decides the status)"})
	}
	apps, err := s.approvals(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	for _, a := range apps {
		if a.CanAnswer && a.FinalDecision == "pending" {
			adv.Items = append(adv.Items, adapter.Available{Verb: "approve", Name: a.Name,
				To: fmt.Sprintf(`<approval id=%q decision="approve"> or "decline"`, a.ID)})
		}
	}
	if local != nil {
		if want := textOf(child(local.Root, "status")); want != "" && !strings.EqualFold(want, r.CurrentStatus.Status) {
			adv.Note = fmt.Sprintf("no transition %q for you; available: %s", want, transitionNames(ts))
			for _, t := range ts {
				if strings.EqualFold(t.Name, want) {
					adv.Note = fmt.Sprintf("local <status> %s -> %q", want, t.Name)
				}
			}
		}
	}
	return adv, nil
}
```

- [ ] **Step 18.5: Run everything**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 18.6: Commit**

```bash
git add internal/adapter/jira/customer_apply.go internal/adapter/jira/customer_apply_test.go internal/adapter/jira/jtest/portal_write.go
git commit -m "jira: customer mode replies, transitions, participants, approvals, attachments and new requests"
```

### Task 19: End-to-end scenarios through the CLI

The scenarios of spec §11.3, run through `gfs` against both fakes: clone forms, what pull picks up, commit paths, conflicts, generated files, and customer mode. No production code changes are expected. If a scenario fails, fix the code in the task that owns it and add a unit test there too.

**Files:**
- Test: `internal/cli/jira_scenarios_test.go`, `internal/cli/jira_customer_e2e_test.go`

**Interfaces:**
- Consumes: `jiraSite` (Task 16), `mustRun`, `mustContain`, `replaceIn`, `exists`, `remoteURL` (existing CLI test helpers), `jtest.Server`, `jtest.Portal`.
- Produces: helpers `fullSite(t)`, `cloneSite(t, srv, url) string`, `read(t, path) string`.

The incremental cases need no clock tricks: the fakes stamp changes with the real time, and the 10-minute search overlap covers a change made right after the clone.

- [ ] **Step 19.1: Write the agent scenarios**

`internal/cli/jira_scenarios_test.go`:

```go
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

// fullSite is jiraSite plus a service project SUP whose workflow needs a
// resolution to close, link types, and a few more issues.
func fullSite(t *testing.T) *jtest.Server {
	t.Helper()
	srv := jiraSite(t)
	srv.AddProject(jtest.Project{Key: "SUP", ID: "10001", Type: "service_desk"}, jtest.IssueType{ID: "3", Name: "Support"})
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "priority", Name: "Priority", Type: "priority"},
		jtest.Field{ID: "assignee", Name: "Assignee", Type: "user"},
		jtest.Field{ID: "description", Name: "Description", Type: "string"},
		jtest.Field{ID: "parent", Name: "Parent", Type: "issuelink"})
	srv.SetFields("SUP", "3",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"},
		jtest.Field{ID: "description", Name: "Description", Type: "string"})
	srv.EditHidden = []string{"resolution"}
	srv.SetWorkflow("SUP", "3", jtest.Workflow{Name: "Support flow",
		Statuses: []jtest.Status{{ID: "10", Name: "Open", Category: "new"}, {ID: "12", Name: "Closed", Category: "done"}},
		Transitions: []jtest.Transition{
			{ID: "31", Name: "Resolve this issue", To: "Closed", From: []string{"Open"}, Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
			{ID: "41", Name: "Close as duplicate", To: "Closed", From: []string{"Open"}, Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
		}})
	srv.AddLinkType(jtest.LinkType{ID: "1", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"})
	srv.AddUser(jtest.User{Account: "u:b", Name: "Bea", Email: "bea@x.com"})
	srv.AddUser(jtest.User{Account: "qm:h", Name: "hadas@example.com", Email: "hadas@example.com", Customer: true})
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Write script", Reporter: "me", Updated: time.Now().Add(-time.Hour),
		Fields: map[string]any{"parent": map[string]any{"key": "GEN-1"}}})
	srv.AddIssue(jtest.Issue{Project: "SUP", Type: "3", Summary: "Promo code", Reporter: "qm:h", Updated: time.Now().Add(-time.Hour)})
	return srv
}

func cloneSite(t *testing.T, srv *jtest.Server, url string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", url, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	return filepath.Join(dir, "wt")
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJiraCloneForms(t *testing.T) {
	srv := fullSite(t)
	srv.AddIssue(jtest.Issue{Project: "GEN", Type: "1", Summary: "Ancient", Updated: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	base := "?base=" + srv.URL
	for _, c := range []struct {
		url  string
		want []string
		not  []string
	}{
		{"jira://acme.atlassian.net" + base, []string{"gen/GEN-1 Migrate auth.xml", "sup/SUP-1 Promo code.xml", "gen/GEN-3 Ancient.xml"}, nil},
		{"jira:https://acme.atlassian.net/browse/SUP-1" + base, []string{"sup/SUP-1 Promo code.xml"}, []string{"gen"}},
		{"jira://acme.atlassian.net" + base + "&exclude=GEN", []string{"sup/SUP-1 Promo code.xml"}, []string{"gen"}},
		{"jira://acme.atlassian.net/GEN" + base + "&since=2025-01-01", []string{"gen/GEN-1 Migrate auth.xml"}, []string{"gen/GEN-3 Ancient.xml"}},
		{"jira://acme.atlassian.net/GEN" + base + "&limit=1", []string{"gen/GEN-2 Write script.xml"}, []string{"gen/GEN-1 Migrate auth.xml"}}, // GEN-2 was updated last
	} {
		cloneSite(t, srv, c.url)
		for _, p := range c.want {
			if !exists(p) {
				t.Errorf("%s: %s missing", c.url, p)
			}
		}
		for _, p := range c.not {
			if exists(p) {
				t.Errorf("%s: %s must not exist", c.url, p)
			}
		}
	}
	mustContain(t, mustRun(t, 2, "clone", "jira://acme.atlassian.net?filter=GEN&since=soon"+strings.TrimPrefix(base, "?")), "since=soon")
}

func TestJiraPullPicksUpRemoteChanges(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	searches := srv.Count("POST", "/rest/api/3/search/jql")
	mustContain(t, mustRun(t, 0, "pull"), "Already up to date.")
	if n := srv.Count("POST", "/rest/api/3/search/jql") - searches; n != 1 {
		t.Fatalf("a pull with nothing new made %d searches", n)
	}
	gen1, sup1 := srv.ID("GEN-1"), srv.ID("SUP-1")
	srv.AddComment(gen1, jtest.Comment{Author: "u:b", Body: json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"looks good"}]}]}`)})
	for i := 0; i < 21; i++ {
		srv.AddWorklog(gen1, jtest.Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
	}
	srv.AddAttachment(gen1, jtest.Attachment{Filename: "log.txt", Mime: "text/plain", Data: []byte("log"), Author: "u:b"})
	srv.Edit(sup1, func(is *jtest.Issue) { is.Status = "Closed" })
	mustRun(t, 0, "pull")
	f := read(t, "gen/GEN-1 Migrate auth.xml")
	if !strings.Contains(f, "looks good") || strings.Count(f, "<worklog ") != 21 || !strings.Contains(f, `name="log.txt"`) {
		t.Fatalf("GEN-1 after pull:\n%s", f)
	}
	if !strings.Contains(read(t, "sup/SUP-1 Promo code.xml"), "<status>Closed</status>") {
		t.Fatal("transition not picked up")
	}
	srv.Move(srv.ID("GEN-2"), "SUP")
	mustContain(t, mustRun(t, 0, "pull"), "gen/GEN-2 Write script.xml -> sup/SUP-2 Write script.xml")
	srv.Delete(sup1)
	mustRun(t, 0, "pull")
	if !exists("sup/SUP-1 Promo code.xml") {
		t.Fatal("an incremental pull cannot see deletions")
	}
	mustContain(t, mustRun(t, 0, "pull", "--full"), "-  sup/SUP-1 Promo code.xml")
	cfg := read(t, ".gfs/config")
	os.WriteFile(".gfs/config", []byte(regexp.MustCompile(`url = (.*)`).ReplaceAllString(cfg, "url = $1&exclude=SUP")), 0o644)
	mustRun(t, 0, "pull")
	if exists("sup/SUP-2 Write script.xml") {
		t.Fatal("files of an excluded project must go")
	}
}

func TestJiraMoveWithLocalEdit(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "<summary>Migrate auth</summary>", "<summary>Migrate auth</summary>\n      <priority>High</priority>")
	srv.Move(srv.ID("GEN-1"), "SUP")
	mustRun(t, 0, "pull")
	f := read(t, "sup/SUP-2 Migrate auth.xml")
	if !strings.Contains(f, "<priority>High</priority>") || !strings.Contains(f, `key="SUP-2"`) || exists("gen/GEN-1 Migrate auth.xml") {
		t.Fatalf("the local edit must survive the move:\n%s", f)
	}
	mustContain(t, mustRun(t, 0, "status"), "update priority")
}

func TestJiraCommitScenarios(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)

	// create with a non-initial status, reassign by email
	os.WriteFile("gen/Rate limiter.xml", []byte(`<issue><summary>Rate limiter</summary><type>Task</type><status>Done</status>
  <assignee>bea@x.com</assignee><link type="blocks">GEN-1</link></issue>`), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "create Task in GEN")
	mustRun(t, 0, "commit")
	f := read(t, "gen/GEN-3 Rate limiter.xml")
	if !strings.Contains(f, "<status>Done</status>") || !strings.Contains(f, `<assignee account="u:b">Bea</assignee>`) || !strings.Contains(f, `<link id=`) {
		t.Fatalf("created file:\n%s", f)
	}

	// a transition that needs a resolution; two lead to Closed
	sup := "sup/SUP-1 Promo code.xml"
	replaceIn(t, sup, "<status>Open</status>", "<status>Closed</status>")
	mustContain(t, mustRun(t, 1, "commit", "--dry-run"), `several transitions lead to "Closed"`)
	replaceIn(t, sup, "<status>Closed</status>", "<status>Resolve this issue</status>")
	mustContain(t, mustRun(t, 1, "commit", "--dry-run"), `transition "Resolve this issue" requires <resolution>; add it`)
	replaceIn(t, sup, "<status>Resolve this issue</status>", "<status>Resolve this issue</status>\n      <resolution>Done</resolution>")
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "would run  transition Open -> Closed (Resolve this issue) with <resolution>")
	mustRun(t, 0, "commit")
	if !strings.Contains(read(t, sup), "<status>Closed</status>") {
		t.Fatal("not closed")
	}

	// comments on a service project: internal is fine, public asks
	replaceIn(t, sup, "</issue>", `  <comment internal="true"><paragraph>checked the license</paragraph></comment>
    </issue>`)
	mustRun(t, 0, "commit")
	replaceIn(t, sup, "</issue>", `  <comment public="true"><paragraph>codes attached</paragraph></comment>
    </issue>`)
	mustContain(t, mustRun(t, 1, "commit"), "--allow reply")
	mustRun(t, 0, "commit", "--allow", "reply")
	if cs := srv.Issue(srv.ID("SUP-1")).Comments; len(cs) != 2 || *cs[0].Public || !*cs[1].Public {
		t.Fatal("visibility")
	}

	// worklog, attachment, link delete, sub-tasks
	gen1 := "gen/GEN-1 Migrate auth.xml"
	replaceIn(t, gen1, "</issue>", `  <worklog><started>2026-09-29T09:00:00.000+0200</started><spent>2h</spent></worklog>
    </issue>`)
	os.MkdirAll("gen/GEN-1 Migrate auth.files", 0o755)
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("trace"), 0o644)
	mustRun(t, 0, "commit")
	is := srv.Issue(srv.ID("GEN-1"))
	if len(is.Worklogs) != 1 || len(is.Attachments) != 1 {
		t.Fatalf("%+v", is)
	}
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("changed"), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "update of attachments is not supported")
	os.WriteFile("gen/GEN-1 Migrate auth.files/trace.txt", []byte("trace"), 0o644)
	os.Remove(gen1)
	mustContain(t, mustRun(t, 1, "commit", "--allow", "delete"), "delete or re-parent its sub-tasks first (GEN-2)")
}

func TestJiraLockConflictMerged(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	replaceIn(t, "gen/GEN-1 Migrate auth.xml", "<summary>Migrate auth</summary>", "<summary>Migrate auth</summary>\n      <priority>Low</priority>")
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Migrate auth now" })
	mustContain(t, mustRun(t, 0, "commit"), "merged")
	is := srv.Issue(srv.ID("GEN-1"))
	if is.Summary != "Migrate auth now" || is.Fields["priority"].(map[string]any)["name"] != "Low" {
		t.Fatalf("%+v", is)
	}
	if !exists("gen/GEN-1 Migrate auth now.xml") {
		t.Fatal("renamed after the merged summary")
	}
}

func TestJiraActionsAndGeneratedFiles(t *testing.T) {
	srv := fullSite(t)
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	got := mustRun(t, 0, "actions", "sup/SUP-1 Promo code.xml")
	mustContain(t, got, "status: Open", "Resolve this issue", "-> Closed", "requires <resolution>")
	mustContain(t, read(t, ".workflows.xml"), "transitions need the Administer Jira permission")
	replaceIn(t, ".people.xml", ">Me</person>", ">Myself</person>")
	mustContain(t, mustRun(t, 1, "commit"), ".people.xml")
}

// A fetched attachment follows its issue when a new summary renames the file.
func TestJiraRenameMovesSidecar(t *testing.T) {
	srv := fullSite(t)
	srv.AddAttachment(srv.ID("GEN-1"), jtest.Attachment{Filename: "log.txt", Mime: "text/plain", Data: []byte("log"), Author: "me"})
	cloneSite(t, srv, "jira://acme.atlassian.net?base="+srv.URL)
	mustRun(t, 0, "get", "gen/GEN-1 Migrate auth.files/log.txt")
	srv.Edit(srv.ID("GEN-1"), func(is *jtest.Issue) { is.Summary = "Auth: OIDC/SAML" })
	mustRun(t, 0, "pull")
	if !exists("gen/GEN-1 Auth: OIDC-SAML.files/log.txt") || exists("gen/GEN-1 Migrate auth.files") {
		t.Fatal("the sidecar must move with the file")
	}
	if got := read(t, "gen/GEN-1 Auth: OIDC-SAML.files/log.txt"); got != "log" {
		t.Fatal(got)
	}
}
```

- [ ] **Step 19.2: Write the customer scenario**

`internal/cli/jira_customer_e2e_test.go`:

```go
package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira/jtest"
)

func TestJiraCustomerEndToEnd(t *testing.T) {
	p := jtest.NewPortal()
	t.Cleanup(p.Close)
	p.AddDesk(jtest.Desk{ID: "34", Key: "ECOHELP", Name: "Developer Support"}, jtest.RequestType{ID: "4180", Name: "Marketplace listing"})
	p.SetFields("34", "4180", jtest.RequestField{ID: "summary", Name: "Summary", Required: true}, jtest.RequestField{ID: "description", Name: "Description"})
	p.AddUser(jtest.User{Account: "a:sher", Name: "Sherica"})
	p.AddRequest(jtest.Request{ID: "164494", Key: "ECOHELP-164494", Desk: "34", Type: "4180", Summary: "Listing rejected", Reporter: "me", Mine: true,
		Transitions: []jtest.CustomerTransition{{ID: "7", Name: "Mark as resolved", To: "Resolved", Category: "DONE"}},
		Approvals:   []*jtest.Approval{{ID: "12", Name: "Legal", Decision: "pending", CanAnswer: true}}})
	t.Setenv("GFS_JIRA_TOKEN", "t")
	t.Setenv("GFS_JIRA_EMAIL", "me@x.com")
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "jira+customer:https://ecosystem.atlassian.net/servicedesk/customer/portal/34?base="+p.URL)
	t.Chdir(dir + "/ecosystem")
	file := "ecohelp/ECOHELP-164494 Listing rejected.xml"
	if !exists(file) || !strings.Contains(remoteURL(t, "."), "jira+customer://ecosystem.atlassian.net?base=") {
		t.Fatal("clone layout")
	}

	p.AddComment("164494", jtest.CustomerComment{Author: "a:sher", Body: "Please fix the icon.", Public: true})
	mustRun(t, 0, "pull")
	if !strings.Contains(read(t, file), "Please fix the icon.") {
		t.Fatal("an agent's comment on an open request must arrive")
	}

	replaceIn(t, file, "</request>", "  <comment>Fixed, please re-review.</comment>\n    </request>")
	mustContain(t, mustRun(t, 1, "commit"), "--allow reply")
	mustRun(t, 0, "commit", "--allow", "reply")

	replaceIn(t, file, `status="pending"/>`, `status="pending" decision="approve"/>`)
	mustContain(t, mustRun(t, 0, "actions", file), "Mark as resolved", "Legal")
	mustRun(t, 0, "commit", "--allow", "approve")
	if p.Request("164494").Approvals[0].Decision != "approved" {
		t.Fatal("approval")
	}

	replaceIn(t, file, "<status category=\"indeterminate\">Waiting for support</status>", "<status category=\"indeterminate\">Mark as resolved</status>")
	mustRun(t, 0, "commit")
	if !strings.Contains(read(t, file), `<status category="done">Resolved</status>`) {
		t.Fatalf("status after the transition:\n%s", read(t, file))
	}

	os.WriteFile("ecohelp/Second listing.xml", []byte(`<request><summary>Second listing</summary><requestType>Marketplace listing</requestType></request>`), 0o644)
	mustContain(t, mustRun(t, 0, "status"), `raise "Marketplace listing" on ECOHELP`)
	mustRun(t, 0, "commit")
	var found bool
	entries, _ := os.ReadDir("ecohelp")
	for _, e := range entries {
		found = found || strings.HasSuffix(e.Name(), " Second listing.xml") && strings.HasPrefix(e.Name(), "ECOHELP-")
	}
	if !found {
		t.Fatalf("new request not renamed: %v", entries)
	}
}
```

- [ ] **Step 19.3: Run them**

Run: `go test ./internal/cli/ -run Jira -v`
Expected: PASS. Review Focus items 1 (`TestJiraRenameMovesSidecar`) and 3 (`TestJiraMoveWithLocalEdit`) are covered here.

- [ ] **Step 19.4: Run everything and commit**

Run: `go vet ./... && go test ./...`
Expected: PASS.

```bash
git add internal/cli/jira_scenarios_test.go internal/cli/jira_customer_e2e_test.go
git commit -m "jira: end-to-end scenarios for both modes"
```

### Task 20: Documentation, help and schema

`docs/jira.md` (spec §12), `gfs help jira`, `gfs schema jira` (a RELAX NG grammar checked with `xmllint`), the `start.md` mentions, and `example/jira/abc` regenerated in the real format.

**Files:**
- Create: `internal/adapter/jira/relaxng.go`
- Create: `docs/jira.md`
- Modify: `embed.go`, `internal/cli/docs.go` (`helpTopics`, `newSchema`), `internal/cli/docs_test.go`, `start.md`, `example/README.md`
- Replace: `example/jira/abc/*` (the four old top-level `.xml` files go; `abc/` and `.people.xml` come)
- Test: `internal/adapter/jira/relaxng_test.go`

**Interfaces:**
- Consumes: `adfNodes`, `adfMarks`, `adfRaw`, `issueJSON` (test), `envelope.Bytes`.
- Produces: `func RelaxNG() string` (exported, used by `gfs schema jira`).

- [ ] **Step 20.1: Write the failing grammar test**

`internal/adapter/jira/relaxng_test.go`:

```go
package jira

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

// TestRelaxNG checks the grammar with xmllint against real decoded files.
func TestRelaxNG(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	rng := filepath.Join(dir, "jira.rng")
	os.WriteFile(rng, []byte(RelaxNG()), 0o644)
	var is apiIssue
	json.Unmarshal([]byte(issueJSON), &is)
	root, err := decoder{reg: newRegistry(), jsm: true}.issue(is, map[string]fieldMeta{
		"description": {ID: "description"}, "customfield_10040": cf("customfield_10040", "Users", sys+"select")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"issue.xml":   envelope.Bytes(envelope.New(root), issueSchema),
		"request.xml": []byte(`<request id="1" key="E-1"><summary>s</summary><status category="done">Resolved</status><participant account="a">A</participant><comment id="2">hi</comment></request>`),
		"bad.xml":     []byte(`<issue><description type="application/vnd.atlassian.adf+xml"><paragrph>typo</paragrph></description></issue>`),
	}
	for name, data := range files {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, p).CombinedOutput()
		if name == "bad.xml" {
			if err == nil {
				t.Fatalf("a misspelt ADF element must fail:\n%s", out)
			}
			continue
		}
		if err != nil || !strings.Contains(string(out), "validates") {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
}
```

- [ ] **Step 20.2: Run it to make sure it fails**

Run: `go test ./internal/adapter/jira/ -run TestRelaxNG -v`
Expected: FAIL, `undefined: RelaxNG` (or SKIP without `xmllint`; install `libxml2-utils` to run it).

- [ ] **Step 20.3: Implement `relaxng.go`**

```go
package jira

import (
	"fmt"
	"sort"
	"strings"
)

// RelaxNG is a grammar for Jira files (gfs schema jira): the <issue> and
// <request> structure, and the ADF vocabulary inside bodies. Element names
// are strict; attributes are free, since ADF attributes are open-ended.
func RelaxNG() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	anyAttrs := `<zeroOrMore><attribute><anyName/></attribute></zeroOrMore>`
	text := func(name string) string { return fmt.Sprintf(`<element name="%s">%s<text/></element>`, name, anyAttrs) }
	w(`<?xml version="1.0" encoding="UTF-8"?>`)
	w(`<!-- Generated by gfs from its ADF tables (@atlaskit/adf-schema 57.6); do not edit. -->`)
	w(`<grammar xmlns="http://relaxng.org/ns/structure/1.0">`)
	w(`  <start><choice><ref name="envelope"/><ref name="root"/></choice></start>`)
	w(`  <define name="any"><zeroOrMore><choice><attribute><anyName/></attribute><text/><element><anyName/><ref name="any"/></element></choice></zeroOrMore></define>`)
	w(`  <define name="envelope">
    <element name="gfs">
      %s
      <interleave>
        <element name="content"><ref name="root"/></element>
        <zeroOrMore><element><choice><name>errors</name><name>conflict</name></choice><ref name="any"/></element></zeroOrMore>
      </interleave>
    </element>
  </define>`, anyAttrs)
	w(`  <define name="root"><choice><ref name="issue"/><ref name="request"/><ref name="generated"/></choice></define>`)
	w(`  <define name="generated"><element><choice><name>people</name><name>workflows</name></choice><ref name="any"/></element></define>`)
	w(`  <define name="user"><element><choice><name>user</name><name>assignee</name><name>reporter</name><name>creator</name><name>participant</name></choice>%s<text/></element></define>`, anyAttrs)
	w(`  <define name="field">
    <element name="field">
      %s
      <mixed><zeroOrMore><choice>
        %s %s %s <ref name="user"/> <ref name="block"/> <ref name="inline"/>
      </choice></zeroOrMore></mixed>
    </element>
  </define>`, anyAttrs, text("option"), text("label"), text("sprint"))
	list := func(name, item string) string {
		return fmt.Sprintf(`<optional><element name="%s"><zeroOrMore>%s</zeroOrMore></element></optional>`, name, text(item))
	}
	body := func(name string) string {
		return fmt.Sprintf(`<optional><element name="%s">%s<ref name="adf"/></element></optional>`, name, anyAttrs)
	}
	w(`  <define name="issue">
    <element name="issue">
      %s
      <interleave>`, anyAttrs)
	for _, f := range []string{"summary", "type", "status", "resolution", "priority", "parent", "due"} {
		w(`        <optional>%s</optional>`, text(f))
	}
	w(`        <zeroOrMore><ref name="user"/></zeroOrMore>`)
	w(`        %s`, list("labels", "label"))
	w(`        %s`, list("components", "component"))
	w(`        %s`, list("fixVersions", "version"))
	w(`        %s`, list("affectsVersions", "version"))
	w(`        <optional><element name="timetracking">%s<interleave><optional>%s</optional><optional>%s</optional></interleave></element></optional>`, anyAttrs, text("original"), text("remaining"))
	w(`        <zeroOrMore><ref name="field"/></zeroOrMore>`)
	w(`        %s`, body("environment"))
	w(`        %s`, body("description"))
	w(`        <zeroOrMore>%s</zeroOrMore>`, text("link"))
	w(`        <zeroOrMore><element name="attachment">%s<empty/></element></zeroOrMore>`, anyAttrs)
	w(`        <zeroOrMore><element name="comment">%s<ref name="adf"/></element></zeroOrMore>`, anyAttrs)
	w(`        <zeroOrMore><element name="worklog">%s<interleave><optional>%s</optional><optional>%s</optional>%s</interleave></element></zeroOrMore>`,
		anyAttrs, text("started"), text("spent"), body("comment"))
	w(`      </interleave>
    </element>
  </define>`)
	w(`  <define name="request">
    <element name="request">
      %s
      <interleave>`, anyAttrs)
	for _, f := range []string{"summary", "requestType", "status"} {
		w(`        <optional>%s</optional>`, text(f))
	}
	w(`        <zeroOrMore><ref name="user"/></zeroOrMore>`)
	w(`        <zeroOrMore><ref name="field"/></zeroOrMore>`)
	w(`        <optional>%s</optional>`, text("description"))
	w(`        <zeroOrMore><element name="approval">%s<empty/></element></zeroOrMore>`, anyAttrs)
	w(`        <zeroOrMore><element name="attachment">%s<empty/></element></zeroOrMore>`, anyAttrs)
	w(`        <zeroOrMore>%s</zeroOrMore>`, text("comment"))
	w(`      </interleave>
    </element>
  </define>`)
	nodes := make([]string, 0, len(adfNodes))
	for n := range adfNodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	w(`  <define name="adf"><mixed><zeroOrMore><choice><ref name="block"/><ref name="inline"/></choice></zeroOrMore></mixed></define>`)
	w(`  <define name="block"><choice>`)
	for _, n := range nodes {
		w(`    <element name="%s">%s<ref name="adf"/></element>`, n, anyAttrs)
	}
	w(`    <element name="%s"><text/></element>`, adfRaw)
	w(`  </choice></define>`)
	w(`  <define name="inline"><choice>`)
	for _, m := range adfMarks {
		w(`    <element name="%s">%s<ref name="adf"/></element>`, m, anyAttrs)
	}
	w(`    <element name="text"><text/></element>`)
	w(`  </choice></define>`)
	w(`</grammar>`)
	return b.String()
}
```

Run: `go test ./internal/adapter/jira/ -run TestRelaxNG -v`
Expected: PASS: a decoded issue file validates, and a misspelled ADF element does not.

- [ ] **Step 20.4: Write `docs/jira.md`**

````markdown
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
````

- [ ] **Step 20.5: Serve it and the grammar from the binary**

`embed.go`: the comment says "start.md and the Confluence and Jira references", and the directive becomes

```go
//go:embed start.md docs/jira.md docs/confluence.md docs/confluence/storage.md docs/confluence/roundtrip.json docs/confluence/examples
```

`internal/cli/docs.go`, `helpTopics`, after the `confluence` entry:

```go
		{Use: "jira", Short: "Jira Cloud and service desk portals: URLs, files, transitions, what each change does", Long: doc("docs/jira.md")},
```

`newSchema`'s `RunE` body becomes

```go
			switch args[0] {
			case "confluence":
				fmt.Fprint(cmd.OutOrStdout(), confluence.EmbeddedVocabulary().RelaxNG())
			case "jira":
				fmt.Fprint(cmd.OutOrStdout(), jira.RelaxNG())
			default:
				return usage("unknown adapter %q: want confluence or jira", args[0])
			}
			return nil
```

with the import `github.com/KrzysztofBogdan/gitfs/internal/adapter/jira` (`gfs example` stays Confluence-only through `onlyConfluence`).

`internal/cli/docs_test.go`: the line `mustRun(t, 2, "schema", "jira")` becomes

```go
	mustContain(t, mustRun(t, 0, "schema", "jira"), "<grammar", `<element name="paragraph">`)
	mustContain(t, mustRun(t, 0, "help", "jira"), "# gfs and Jira Cloud")
	mustRun(t, 2, "schema", "slack")
```

- [ ] **Step 20.6: Mention Jira in `start.md` and the examples index**

In `start.md`:
- line 3: `(Confluence today)` → `(Confluence and Jira today)`;
- `Service details: [docs/confluence.md](docs/confluence.md).` → `Service details: [docs/confluence.md](docs/confluence.md), [docs/jira.md](docs/jira.md).`;
- in "Typical session", after the `gfs clone confluence://…` line: `# or: gfs clone jira://acme.atlassian.net          # every project; or .../GEN for one`;
- "Built-in docs" gains `` `gfs help jira` ``;
- Policy defaults: `` except `delete` (and `send`, `publish`, `reply`, `approve` for services that have them), which are `ask`. Jira `reply` is a public comment that emails a customer. ``;
- Credentials item 1: `` `GFS_CONFLUENCE_EMAIL` or `GFS_JIRA_EMAIL` (and `GFS_CONFLUENCE_TOKEN` / `GFS_JIRA_TOKEN` for the token) ``.

In `example/README.md`, the `jira/abc/` row's remote becomes `jira://instance.atlassian.net/ABC`.

- [ ] **Step 20.7: Regenerate `example/jira/abc`**

These files are real `gfs` output: a clone of a fake project `ABC`, with the pending edits the README describes. Delete the four old `.xml` files in `example/jira/abc/`, then write:

`example/jira/abc/README.md`:

````markdown
# jira: project ABC

One file per issue, named `<KEY> <summary>.xml`, inside the project folder.
Every change here is plain CRUD on elements; no envelope carries an `action`.

```shell
$ gfs clone jira://instance.atlassian.net/ABC
Cloned 5 resources from jira://instance.atlassian.net?filter=ABC into abc
$ cd abc && ls -A . abc
.:
.gfs  .people.xml  .workflows.xml  abc

abc:
'ABC-118561 Retry loop never backs off.xml'
'ABC-118562 Login page: 500 on empty password.xml'
'ABC-118563 Upgrade Go to 1.27.xml'
```

## Create an issue

Any new file in the project folder with an `<issue>` root and no `key`
(`abc/Rate limiter drops burst traffic.xml`). `<type>` and `<summary>` are
required, plus whatever the type's create screen requires.

## Comment, transition, log work: one file, three changes

`abc/ABC-118561 Retry loop never backs off.xml` has a new `<status>`, a
comment without `id`, and a worklog without `id`:

```shell
$ gfs status
  M  abc/ABC-118561 Retry loop never backs off.xml
        transition to Done
        add comment
        log 2h
  A  abc/Rate limiter drops burst traffic.xml create Bug in ABC

$ gfs commit --dry-run
create  abc/Rate limiter drops burst traffic.xml   would run  create Bug in ABC
update  abc/ABC-118561 Retry loop never backs off.xml   would run  transition In Progress -> Done (Done)
create  abc/ABC-118561 Retry loop never backs off.xml   would run  add comment
create  abc/ABC-118561 Retry loop never backs off.xml   would run  log 2h
dry run: 4 actions, 0 failed, 0 denied
```

After `gfs commit` the new file is renamed `ABC-118564 Rate limiter drops
burst traffic.xml` and every new element gets its `id`, `author` and
timestamps. `gfs actions <file>` shows the transitions you can take on an
issue now.

## Pull merges into pending work

If someone comments while your `<comment>` is still uncommitted, pull adds the
remote comment (it has an `id`) and keeps yours (it has none). Editing a
comment that changed remotely is the only conflict, marked `C`.

Details: `gfs help jira`.
````

`example/jira/abc/abc/ABC-118561 Retry loop never backs off.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <issue id="40311" key="ABC-118561" created="2026-09-02T09:30:00.000+0200" updated="2026-09-28T16:10:00.000+0200">
      <summary>Retry loop never backs off</summary>
      <type>Bug</type>
      <status>Done</status>
      <priority>High</priority>
      <assignee account="712020:alice">Alice</assignee>
      <reporter account="712020:bob">Bob</reporter>
      <creator account="712020:bob">Bob</creator>
      <labels>
        <label>backend</label>
      </labels>
      <description type="application/vnd.atlassian.adf+xml">
        <paragraph>The client retries every 10ms forever when the upstream returns 503.</paragraph>
      </description>
      <comment id="10043" account="712020:bob" author="Bob" created="2026-09-27T21:04:00.000+0200" updated="2026-09-27T21:04:00.000+0200">
        <paragraph>Seen again in prod tonight.</paragraph>
      </comment>
      <comment>
        <paragraph>Verified on staging, closing.</paragraph>
      </comment>
      <worklog>
        <started>2026-09-29T09:00:00.000+0200</started>
        <spent>2h</spent>
      </worklog>
    </issue>
  </content>
</gfs>
```

`example/jira/abc/abc/ABC-118562 Login page: 500 on empty password.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <issue id="40312" key="ABC-118562" created="2026-09-03T14:02:00.000+0200" updated="2026-09-03T14:02:00.000+0200">
      <summary>Login page: 500 on empty password</summary>
      <type>Bug</type>
      <status>To Do</status>
      <priority>Medium</priority>
      <reporter account="712020:bob">Bob</reporter>
      <creator account="712020:bob">Bob</creator>
      <description type="application/vnd.atlassian.adf+xml">
        <paragraph>Submitting the login form with an empty password returns HTTP 500 instead of a validation error.</paragraph>
      </description>
    </issue>
  </content>
</gfs>
```

`example/jira/abc/abc/ABC-118563 Upgrade Go to 1.27.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <issue id="40313" key="ABC-118563" created="2026-09-10T11:00:00.000+0200" updated="2026-09-10T11:00:00.000+0200">
      <summary>Upgrade Go to 1.27</summary>
      <type>Task</type>
      <status>To Do</status>
      <assignee account="712020:bob">Bob</assignee>
      <reporter account="712020:alice">Alice</reporter>
      <creator account="712020:alice">Alice</creator>
      <field id="customfield_10016" name="Story Points">3</field>
    </issue>
  </content>
</gfs>
```

`example/jira/abc/abc/Rate limiter drops burst traffic.xml`:

```xml
<issue>
  <summary>Rate limiter drops burst traffic</summary>
  <type>Bug</type>
  <priority>High</priority>
  <labels><label>backend</label><label>p1</label></labels>
  <description type="application/vnd.atlassian.adf+xml">
    <paragraph>Bursts above 200 rps are dropped instead of queued.</paragraph>
    <paragraph>Reproduce with <code>hey -z 10s -q 300 http://localhost:8080/api</code>.</paragraph>
  </description>
</issue>
```

`example/jira/abc/.people.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <people id="people">
      <person account="712020:alice" type="atlassian" active="true" email="alice@example.com">Alice</person>
      <person account="712020:bob" type="atlassian" active="true">Bob</person>
    </people>
  </content>
</gfs>
```

- [ ] **Step 20.8: Run everything and commit**

Run: `go vet ./... && go test ./...`
Expected: PASS, including `TestDocsCommands`.

```bash
git add docs/jira.md embed.go internal/cli/docs.go internal/cli/docs_test.go start.md example internal/adapter/jira/relaxng.go internal/adapter/jira/relaxng_test.go
git commit -m "docs: Jira reference, gfs help jira, gfs schema jira, example tree in the real format"
```

### Task 21: Real-site checks

The checks of spec §11.4 that only a real site can answer. They skip unless the environment names a site, so CI is unaffected. The read check is safe on any project. The write checks create and delete issues, so point them at a scratch project.

**Files:**
- Test: `internal/adapter/jira/realsite_test.go`

**Interfaces:**
- Consumes: `openSession`, `openCustomer`, `parseTarget`, `adfToNodes`, `nodesToADF`, `appendMerged`, `markRank`, `planSub`/`sub`, `loadLinkTypes`, `creds.System`.
- Produces: helpers `realSession`, `normalADF`, `updatedOf`, `scratchIssue`.

What each check settles:
- `TestRealADFRoundTrip` (read-only): every description and environment of up to 30 issues goes ADF → XML → ADF and compares equal after dropping null attributes and merging adjacent text. An empty document counts as no body. When this plan was written it passed on 66 real bodies from three projects.
- `TestRealWrites` (scratch project): a new comment and a new worklog each move the issue's `updated`, which incremental pull depends on. A link written as "A <outward phrase> B" reads back that way on A; if not, swap the two keys in `linkBody` (Task 13).
- `TestRealInternalComment` (scratch service project): a comment sent with `sd.public.comment` `{internal: true}` comes back internal.
- `TestRealCustomer` (read-only): a customer portal lists and fetches. When this plan was written it listed 77 requests on a real portal.

- [ ] **Step 21.1: Add the tests**

`internal/adapter/jira/realsite_test.go`:

```go
package jira

// Checks against a real Jira Cloud site (jira spec §11.4). They skip unless
// the environment names a site:
//
//	GFS_JIRA_SITE=jira://<site>/<PROJECT>   read: ADF round trip on up to 30 issues
//	GFS_JIRA_SCRATCH=<KEY>                  write: a project where tests may create and delete issues
//	GFS_JIRA_SCRATCH_JSM=<KEY>              write: a service project for the internal-comment check
//	GFS_JIRA_CUSTOMER=jira+customer://<site> customer: list and fetch
//
// plus GFS_JIRA_EMAIL and GFS_JIRA_TOKEN (or a stored token). Example:
//
//	GFS_JIRA_SITE=jira://acme.atlassian.net/GEN go test ./internal/adapter/jira -run Real -v

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func realSession(t *testing.T, raw string) *session {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// normalADF is v as gfs would give it back: null attributes dropped,
// adjacent text with the same marks merged, marks in wrapper order.
func normalADF(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if val == nil {
				continue
			}
			if k == "attrs" {
				attrs := map[string]any{}
				for ak, av := range val.(map[string]any) {
					if av != nil {
						attrs[ak] = av
					}
				}
				if len(attrs) == 0 {
					continue
				}
				val = attrs
			}
			out[k] = normalADF(val)
		}
		if ms, ok := out["marks"].([]any); ok {
			sort.SliceStable(ms, func(i, j int) bool {
				return markRank[ms[i].(map[string]any)["type"].(string)] < markRank[ms[j].(map[string]any)["type"].(string)]
			})
		}
		if c, ok := out["content"].([]any); ok {
			var merged []any
			for _, item := range c {
				merged = appendMerged(merged, item)
			}
			if len(merged) == 0 {
				delete(out, "content")
			} else {
				out["content"] = merged
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalADF(x[i])
		}
		return out
	}
	return v
}

func TestRealADFRoundTrip(t *testing.T) {
	raw := os.Getenv("GFS_JIRA_SITE")
	if raw == "" {
		t.Skip("GFS_JIRA_SITE not set")
	}
	s := realSession(t, raw+map[bool]string{true: "&", false: "?"}[strings.Contains(raw, "?")]+"limit=30")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range l.Resources {
		if r.ID == peopleID || r.ID == workflowsID {
			continue
		}
		is, err := s.getIssue(bg, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"description", "environment"} {
			if isEmptyJSON(is.Fields[f]) {
				continue
			}
			nodes, err := adfToNodes(is.Fields[f])
			if err != nil {
				t.Errorf("%s %s: %v", is.Key, f, err)
				continue
			}
			back, err := nodesToADF(nodes)
			if err != nil {
				t.Errorf("%s %s back: %v", is.Key, f, err)
				continue
			}
			var orig any
			json.Unmarshal(is.Fields[f], &orig)
			if m, ok := normalADF(orig).(map[string]any); ok && m["content"] == nil {
				orig = nil // an empty doc: gfs shows no element and never sends one
			}
			b, _ := json.Marshal(back)
			var got any
			json.Unmarshal(b, &got)
			if !reflect.DeepEqual(normalADF(orig), normalADF(got)) {
				ob, _ := json.Marshal(normalADF(orig))
				gb, _ := json.Marshal(normalADF(got))
				t.Errorf("%s %s differs\nremote %s\ngfs    %s", is.Key, f, ob, gb)
			}
			checked++
		}
	}
	t.Logf("%d bodies round-tripped", checked)
}

func updatedOf(t *testing.T, s *session, id string) string {
	t.Helper()
	var is apiIssue
	if err := s.c.Do(bg, http.MethodGet, "/rest/api/3/issue/"+id+"?fields=updated", nil, &is); err != nil {
		t.Fatal(err)
	}
	return is.str("updated")
}

// scratchIssue creates an issue in the scratch project and deletes it when the test ends.
func scratchIssue(t *testing.T, s *session, key, summary string) (id, issueKey string) {
	t.Helper()
	p := s.projects[key]
	if p == nil {
		t.Fatalf("scratch project %s not visible", key)
	}
	if err := s.loadTypes(bg, p); err != nil {
		t.Fatal(err)
	}
	var typeID string
	for _, tm := range p.Types {
		if !tm.Subtask {
			typeID = tm.ID
			break
		}
	}
	var resp struct{ ID, Key string }
	body := map[string]any{"fields": map[string]any{"project": map[string]any{"key": key}, "issuetype": map[string]any{"id": typeID}, "summary": summary}}
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue", body, &resp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.c.Do(bg, http.MethodDelete, "/rest/api/3/issue/"+resp.ID, nil, nil) })
	return resp.ID, resp.Key
}

func TestRealWrites(t *testing.T) {
	key := os.Getenv("GFS_JIRA_SCRATCH")
	site := os.Getenv("GFS_JIRA_SITE")
	if key == "" || site == "" {
		t.Skip("GFS_JIRA_SITE and GFS_JIRA_SCRATCH not set")
	}
	u, _ := url.Parse(site)
	s := realSession(t, "jira://"+u.Host+"/"+key)
	a, akey := scratchIssue(t, s, key, "gfs real-site check A")
	_, bkey := scratchIssue(t, s, key, "gfs real-site check B")
	ic := &issueCtx{id: a, key: akey, p: s.projects[key]}

	before := updatedOf(t, s, a)
	time.Sleep(1100 * time.Millisecond)
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue/"+a+"/comment", map[string]any{"body": map[string]any{"type": "doc", "version": 1,
		"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "gfs check"}}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if after := updatedOf(t, s, a); after == before {
		t.Error("adding a comment must bump the issue's updated (incremental pull relies on it)")
	}
	before = updatedOf(t, s, a)
	time.Sleep(1100 * time.Millisecond)
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue/"+a+"/worklog?adjustEstimate=auto",
		map[string]any{"started": time.Now().Format("2006-01-02T15:04:05.000-0700"), "timeSpent": "1m"}, nil); err != nil {
		t.Fatal(err)
	}
	if after := updatedOf(t, s, a); after == before {
		t.Error("adding a worklog must bump the issue's updated")
	}

	lts, err := s.loadLinkTypes(bg)
	if err != nil || len(lts) == 0 {
		t.Fatal(err)
	}
	lt := lts[0]
	req := adapter.ApplyRequest{Local: &adapter.Resource{Root: el("issue")}}
	req.Local.Root.Children = append(req.Local.Root.Children, textEl2("link", bkey, "type", lt.Outward))
	if err := s.sub(bg, ic, req, adapter.Action{Verb: "create", Target: "link[1]"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Fetch(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	l := r.Root.Child("link")
	if typ, _ := attr(l, "type"); l == nil || typ != lt.Outward || textOf(l) != bkey {
		t.Errorf("link direction: wrote \"%s %s %s\", Jira shows %q on %s; swap the keys in linkBody", akey, lt.Outward, bkey, typ+" "+textOf(l), akey)
	}
}

func TestRealInternalComment(t *testing.T) {
	key := os.Getenv("GFS_JIRA_SCRATCH_JSM")
	site := os.Getenv("GFS_JIRA_SITE")
	if key == "" || site == "" {
		t.Skip("GFS_JIRA_SITE and GFS_JIRA_SCRATCH_JSM not set")
	}
	u, _ := url.Parse(site)
	s := realSession(t, "jira://"+u.Host+"/"+key)
	id, k := scratchIssue(t, s, key, "gfs internal comment check")
	ic := &issueCtx{id: id, key: k, p: s.projects[key]}
	root := el("issue")
	c := el("comment", "internal", "true")
	c.Children = append(c.Children, textEl("paragraph", "internal gfs check"))
	root.Children = append(root.Children, c)
	if err := s.sub(bg, ic, adapter.ApplyRequest{Local: &adapter.Resource{Root: root}}, adapter.Action{Verb: "create", Target: "comment[1]"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := attr(r.Root.Child("comment"), "internal"); v != "true" {
		t.Fatalf("the comment must stay internal:\n%v", r.Root.Child("comment"))
	}
}

func TestRealCustomer(t *testing.T) {
	raw := os.Getenv("GFS_JIRA_CUSTOMER")
	if raw == "" {
		t.Skip("GFS_JIRA_CUSTOMER not set")
	}
	u, _ := url.Parse(raw)
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openCustomer(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Resources {
		if r.ID == peopleID {
			continue
		}
		if _, err := s.Fetch(bg, r.ID); err != nil {
			t.Fatalf("%s: %v", r.Path, err)
		}
		t.Logf("%s", r.Path)
		break
	}
	t.Logf("%d requests", len(l.Resources)-1)
}
```

- [ ] **Step 21.2: Run them without a site**

Run: `go test ./internal/adapter/jira/ -run Real -v`
Expected: four `SKIP` lines.

- [ ] **Step 21.3: Run the read checks against a real site**

Run (your site and project; needs a stored token or `GFS_JIRA_TOKEN`):

```bash
GFS_JIRA_EMAIL=me@example.com GFS_JIRA_SITE=jira://acme.atlassian.net/GEN go test ./internal/adapter/jira/ -run TestRealADFRoundTrip -v -count=1
GFS_JIRA_EMAIL=me@example.com GFS_JIRA_CUSTOMER='jira+customer://ecosystem.atlassian.net?ownership=owned' go test ./internal/adapter/jira/ -run TestRealCustomer -v -count=1
```

Expected: PASS. A difference in the ADF check names the issue and prints both JSONs. Fix `adf.go` or `adfspec.go` and add the case to `TestADFRoundTrip`.

- [ ] **Step 21.4: Run the write checks against scratch projects (ask the project owner first)**

```bash
GFS_JIRA_EMAIL=me@example.com GFS_JIRA_SITE=jira://acme.atlassian.net/SCRATCH GFS_JIRA_SCRATCH=SCRATCH GFS_JIRA_SCRATCH_JSM=SCRSD \
  go test ./internal/adapter/jira/ -run 'TestRealWrites|TestRealInternalComment' -v -count=1
```

Expected: PASS. Record the result in the commit message.

- [ ] **Step 21.5: Commit**

```bash
git add internal/adapter/jira/realsite_test.go
git commit -m "jira: real-site checks for ADF round trip, updated bumps, link direction, internal comments, customer portals"
```

