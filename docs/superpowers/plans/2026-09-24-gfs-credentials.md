# gfs credentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** API tokens live in the system keyring (with read-only fallback to `alogin`'s entries), each working tree is pinned to one identity, and `gfs auth set|rm|clear|list` manages gfs's own tokens.

**Architecture:** A new package `internal/creds` owns the keyring entries (service `gfs`, user `atlassian:<email>`), the identity list file, the `alogin` fallback, and the global config (`$XDG_CONFIG_HOME/gfs/config`, per-host default email). It exposes a small `creds.Lookup` interface that the Confluence adapter uses to resolve email and token after the environment, URL and `.gfs/config`. Sessions may implement `adapter.Identified`; `engine.Clone` writes that identity to `[remote] email`. `internal/cli/auth.go` adds the `auth` command group.

**Tech Stack:** Go 1.25 (`go.mod` says `go 1.25.0`), `github.com/zalando/go-keyring` v0.2.8 (new), `github.com/spf13/cobra`, `golang.org/x/term`.

**Spec:** `docs/superpowers/specs/2026-09-24-gfs-credentials-design.md` (read it before any task). It extends `docs/superpowers/specs/2026-09-23-gfs-cli-design.md`.

## Global Constraints

- Module path `github.com/KrzysztofBogdan/gitfs`; binary `gfs` from `./cmd/gfs`. This machine's system Go is older, so run every `go` command with `GOTOOLCHAIN=go1.25.0` exported.
- Run `gofmt -l .` (must print nothing) and `go vet ./...` before every commit. Struct literals of types from another package use keyed fields, or `go vet` fails.
- Tests use the standard `testing` package only. No test talks to the network; Confluence is tested against `cftest` (an `httptest.Server`).
- **No test touches the real keyring or the real home directory.** Every test that can reach `creds` calls `keyring.MockInit()` and points `HOME` and `XDG_CONFIG_HOME` (or `creds.Dirs`) at `t.TempDir()`.
- Keyring: service `gfs`, user `atlassian:<email>` with the email lower-cased, secret = the token.
- Identity list: `$XDG_CONFIG_HOME/gfs/identities` (default `~/.config/gfs/identities`), one `atlassian:<email>` per line, sorted, no duplicates, written atomically.
- Global config: `$XDG_CONFIG_HOME/gfs/config`, INI parsed by `workdir.ParseConfig`, sections `[host "<host>"]` with key `email`.
- alogin (read only, never written): `~/.alogin.json` is a JSON array of `{name, email, …}`; keyring service `alogin`, user = profile `name`, secret = JSON `{email, token, accountId}`.
- Keyring failure other than "not found": `keyring unavailable (<cause>); set GFS_CONFLUENCE_TOKEN instead`.
- Email resolution order: `GFS_CONFLUENCE_EMAIL`, URL user, `[remote] email`, `[host "H"] email`, sole identity. Token: `GFS_CONFLUENCE_TOKEN`, then the store.
- Error texts: `no identity for <host>: run gfs auth set <email> --host <host>, or put the email in the URL (confluence://me%40x.com@<host>/<SPACE>)` and `no token for <email>: run gfs auth set <email>`.
- Exit codes: 0 ok, 1 an operation failed (verify failed, nothing to remove), 2 usage (empty token, `clear` without terminal and without `--yes`).

## File structure

```
internal/workdir/config.go      + Config.Sections()
internal/workdir/workdir.go     + WriteAtomic (exported wrapper)
internal/creds/creds.go         Dirs, Store (Get/Set/Delete/Clear/Identities), identity list
internal/creds/alogin.go        read-only alogin fallback
internal/creds/global.go        Global: per-host default email
internal/creds/lookup.go        Lookup interface, System implementation
internal/creds/*_test.go
internal/adapter/adapter.go     + Identified
internal/engine/clone.go        write [remote] email
internal/engine/clone_test.go
internal/adapter/confluence/url.go       resolution via creds.Lookup
internal/adapter/confluence/adapter.go   pass creds.System{}
internal/adapter/confluence/session.go   session.Identity()
internal/adapter/confluence/client.go    VerifyToken
internal/adapter/confluence/cftest/server.go  Accounts + GET /wiki/rest/api/user/current
internal/cli/auth.go            gfs auth set|rm|clear|list
internal/cli/root.go            register newAuth()
internal/cli/auth_test.go
internal/cli/confluence_e2e_test.go      clone with stored token
Readme.md
```

---

### Task 1: Token store in the keyring, with alogin fallback

