# gfs credentials design

Status: draft for review. Date: 2026-09-24.
Extends `2026-09-23-gfs-cli-design.md` §3 (`clone`: "Credentials are resolved
by the adapter (environment, keyring, or prompt)").

## 1. Purpose

Today the Confluence adapter reads its API token only from
`GFS_CONFLUENCE_TOKEN`, and the email from `GFS_CONFLUENCE_EMAIL`, the URL
user, or `[remote] email`. Tokens in the environment leak into shell history
and child processes, and switching between accounts means re-exporting.

Design principle, borrowed from git: **the working tree says which identity;
a credential store holds the secret for that identity.** Git picks the
identity from the remote (`user@host`, `~/.ssh/config` `Host`) and asks a
credential helper for the secret keyed by protocol, host, and username.

### Goals

* Store API tokens in the system keyring, never in files.
* Several identities side by side; each working tree is pinned to one.
* Per-host default identity, so `gfs clone` needs no flags in the common case.
* `gfs auth` commands to set, update, remove, clear, and list stored tokens.
* Reuse tokens already stored by `alogin` (forge-cli) without copying them.
* Environment variables keep working and keep winning (CI, tests).

### Non-goals

* ssh-style host aliases (`confluence://work/ENG`). The per-host default and
  the URL user cover the same need.
* A pluggable credential-helper protocol (`git credential`-style executables).
* OAuth or any flow other than email + API token.
* Writing to or deleting `alogin`'s data.

## 2. Terms

| term     | meaning                                                                 |
|----------|-------------------------------------------------------------------------|
| realm    | a family of services that share one token per account; `atlassian` is the only realm now (Confluence, later Jira) |
| identity | an email within a realm                                                  |
| store    | where tokens live: the gfs keyring entries, falling back to `alogin`'s  |

## 3. Storage

### 3.1 gfs keyring entries

Library: `github.com/zalando/go-keyring` (macOS Keychain, Windows Credential
Manager, Linux Secret Service over D-Bus).

* service: `gfs`
* user: `<realm>:<email>`, e.g. `atlassian:me@example.com`
* secret: the token, as is

An Atlassian API token belongs to the account, not to a site, so one entry
serves every site the account can reach.

### 3.2 Identity list

The keyring cannot enumerate entries, so gfs keeps a list of stored
identities, without secrets, in `$XDG_CONFIG_HOME/gfs/identities`
(default `~/.config/gfs/identities`), one `<realm>:<email>` per line, sorted.
It is written atomically on every `set`, `rm`, and `clear`. A line whose
keyring entry is missing is reported by `gfs auth list` as `(missing)` and
removed by `rm` or `clear` without error.

### 3.3 alogin fallback (read only)

When the gfs store has no token for `atlassian:<email>`:

1. Read `~/.alogin.json`: a JSON array of `{name, email, …}` profiles.
2. Take the first profile whose `email` equals the email (case-insensitive).
3. `keyring.Get("alogin", profile.name)` returns JSON
   `{email, token, accountId}`; use `token`.

Any failure along the way (no file, no match, no entry, bad JSON) means "not
found". It is not an error, except that bad JSON is shown as a warning by
`gfs auth list`. gfs never writes `~/.alogin.json` or the `alogin` service.

### 3.4 Keyring unavailable

If the keyring backend fails (for example no D-Bus session on a headless
Linux box), a lookup reports: `keyring unavailable (<cause>); set
GFS_CONFLUENCE_TOKEN instead`. `gfs auth set` fails with the same message.

## 4. Global config

`$XDG_CONFIG_HOME/gfs/config` (default `~/.config/gfs/config`), same INI
format and parser as `.gfs/config` (`internal/workdir/config.go`):

```ini
[host "acme.atlassian.net"]
email = me@example.com

[host "other.atlassian.net"]
email = me@other.example
```

The section name is the literal string `host "acme.atlassian.net"`. The file
is optional; a missing file is an empty config. `gfs auth set --host` writes
it; people may also edit it by hand.

## 5. Resolution

In the Confluence adapter (`parseTarget`), for remote host `H`:

**Email**, first non-empty of:

1. `GFS_CONFLUENCE_EMAIL`
2. URL user (`confluence://me%40x.com@H/SPACE`)
3. `[remote] email` in `.gfs/config`
4. `[host "H"] email` in the global config
5. the only `atlassian:` identity, if exactly one is known in the gfs
   identity list and alogin profiles together

**Token**, first non-empty of:

1. `GFS_CONFLUENCE_TOKEN`
2. the store for `atlassian:<email>` (gfs entry, then alogin)

Errors:

* no email: `no identity for H: run gfs auth set <email> --host H, or put the
  email in the URL (confluence://me%40x.com@H/SPACE)`
* no token: `no token for me@x.com: run gfs auth set me@x.com`

The adapter receives the store and the global config through its
dependencies (as `getenv` is passed today), so tests use a mock keyring and
a temp config dir.

## 6. Clone pins the identity

`adapter.Session` gains an optional interface:

```go
type Identified interface{ Identity() string }
```

The Confluence session returns the resolved email. `engine.Clone` writes it
to `[remote] email` in `.gfs/config` when the session implements the
interface and the value is non-empty. Every later command in that tree
resolves to the same account at step 3, whatever the host default says
later. Environment variables still override (steps 1-2).

## 7. Commands

All `gfs auth` commands work outside a working tree. They manage only gfs's
own entries (§3.1, §3.2) and the global config. The realm is `atlassian`;
a `--realm` flag is not added until a second realm exists.

### `gfs auth set <email> [--host <host>]`

Store the token for `<email>`, replacing any existing one; this is also how a
token is updated. The token is read without echo when stdin is a terminal,
otherwise from the first line of stdin (for scripts:
`pass show atl | gfs auth set me@x.com`). An empty token is an error.

With `--host`, first verify the token with
`GET https://<host>/wiki/rest/api/user/current` using basic auth; a non-200
response fails and stores nothing. On success, store the token and set
`[host "<host>"] email = <email>` in the global config. The check is
`confluence.VerifyToken(ctx, base, email, token) (displayName, error)`; a
hidden `--base` flag overrides `https://<host>` so tests can point it at
`cftest`.

```text
$ gfs auth set me@x.com --host acme.atlassian.net
API token:
verified: Krzysztof Bogdan on acme.atlassian.net
stored: atlassian:me@x.com
default for acme.atlassian.net: me@x.com
```

### `gfs auth rm <email>`

Delete the gfs entry and remove the identity from the list. Host defaults in
the global config that point to it are left alone and reported:

```text
$ gfs auth rm me@x.com
removed: atlassian:me@x.com
note: still the default for acme.atlassian.net in ~/.config/gfs/config
```

If the email has no gfs entry but has an alogin profile:
`me@x.com is stored by alogin, not gfs; nothing removed` (exit 1).
Unknown email: `no stored token for me@x.com` (exit 1).

### `gfs auth clear [--yes]`

Delete every gfs entry and empty the identity list. Asks
`delete N stored tokens? [y/N]` unless `--yes`; without a terminal and
without `--yes` it refuses (exit 2). The global config is untouched.

### `gfs auth list`

One line per identity, gfs entries first, then alogin-only ones; never
prints tokens.

```text
$ gfs auth list
atlassian:me@x.com          gfs      default for acme.atlassian.net
atlassian:kb@dwa.ovh        alogin
atlassian:old@x.com         gfs (missing)
```

## 8. Code layout

* `internal/creds`: `Store` (Get, Set, Delete, List) over go-keyring plus the
  identity list; `alogin.go` for the fallback; `global.go` for the global
  config path and host defaults. Paths come from a `Dirs` value
  (`ConfigDir`, `Home`) so tests use temp dirs.
* `internal/adapter/confluence/url.go`: resolution per §5;
  `VerifyToken` in `client.go`.
* `internal/adapter/adapter.go`: `Identified`.
* `internal/engine/clone.go`: write `[remote] email`.
* `internal/cli/auth.go`: the `auth` command group.

## 9. Testing

* `creds`: `keyring.MockInit()`; set/replace/rm/clear/list; identity list
  kept in sync; missing entries; alogin fallback from fixture
  `~/.alogin.json` with mocked `alogin` entries, including no match and bad
  JSON.
* Resolution: table test covering every step of §5 in order and both error
  messages.
* `auth set --host`: `cftest` gains `GET /wiki/rest/api/user/current`
  (200 with `displayName` for the configured email and token, 401
  otherwise); 200 stores and sets the host default, 401 stores nothing.
* End to end: the existing e2e tests keep passing (env first); a new one
  clones with the token only in the mock store and checks that
  `.gfs/config` gets `[remote] email`, then `pull` works with no env vars.