**Files:**
- Modify: `internal/workdir/config.go` (add `Sections`), `internal/workdir/workdir.go` (add `WriteAtomic`)
- Create: `internal/creds/creds.go`, `internal/creds/alogin.go`
- Test: `internal/creds/creds_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `workdir.WriteAtomic(path string, data []byte) error` (added here).
- Produces:
  - `const creds.Realm = "atlassian"`
  - `var creds.ErrNotFound error`
  - `type creds.Dirs struct{ Config, Home string }`, `func creds.DefaultDirs() creds.Dirs`
  - `type creds.Store struct{ Dirs creds.Dirs }` with
    `Get(email string) (token, source string, err error)` (source `"gfs"` or `"alogin"`),
    `Set(email, token string) error`, `Delete(email string) error`,
    `Clear() (int, error)`, `Identities() ([]creds.Identity, []string, error)` (second value: warnings)
  - `type creds.Identity struct{ Email, Source string; Missing bool }`
  - `func (c *workdir.Config) Sections() []string` (sorted)

- [ ] **Step 1: Add the dependency and the workdir helpers**

```bash
export GOTOOLCHAIN=go1.25.0
go get github.com/zalando/go-keyring@v0.2.8
```

Append to `internal/workdir/config.go`:

```go
// Sections returns the section names in sorted order.
func (c *Config) Sections() []string {
	names := make([]string, 0, len(c.sections))
	for n := range c.sections {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

Add to `internal/workdir/workdir.go`, right above `writeAtomic`:

```go
// WriteAtomic writes data to path through a temp file and a rename.
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }
```

- [ ] **Step 2: Write the failing tests**

`internal/creds/creds_test.go`:

```go
package creds

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func testStore(t *testing.T) Store {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	return Store{Dirs: Dirs{Config: filepath.Join(home, ".config", "gfs"), Home: home}}
}

func listFile(t *testing.T, s Store) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.Dirs.Config, "identities"))
	return string(b)
}

func TestSetGetReplace(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.Get("me@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := s.Set("me@x.com", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("Me@X.com", "two"); err != nil {
		t.Fatal(err)
	}
	tok, src, err := s.Get("me@x.com")
	if err != nil || tok != "two" || src != "gfs" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	if got := listFile(t, s); got != "atlassian:me@x.com\n" {
		t.Fatalf("identities file %q", got)
	}
	if err := s.Set("a@x.com", ""); err == nil {
		t.Fatal("empty token must fail")
	}
}

func TestDeleteAndClear(t *testing.T) {
	s := testStore(t)
	s.Set("b@x.com", "tb")
	s.Set("a@x.com", "ta")
	if got := listFile(t, s); got != "atlassian:a@x.com\natlassian:b@x.com\n" {
		t.Fatalf("identities file %q", got)
	}
	if err := s.Delete("a@x.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("a@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete("a@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	n, err := s.Clear()
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if got := listFile(t, s); got != "" {
		t.Fatalf("identities file after clear %q", got)
	}
	if _, _, err := s.Get("b@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after clear: %v", err)
	}
}

func TestMissingEntry(t *testing.T) {
	s := testStore(t)
	s.Set("a@x.com", "ta")
	keyring.Delete("gfs", "atlassian:a@x.com") // entry removed behind gfs's back
	ids, _, err := s.Identities()
	if err != nil || len(ids) != 1 || !ids[0].Missing || ids[0].Source != "gfs" {
		t.Fatalf("got %+v %v", ids, err)
	}
	if err := s.Delete("a@x.com"); err != nil {
		t.Fatalf("delete of a missing entry: %v", err)
	}
	if got := listFile(t, s); got != "" {
		t.Fatalf("identities file %q", got)
	}
}

func TestAloginFallback(t *testing.T) {
	s := testStore(t)
	os.WriteFile(filepath.Join(s.Dirs.Home, ".alogin.json"), []byte(`[{"name":"work","email":"KB@x.com","createdAt":"x"}]`), 0o600)
	keyring.Set("alogin", "work", `{"email":"KB@x.com","token":"atok","accountId":"1"}`)
	tok, src, err := s.Get("kb@x.com")
	if err != nil || tok != "atok" || src != "alogin" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	ids, warns, err := s.Identities()
	if err != nil || len(warns) != 0 || len(ids) != 1 || ids[0] != (Identity{Email: "kb@x.com", Source: "alogin"}) {
		t.Fatalf("got %+v %v %v", ids, warns, err)
	}
	s.Set("kb@x.com", "gtok") // gfs entry wins and the identity is listed once
	if tok, src, _ := s.Get("kb@x.com"); tok != "gtok" || src != "gfs" {
		t.Fatalf("got %q %q", tok, src)
	}
	if ids, _, _ := s.Identities(); len(ids) != 1 || ids[0].Source != "gfs" {
		t.Fatalf("got %+v", ids)
	}
}

func TestAloginBadJSON(t *testing.T) {
	s := testStore(t)
	os.WriteFile(filepath.Join(s.Dirs.Home, ".alogin.json"), []byte(`{`), 0o600)
	if _, _, err := s.Get("kb@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad alogin JSON must read as not found: %v", err)
	}
	if _, warns, err := s.Identities(); err != nil || len(warns) != 1 || !strings.Contains(warns[0], ".alogin.json") {
		t.Fatalf("got %v %v", warns, err)
	}
}

func TestKeyringUnavailable(t *testing.T) {
	s := testStore(t)
	keyring.MockInitWithError(errors.New("no dbus"))
	defer keyring.MockInit()
	want := "keyring unavailable (no dbus); set GFS_CONFLUENCE_TOKEN instead"
	if _, _, err := s.Get("a@x.com"); err == nil || err.Error() != want {
		t.Fatalf("get: %v", err)
	}
	if err := s.Set("a@x.com", "t"); err == nil || err.Error() != want {
		t.Fatalf("set: %v", err)
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./internal/creds/`
Expected: FAIL to build (`undefined: Store`, `undefined: Dirs`, ...).

- [ ] **Step 4: Implement the store**

`internal/creds/creds.go`:

```go
// Package creds keeps service tokens in the system keyring, and the global
// per-host defaults (credentials spec).
package creds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const (
	Realm   = "atlassian"
	service = "gfs"
)

var ErrNotFound = errors.New("no stored token")

// Dirs locates gfs's config dir and the home dir (for ~/.alogin.json).
type Dirs struct {
	Config string // $XDG_CONFIG_HOME/gfs
	Home   string
}

func DefaultDirs() Dirs {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return Dirs{Config: filepath.Join(cfg, "gfs"), Home: home}
}

type Store struct{ Dirs Dirs }

type Identity struct {
	Email   string
	Source  string // "gfs" or "alogin"
	Missing bool   // listed by gfs, but the keyring entry is gone
}

func key(email string) string { return Realm + ":" + strings.ToLower(email) }

func unavailable(err error) error {
	return fmt.Errorf("keyring unavailable (%v); set GFS_CONFLUENCE_TOKEN instead", err)
}

func (s Store) listPath() string { return filepath.Join(s.Dirs.Config, "identities") }

func (s Store) readList() ([]string, error) {
	data, err := os.ReadFile(s.listPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keys = append(keys, l)
		}
	}
	return keys, nil
}

func (s Store) writeList(keys []string) error {
	sort.Strings(keys)
	keys = slices.Compact(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\n")
	}
	return workdir.WriteAtomic(s.listPath(), []byte(b.String()))
}

// Get returns the token for email and where it came from: "gfs" or "alogin".
func (s Store) Get(email string) (token, source string, err error) {
	tok, err := keyring.Get(service, key(email))
	if err == nil {
		return tok, "gfs", nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return "", "", unavailable(err)
	}
	if tok, ok := aloginToken(s.Dirs.Home, email); ok {
		return tok, "alogin", nil
	}
	return "", "", ErrNotFound
}

// Set stores or replaces the token for email.
func (s Store) Set(email, token string) error {
	if token == "" {
		return errors.New("empty token")
	}
	if err := keyring.Set(service, key(email), token); err != nil {
		return unavailable(err)
	}
	keys, err := s.readList()
	if err != nil {
		return err
	}
	return s.writeList(append(keys, key(email)))
}

// Delete removes gfs's entry for email; ErrNotFound when gfs never stored it.
func (s Store) Delete(email string) error {
	keys, err := s.readList()
	if err != nil {
		return err
	}
	k := key(email)
	if err := keyring.Delete(service, k); err != nil {
		if !errors.Is(err, keyring.ErrNotFound) {
			return unavailable(err)
		}
		if !slices.Contains(keys, k) {
			return ErrNotFound
		}
	}
	return s.writeList(slices.DeleteFunc(keys, func(x string) bool { return x == k }))
}

// Clear deletes every gfs entry and returns how many identities were listed.
func (s Store) Clear() (int, error) {
	keys, err := s.readList()
	if err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := keyring.Delete(service, k); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return 0, unavailable(err)
		}
	}
	return len(keys), s.writeList(nil)
}

// Identities lists gfs's identities, then alogin-only ones. The warnings
// report an unreadable ~/.alogin.json.
func (s Store) Identities() ([]Identity, []string, error) {
	keys, err := s.readList()
	if err != nil {
		return nil, nil, err
	}
	var ids []Identity
	seen := map[string]bool{}
	for _, k := range keys {
		email, ok := strings.CutPrefix(k, Realm+":")
		if !ok {
			continue
		}
		_, gerr := keyring.Get(service, k)
		if gerr != nil && !errors.Is(gerr, keyring.ErrNotFound) {
			return nil, nil, unavailable(gerr)
		}
		ids = append(ids, Identity{Email: email, Source: "gfs", Missing: gerr != nil})
		seen[email] = true
	}
	var warnings []string
	profiles, perr := aloginProfiles(s.Dirs.Home)
	if perr != nil {
		warnings = append(warnings, perr.Error())
	}
	var extra []string
	for _, p := range profiles {
		if e := strings.ToLower(p.Email); e != "" && !seen[e] {
			seen[e] = true
			extra = append(extra, e)
		}
	}
	sort.Strings(extra)
	for _, e := range extra {
		ids = append(ids, Identity{Email: e, Source: "alogin"})
	}
	return ids, warnings, nil
}
```

`internal/creds/alogin.go`:

```go
package creds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// alogin (forge-cli) keeps profiles in ~/.alogin.json and each profile's
// {email, token, accountId} in the keyring under service "alogin". Read only.
type aloginProfile struct{ Name, Email string }

// aloginProfiles returns no profiles when the file is missing or unreadable,
// and an error only when it does not parse.
func aloginProfiles(home string) ([]aloginProfile, error) {
	path := filepath.Join(home, ".alogin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var ps []aloginProfile
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ps, nil
}

func aloginToken(home, email string) (string, bool) {
	ps, _ := aloginProfiles(home)
	for _, p := range ps {
		if !strings.EqualFold(p.Email, email) {
			continue
		}
		raw, err := keyring.Get("alogin", p.Name)
		if err != nil {
			return "", false
		}
		var c struct {
			Token string `json:"token"`
		}
		if json.Unmarshal([]byte(raw), &c) != nil || c.Token == "" {
			return "", false
		}
		return c.Token, true
	}
	return "", false
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/creds/ ./internal/workdir/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/workdir internal/creds
git commit -m "creds: tokens in the keyring, identity list, alogin fallback"
```

---

### Task 2: Global config and the Lookup used by adapters

**Files:**
- Create: `internal/creds/global.go`, `internal/creds/lookup.go`
- Test: `internal/creds/global_test.go`, `internal/creds/lookup_test.go`

**Interfaces:**
- Consumes: `creds.Store`, `creds.Dirs`, `creds.DefaultDirs()` (Task 1); `workdir.NewConfig`, `workdir.ParseConfig`, `Config.Get/Set/Sections/Bytes`, `workdir.WriteAtomic`.
- Produces:
  - `func creds.LoadGlobal(d creds.Dirs) (*creds.Global, error)`
  - `(*Global).Path() string`, `HostEmail(host string) string`, `SetHostEmail(host, email string)`, `HostsFor(email string) []string`, `Save() error`
  - `type creds.Lookup interface { Token(email string) (string, error); HostEmail(host string) (string, error); SoleIdentity() string }`
  - `type creds.System struct{}` implementing `Lookup`

- [ ] **Step 1: Write the failing tests**

`internal/creds/global_test.go`:

```go
package creds

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGlobalHostDefaults(t *testing.T) {
	d := Dirs{Config: filepath.Join(t.TempDir(), "gfs")}
	g, err := LoadGlobal(d)
	if err != nil || g.HostEmail("acme.atlassian.net") != "" {
		t.Fatalf("missing file must read as empty: %v", err)
	}
	g.SetHostEmail("other.atlassian.net", "me@x.com")
	g.SetHostEmail("ACME.atlassian.net", "me@x.com")
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d.Config, "config"))
	want := "[host \"acme.atlassian.net\"]\nemail = me@x.com\n\n[host \"other.atlassian.net\"]\nemail = me@x.com\n"
	if string(b) != want {
		t.Fatalf("config file:\n%s", b)
	}
	g, _ = LoadGlobal(d)
	if got := g.HostEmail("acme.atlassian.net"); got != "me@x.com" {
		t.Fatalf("host email %q", got)
	}
	if got := g.HostsFor("ME@x.com"); !slices.Equal(got, []string{"acme.atlassian.net", "other.atlassian.net"}) {
		t.Fatalf("hosts %v", got)
	}
	if g.Path() != filepath.Join(d.Config, "config") {
		t.Fatal(g.Path())
	}
}

func TestGlobalBadFile(t *testing.T) {
	d := Dirs{Config: t.TempDir()}
	os.WriteFile(filepath.Join(d.Config, "config"), []byte("no equals sign\n"), 0o644)
	if _, err := LoadGlobal(d); err == nil || !strings.Contains(err.Error(), filepath.Join(d.Config, "config")) {
		t.Fatalf("got %v", err)
	}
}
```

`internal/creds/lookup_test.go`:

```go
package creds

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func systemEnv(t *testing.T) Dirs {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return DefaultDirs()
}

func TestDefaultDirs(t *testing.T) {
	d := systemEnv(t)
	if d.Config != filepath.Join(d.Home, "xdg", "gfs") {
		t.Fatalf("%+v", d)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultDirs().Config; got != filepath.Join(d.Home, ".config", "gfs") {
		t.Fatal(got)
	}
}

func TestSystemLookup(t *testing.T) {
	d := systemEnv(t)
	var lk Lookup = System{}
	if got := lk.SoleIdentity(); got != "" {
		t.Fatalf("no identities: %q", got)
	}
	Store{Dirs: d}.Set("me@x.com", "tok")
	if got := lk.SoleIdentity(); got != "me@x.com" {
		t.Fatalf("one identity: %q", got)
	}
	if tok, err := lk.Token("me@x.com"); err != nil || tok != "tok" {
		t.Fatalf("token %q %v", tok, err)
	}
	Store{Dirs: d}.Set("two@x.com", "tok2")
	if got := lk.SoleIdentity(); got != "" {
		t.Fatalf("two identities: %q", got)
	}
	g, _ := LoadGlobal(d)
	g.SetHostEmail("acme.atlassian.net", "two@x.com")
	g.Save()
	if got, err := lk.HostEmail("acme.atlassian.net"); err != nil || got != "two@x.com" {
		t.Fatalf("host email %q %v", got, err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/creds/`
Expected: FAIL to build (`undefined: LoadGlobal`, `undefined: Lookup`, `undefined: System`).

- [ ] **Step 3: Implement**

`internal/creds/global.go`:

```go
package creds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Global is $XDG_CONFIG_HOME/gfs/config: [host "<host>"] email = <email>.
type Global struct {
	path string
	cfg  *workdir.Config
}

func LoadGlobal(d Dirs) (*Global, error) {
	path := filepath.Join(d.Config, "config")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Global{path: path, cfg: workdir.NewConfig()}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := workdir.ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Global{path: path, cfg: cfg}, nil
}

func (g *Global) Path() string { return g.path }

func hostSection(host string) string { return fmt.Sprintf("host %q", strings.ToLower(host)) }

func (g *Global) HostEmail(host string) string { return g.cfg.Get(hostSection(host), "email") }

func (g *Global) SetHostEmail(host, email string) { g.cfg.Set(hostSection(host), "email", email) }

// HostsFor returns, sorted, the hosts whose default email is email.
func (g *Global) HostsFor(email string) []string {
	var hosts []string
	for _, sec := range g.cfg.Sections() {
		var h string
		if _, err := fmt.Sscanf(sec, "host %q", &h); err != nil {
			continue
		}
		if strings.EqualFold(g.cfg.Get(sec, "email"), email) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (g *Global) Save() error { return workdir.WriteAtomic(g.path, g.cfg.Bytes()) }
```

`internal/creds/lookup.go`:

```go
package creds

// Lookup is what an adapter needs to resolve an identity (spec §5).
type Lookup interface {
	Token(email string) (string, error) // ErrNotFound when nothing is stored
	HostEmail(host string) (string, error)
	SoleIdentity() string // "" unless exactly one identity is known
}

// System uses the real keyring and config dirs, resolved on every call so
// tests can redirect HOME and XDG_CONFIG_HOME.
type System struct{}

func (System) Token(email string) (string, error) {
	tok, _, err := Store{Dirs: DefaultDirs()}.Get(email)
	return tok, err
}

func (System) HostEmail(host string) (string, error) {
	g, err := LoadGlobal(DefaultDirs())
	if err != nil {
		return "", err
	}
	return g.HostEmail(host), nil
}

// SoleIdentity ignores keyring errors: it is the last resort for the email,
// and a broken keyring is reported by the token lookup that follows.
func (System) SoleIdentity() string {
	ids, _, err := Store{Dirs: DefaultDirs()}.Identities()
	if err != nil {
		return ""
	}
	var live []string
	for _, id := range ids {
		if !id.Missing {
			live = append(live, id.Email)
		}
	}
	if len(live) == 1 {
		return live[0]
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/creds/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/creds
git commit -m "creds: global per-host default email; Lookup for adapters"
```

---

### Task 3: Clone pins the session's identity

**Files:**
- Modify: `internal/adapter/adapter.go` (add `Identified` after the `Session` interface)
- Modify: `internal/engine/clone.go:14-15`
- Test: `internal/engine/clone_test.go`

**Interfaces:**
- Consumes: `engine.Clone(ctx, ad, sess, rawURL, dir, out)` (unchanged signature), `fake.New()`.
- Produces: `type adapter.Identified interface{ Identity() string }`; `.gfs/config` gets `[remote] email = <identity>` on clone when the session implements it with a non-empty value.

- [ ] **Step 1: Write the failing test**

`internal/engine/clone_test.go`:

```go
package engine

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

type identified struct {
	adapter.Session
	id string
}

func (s identified) Identity() string { return s.id }

func TestClonePinsIdentity(t *testing.T) {
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, identified{Session: sess, id: "me@x.com"}, "fake://x", filepath.Join(t.TempDir(), "wt"), &out)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := env.Tree.LoadConfig()
	if got := cfg.Get("remote", "email"); got != "me@x.com" {
		t.Fatalf("remote email %q", got)
	}

	plain, _, _ := cloned(t) // the fake session has no identity
	cfg, _ = plain.Tree.LoadConfig()
	if got := cfg.Get("remote", "email"); got != "" {
		t.Fatalf("remote email %q", got)
	}
}
```

- [ ] **Step 2: Run the test to see it fail**

Run: `go test ./internal/engine/ -run TestClonePinsIdentity`
Expected: FAIL with `remote email ""`.

- [ ] **Step 3: Implement**

In `internal/adapter/adapter.go`, after the `Session` interface:

```go
// Identified is implemented by sessions that know which account they act as.
// Clone records the identity as [remote] email (credentials spec §6).
type Identified interface{ Identity() string }
```

In `internal/engine/clone.go`, after `cfg.Set("remote", "url", rawURL)`:

```go
	if id, ok := sess.(adapter.Identified); ok && id.Identity() != "" {
		cfg.Set("remote", "email", id.Identity())
	}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/engine/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/adapter/adapter.go internal/engine
git commit -m "engine: clone records the session identity as [remote] email"
```

---

### Task 4: Confluence resolves identity and token through creds

**Files:**
- Modify: `internal/adapter/confluence/url.go` (whole file), `internal/adapter/confluence/adapter.go:28-34` (`Open`), `internal/adapter/confluence/corpus_test.go:26`, `internal/adapter/confluence/session.go` (add `Identity`), `internal/adapter/confluence/client.go` (add `VerifyToken`), `internal/adapter/confluence/cftest/server.go` (add `Accounts`, route, handler)
- Test: `internal/adapter/confluence/client_test.go` (replace `TestParseTarget`, add `TestVerifyToken`), `internal/adapter/confluence/session_test.go` (add `TestSessionIdentity`)

**Interfaces:**
- Consumes: `creds.Lookup`, `creds.System{}`, `creds.ErrNotFound` (Task 2); `adapter.Identified` (Task 3).
- Produces:
  - `parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error)`
  - `func (s *session) Identity() string`
  - `func confluence.VerifyToken(ctx context.Context, base, email, token string) (string, error)` returning the display name
  - `cftest.Server.Accounts map[string]string` (email → token; nil accepts any credentials) and `GET /wiki/rest/api/user/current` → `{"accountId","displayName","email"}`

- [ ] **Step 1: Write the failing tests**

Replace `TestParseTarget` in `internal/adapter/confluence/client_test.go` (keep `env`), and add the fake lookup and `TestVerifyToken`. Add `"github.com/KrzysztofBogdan/gitfs/internal/creds"` to the imports.

```go
type fakeLookup struct {
	tokens map[string]string
	hosts  map[string]string
	sole   string
}

func (f fakeLookup) Token(e string) (string, error) {
	if t, ok := f.tokens[e]; ok {
		return t, nil
	}
	return "", creds.ErrNotFound
}
func (f fakeLookup) HostEmail(h string) (string, error) { return f.hosts[h], nil }
func (f fakeLookup) SoleIdentity() string               { return f.sole }

func TestParseTarget(t *testing.T) {
	both := env(map[string]string{"GFS_CONFLUENCE_TOKEN": "tok", "GFS_CONFLUENCE_EMAIL": "me@x.com"})
	none := env(nil)
	const acme = "acme.atlassian.net"
	cases := []struct {
		name    string
		raw     string
		cfg     map[string]string
		getenv  func(string) string
		lk      fakeLookup
		base    string
		email   string
		token   string
		wantErr string
	}{
		{"env", "confluence://acme.atlassian.net/ENG", nil, both, fakeLookup{}, "https://acme.atlassian.net", "me@x.com", "tok", ""},
		{"base query", "confluence://acme.atlassian.net/ENG?base=http://127.0.0.1:9", nil, both, fakeLookup{}, "http://127.0.0.1:9", "me@x.com", "tok", ""},
		{"env email beats URL user", "confluence://u%40x.com@acme.atlassian.net/ENG", nil, both, fakeLookup{}, "https://acme.atlassian.net", "me@x.com", "tok", ""},
		{"URL user, cfg base, env token", "confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"base": "http://b"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), fakeLookup{}, "http://b", "u@x.com", "t", ""},
		{"URL user beats remote email", "confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none,
			fakeLookup{tokens: map[string]string{"u@x.com": "ut"}}, "https://acme.atlassian.net", "u@x.com", "ut", ""},
		{"remote email beats host default", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none,
			fakeLookup{tokens: map[string]string{"c@x.com": "ct"}, hosts: map[string]string{acme: "h@x.com"}}, "https://acme.atlassian.net", "c@x.com", "ct", ""},
		{"host default beats sole identity", "confluence://acme.atlassian.net/ENG", nil, none,
			fakeLookup{tokens: map[string]string{"h@x.com": "ht"}, hosts: map[string]string{acme: "h@x.com"}, sole: "s@x.com"}, "https://acme.atlassian.net", "h@x.com", "ht", ""},
		{"sole identity", "confluence://acme.atlassian.net/ENG", nil, none,
			fakeLookup{tokens: map[string]string{"s@x.com": "st"}, sole: "s@x.com"}, "https://acme.atlassian.net", "s@x.com", "st", ""},
		{"env token beats store", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "envtok"}),
			fakeLookup{tokens: map[string]string{"c@x.com": "ct"}}, "https://acme.atlassian.net", "c@x.com", "envtok", ""},
		{"bad path", "confluence://acme.atlassian.net/", nil, both, fakeLookup{}, "", "", "", "want confluence://<host>/<SPACEKEY>"},
		{"no identity", "confluence://acme.atlassian.net/ENG", nil, none, fakeLookup{}, "", "", "",
			"no identity for acme.atlassian.net: run gfs auth set <email> --host acme.atlassian.net, or put the email in the URL (confluence://me%40x.com@acme.atlassian.net/ENG)"},
		{"no token", "confluence://acme.atlassian.net/ENG", map[string]string{"email": "c@x.com"}, none, fakeLookup{}, "", "", "",
			"no token for c@x.com: run gfs auth set c@x.com"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		got, err := parseTarget(u, c.cfg, c.getenv, c.lk)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.base != c.base || got.space != "ENG" || got.email != c.email || got.token != c.token {
			t.Errorf("%s: got %+v %v", c.name, got, err)
		}
	}
}

func TestVerifyToken(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.Accounts = map[string]string{"me@x.com": "good"}
	name, err := VerifyToken(context.Background(), s.URL, "me@x.com", "good")
	if err != nil || name != "Me" {
		t.Fatalf("got %q %v", name, err)
	}
	_, err = VerifyToken(context.Background(), s.URL, "me@x.com", "bad")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("bad token: %v", err)
	}
}
```

Add to `internal/adapter/confluence/session_test.go` (add the `adapter` import if the file lacks it):

```go
func TestSessionIdentity(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.AddSpace("ENG", "100")
	sess, err := openSession(context.Background(), target{base: s.URL, space: "ENG", email: "me@x.com", token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var id adapter.Identified = sess
	if id.Identity() != "me@x.com" {
		t.Fatal(id.Identity())
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/adapter/confluence/`
Expected: FAIL to build (`too many arguments in call to parseTarget`, `undefined: VerifyToken`, `s.Accounts undefined`, `*session does not implement adapter.Identified`).

- [ ] **Step 3: Implement resolution**

Replace `internal/adapter/confluence/url.go`:

```go
package confluence

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

type target struct{ base, host, space, email, token string }

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (target, error) {
	space := strings.Trim(u.Path, "/")
	if u.Host == "" || space == "" || strings.Contains(space, "/") {
		return target{}, fmt.Errorf("bad remote %q: want confluence://<host>/<SPACEKEY>", u.String())
	}
	t := target{host: u.Hostname(), space: space, base: "https://" + u.Host}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := u.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	email, err := resolveEmail(u, cfg, getenv, lk)
	if err != nil {
		return target{}, err
	}
	if email == "" {
		return target{}, fmt.Errorf("no identity for %s: run gfs auth set <email> --host %s, or put the email in the URL (confluence://me%%40x.com@%s/%s)", t.host, t.host, t.host, space)
	}
	t.email = email
	if t.token = getenv("GFS_CONFLUENCE_TOKEN"); t.token == "" {
		tok, err := lk.Token(email)
		if errors.Is(err, creds.ErrNotFound) {
			return target{}, fmt.Errorf("no token for %s: run gfs auth set %s", email, email)
		}
		if err != nil {
			return target{}, err
		}
		t.token = tok
	}
	return t, nil
}

// resolveEmail follows credentials spec §5: env, URL user, [remote] email,
// host default, sole identity.
func resolveEmail(u *url.URL, cfg map[string]string, getenv func(string) string, lk creds.Lookup) (string, error) {
	if e := getenv("GFS_CONFLUENCE_EMAIL"); e != "" {
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

In `internal/adapter/confluence/adapter.go`, `Open` becomes (add the `creds` import):

```go
func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openSession(ctx, t)
}
```

`internal/adapter/confluence/corpus_test.go:26` also calls it (a live test, skipped without `GFS_CORPUS_URL`); change that line to:

```go
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
```

and add the `creds` import there. Then `grep -rn 'parseTarget(' internal/` must show only `url.go`, `adapter.go`, `client_test.go` and `corpus_test.go`.

In `internal/adapter/confluence/session.go`, after `Close`:

```go
// Identity is the account this session acts as (adapter.Identified).
func (s *session) Identity() string { return s.c.t.email }
```

- [ ] **Step 4: Implement VerifyToken and the cftest endpoint**

Append to `internal/adapter/confluence/client.go`:

```go
// VerifyToken checks email and token against the site at base and returns
// the account's display name.
func VerifyToken(ctx context.Context, base, email, token string) (string, error) {
	c := newClient(target{base: strings.TrimRight(base, "/"), email: email, token: token})
	var u struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/wiki/rest/api/user/current", nil, &u); err != nil {
		return "", err
	}
	return u.DisplayName, nil
}
```

In `internal/adapter/confluence/cftest/server.go`:

- add a field to `Server`, after `Fail`:

```go
	Accounts  map[string]string // email -> token for GET /wiki/rest/api/user/current; nil accepts any credentials
```

- register the route in `New`, after `GET /wiki/rest/api/user`:

```go
	mux.HandleFunc("GET /wiki/rest/api/user/current", s.currentUser)
```

- add the handler after `getUser`:

```go
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) {
	u, p, _ := r.BasicAuth()
	if s.Accounts != nil && s.Accounts[u] != p {
		fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, 200, map[string]any{"accountId": "me", "displayName": s.users["me"], "email": u})
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/adapter/confluence/... ./internal/cli/`
Expected: PASS (the CLI e2e tests set both env vars, so they are unaffected).

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/adapter/confluence
git commit -m "confluence: resolve identity and token through creds; VerifyToken"
```

---

### Task 5: `gfs auth set|rm|clear|list`

**Files:**
- Create: `internal/cli/auth.go`
- Modify: `internal/cli/root.go` (add `newAuth()` to `root.AddCommand`)
- Test: `internal/cli/auth_test.go`

**Interfaces:**
- Consumes: `creds.Store`, `creds.DefaultDirs`, `creds.ErrNotFound`, `creds.Realm`, `creds.LoadGlobal` and `Global` methods (Tasks 1-2); `confluence.VerifyToken` (Task 4); `cli.usage`, `cli.prompter`, `cli.ExitError`, `cli.exitCode` (existing, `internal/cli/env.go` and `root.go`).
- Produces: `newAuth() *cobra.Command`; test helpers `authEnv(t) string` (returns the temp home) and `gfsIn(t, stdin string, args ...string) (string, int)`, reused by Task 6.

Output lines (exact):
- set: `verified: <display name> on <host>`, `stored: atlassian:<email>`, `default for <host>: <email>`
- rm: `removed: atlassian:<email>`, `note: still the default for <host> in <global config path>`
- clear: `deleted <n> stored tokens`, `nothing deleted`, `no stored tokens`
- list: `atlassian:<email>` padded to 28, source padded to 14 (`gfs`, `gfs (missing)`, `alogin`), then `default for <h1>, <h2>` when any; trailing spaces trimmed; `no stored tokens` when empty; `warning: <text>` on stderr per warning.

- [ ] **Step 1: Write the failing tests**

`internal/cli/auth_test.go`:

```go
package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// authEnv isolates the keyring, HOME and XDG_CONFIG_HOME, and chdirs to the temp home.
func authEnv(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GFS_CONFLUENCE_EMAIL", "")
	t.Setenv("GFS_CONFLUENCE_TOKEN", "")
	t.Chdir(home)
	return home
}

func gfsIn(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String(), exitCode(err)
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestAuthSetListRm(t *testing.T) {
	authEnv(t)
	out, code := gfsIn(t, "tok1\n", "auth", "set", "Me@X.com")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "stored: atlassian:me@x.com")
	gfsIn(t, "tok2\n", "auth", "set", "me@x.com")
	tok, src, err := creds.Store{Dirs: creds.DefaultDirs()}.Get("me@x.com")
	if err != nil || tok != "tok2" || src != "gfs" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "atlassian:me@x.com", "gfs")
	if strings.Contains(out, "tok2") {
		t.Fatalf("list printed the token:\n%s", out)
	}
	out, code = gfsIn(t, "", "auth", "rm", "me@x.com")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "removed: atlassian:me@x.com")
	out, code = gfsIn(t, "", "auth", "rm", "me@x.com")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "no stored token for me@x.com")
	if out, code = gfsIn(t, "\n", "auth", "set", "a@x.com"); code != 2 {
		t.Fatalf("empty token: exit %d\n%s", code, out)
	}
}

func TestAuthSetHost(t *testing.T) {
	authEnv(t)
	srv := cftest.New()
	defer srv.Close()
	srv.Accounts = map[string]string{"me@x.com": "good"}
	out, code := gfsIn(t, "bad\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "verify on acme.atlassian.net")
	if _, _, err := (creds.Store{Dirs: creds.DefaultDirs()}).Get("me@x.com"); !errors.Is(err, creds.ErrNotFound) {
		t.Fatalf("a failed verify must store nothing: %v", err)
	}
	out, code = gfsIn(t, "good\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL)
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "verified: Me on acme.atlassian.net", "stored: atlassian:me@x.com", "default for acme.atlassian.net: me@x.com")
	g, _ := creds.LoadGlobal(creds.DefaultDirs())
	if got := g.HostEmail("acme.atlassian.net"); got != "me@x.com" {
		t.Fatalf("host default %q", got)
	}
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "default for acme.atlassian.net")
	out, _ = gfsIn(t, "", "auth", "rm", "me@x.com")
	mustContain(t, out, "note: still the default for acme.atlassian.net in "+g.Path())
}

func TestAuthClear(t *testing.T) {
	authEnv(t)
	gfsIn(t, "ta\n", "auth", "set", "a@x.com")
	gfsIn(t, "tb\n", "auth", "set", "b@x.com")
	if out, code := gfsIn(t, "", "auth", "clear"); code != 2 {
		t.Fatalf("clear without a terminal and without --yes: exit %d\n%s", code, out)
	}
	out, code := gfsIn(t, "", "auth", "clear", "--yes")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "deleted 2 stored tokens")
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "no stored tokens")
}

func TestAuthAloginOnly(t *testing.T) {
	home := authEnv(t)
	os.WriteFile(filepath.Join(home, ".alogin.json"), []byte(`[{"name":"work","email":"kb@x.com"}]`), 0o600)
	keyring.Set("alogin", "work", `{"email":"kb@x.com","token":"atok"}`)
	out, _ := gfsIn(t, "", "auth", "list")
	mustContain(t, out, "atlassian:kb@x.com", "alogin")
	out, code := gfsIn(t, "", "auth", "rm", "kb@x.com")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "kb@x.com is stored by alogin, not gfs; nothing removed")
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/cli/ -run TestAuth`
Expected: FAIL (`unknown command "auth" for "gfs"`).

- [ ] **Step 3: Implement**

`internal/cli/auth.go`:

```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func newAuth() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Manage API tokens stored in the system keyring"}
	auth.AddCommand(newAuthSet(), newAuthRm(), newAuthClear(), newAuthList())
	return auth
}

func tokenStore() creds.Store { return creds.Store{Dirs: creds.DefaultDirs()} }

// readToken reads without echo from a terminal, else the first line of input.
func readToken(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "API token: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func newAuthSet() *cobra.Command {
	var host, base string
	cmd := &cobra.Command{
		Use:   "set <email>",
		Short: "Store or replace the API token for an email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(args[0])
			if base != "" && host == "" {
				return usage("--base needs --host")
			}
			token, err := readToken(cmd)
			if err != nil {
				return err
			}
			if token == "" {
				return usage("empty token")
			}
			out := cmd.OutOrStdout()
			if host != "" {
				if base == "" {
					base = "https://" + host
				}
				name, err := confluence.VerifyToken(cmd.Context(), base, email, token)
				if err != nil {
					return &ExitError{Code: 1, Err: fmt.Errorf("verify on %s: %w", host, err)}
				}
				fmt.Fprintf(out, "verified: %s on %s\n", name, host)
			}
			if err := tokenStore().Set(email, token); err != nil {
				return err
			}
			fmt.Fprintf(out, "stored: %s:%s\n", creds.Realm, email)
			if host != "" {
				g, err := creds.LoadGlobal(creds.DefaultDirs())
				if err != nil {
					return err
				}
				g.SetHostEmail(host, email)
				if err := g.Save(); err != nil {
					return err
				}
				fmt.Fprintf(out, "default for %s: %s\n", host, email)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "verify the token on this site and make the email its default")
	cmd.Flags().StringVar(&base, "base", "", "base URL instead of https://<host> (tests)")
	cmd.Flags().MarkHidden("base")
	return cmd
}

func newAuthRm() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <email>",
		Short: "Delete the stored API token for an email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(args[0])
			s := tokenStore()
			err := s.Delete(email)
			if errors.Is(err, creds.ErrNotFound) {
				if _, src, gerr := s.Get(email); gerr == nil && src == "alogin" {
					return &ExitError{Code: 1, Err: fmt.Errorf("%s is stored by alogin, not gfs; nothing removed", email)}
				}
				return &ExitError{Code: 1, Err: fmt.Errorf("no stored token for %s", email)}
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "removed: %s:%s\n", creds.Realm, email)
			g, err := creds.LoadGlobal(creds.DefaultDirs())
			if err != nil {
				return err
			}
			for _, h := range g.HostsFor(email) {
				fmt.Fprintf(out, "note: still the default for %s in %s\n", h, g.Path())
			}
			return nil
		},
	}
}

func newAuthClear() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete every API token gfs has stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			s := tokenStore()
			ids, _, err := s.Identities()
			if err != nil {
				return err
			}
			n := 0
			for _, id := range ids {
				if id.Source == "gfs" {
					n++
				}
			}
			if n == 0 {
				fmt.Fprintln(out, "no stored tokens")
				return nil
			}
			if !yes {
				ask := prompter()
				if ask == nil {
					return usage("refusing to delete %d stored tokens without a terminal; pass --yes", n)
				}
				if !ask(fmt.Sprintf("delete %d stored tokens?", n)) {
					fmt.Fprintln(out, "nothing deleted")
					return nil
				}
			}
			if n, err = s.Clear(); err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted %d stored tokens\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func newAuthList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored identities and the hosts that default to them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, warnings, err := tokenStore().Identities()
			if err != nil {
				return err
			}
			g, err := creds.LoadGlobal(creds.DefaultDirs())
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			out := cmd.OutOrStdout()
			if len(ids) == 0 {
				fmt.Fprintln(out, "no stored tokens")
				return nil
			}
			for _, id := range ids {
				src := id.Source
				if id.Missing {
					src += " (missing)"
				}
				line := fmt.Sprintf("%-28s %-14s", creds.Realm+":"+id.Email, src)
				if hosts := g.HostsFor(id.Email); len(hosts) > 0 {
					line += " default for " + strings.Join(hosts, ", ")
				}
				fmt.Fprintln(out, strings.TrimRight(line, " "))
			}
			return nil
		},
	}
}
```

In `internal/cli/root.go`, the `AddCommand` line becomes:

```go
	root.AddCommand(newClone(), newStatus(), newDiff(), newCommit(), newPull(), newResolve(), newLog(), newActions(), newGet(), newAuth())
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/cli
git commit -m "cli: gfs auth set, rm, clear, list"
```

---

### Task 6: End to end with a stored token; README

**Files:**
- Test: `internal/cli/confluence_e2e_test.go` (add `TestConfluenceStoredToken`)
- Modify: `Readme.md` ("Current repo state" shell block)

**Interfaces:**
- Consumes: `authEnv`, `gfsIn`, `mustContain` (Task 5, `internal/cli/auth_test.go`), `mustRun` (existing, `confluence_e2e_test.go`), `cftest.Server.Accounts` (Task 4), `creds.LoadGlobal` (Task 2).
- Produces: nothing new.

- [ ] **Step 1: Write the test**

Append to `internal/cli/confluence_e2e_test.go` (add `"github.com/KrzysztofBogdan/gitfs/internal/creds"` to the imports):

```go
// No env vars: the host default picks the email, the keyring gives the token,
// clone pins the email, and pull keeps using it after the host default changes.
func TestConfluenceStoredToken(t *testing.T) {
	home := authEnv(t)
	srv := cftest.New()
	defer srv.Close()
	srv.Accounts = map[string]string{"me@x.com": "good"}
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>x</p>"})

	if out, code := gfsIn(t, "good\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL); code != 0 {
		t.Fatal(out)
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?base="+srv.URL, "wt")
	cfg, _ := os.ReadFile(filepath.Join(home, "wt", ".gfs", "config"))
	mustContain(t, string(cfg), "email = me@x.com")

	g, _ := creds.LoadGlobal(creds.DefaultDirs())
	g.SetHostEmail("acme.atlassian.net", "other@x.com") // has no token: would fail if used
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(home, "wt"))
	mustRun(t, 0, "pull")
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/cli/ -run TestConfluenceStoredToken -v`
Expected: PASS. If it fails, the bug is in Tasks 1-5; fix it there, not in this test.

- [ ] **Step 3: Update the README**

In `Readme.md`, replace the shell block under "Current repo state" that starts with `export GFS_CONFLUENCE_EMAIL=` with:

```shell
gfs auth set me@example.com --host acme.atlassian.net   # asks for the Atlassian API token, keeps it in the system keyring
gfs clone confluence://acme.atlassian.net/ENG confluence   # the working tree remembers me@example.com
cd confluence
gfs get eng/Home/Runbooks.xml      # download a page's attachments into eng/Home/Runbooks.files/
vim eng/Home/Architecture.xml
gfs status
gfs commit --dry-run
gfs commit
```

and add after the block:

```markdown
`gfs auth list` shows stored identities (tokens stored by `alogin` are used too), `gfs auth rm <email>` and
`gfs auth clear` delete gfs's own tokens. `GFS_CONFLUENCE_EMAIL` and `GFS_CONFLUENCE_TOKEN` still override
everything (`docs/superpowers/specs/2026-09-24-gfs-credentials-design.md`).
```

- [ ] **Step 4: Full check and commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/confluence_e2e_test.go Readme.md
git commit -m "Stored-token end-to-end test through the CLI; README mentions gfs auth"
```
