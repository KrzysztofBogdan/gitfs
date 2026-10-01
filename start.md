# gfs: getting started

`gfs` mirrors a net service (Confluence and Jira today) as a directory of XML files.
You edit files; `gfs commit` makes the remote look like your working tree.
The vocabulary is git's, but the remote is a live service, not a repository:
there is no history graph, only the current remote state.

Service details: [docs/confluence.md](docs/confluence.md), [docs/jira.md](docs/jira.md),
[docs/dns.md](docs/dns.md) (OVH and ClouDNS zones).

## Install

```shell
./install.sh                     # builds ./cmd/gfs into ~/.local/bin/gfs
INSTALL_DIR=/some/dir ./install.sh
```

## Typical session

```shell
gfs auth set me@example.com --host acme.atlassian.net   # once per account
gfs clone confluence://acme.atlassian.net          # every space; or .../ENG for one
# or: gfs clone jira://acme.atlassian.net          # every project; or .../GEN for one
# or: gfs clone ovh://eu                            # DNS zones; cloudns://sub-1234 for ClouDNS
cd acme
vim "eng/Home/Architecture.xml"
gfs status                       # what will happen on the remote
gfs diff                         # canonical diff against the last synced state
gfs commit --dry-run             # check everything, change nothing
gfs commit
gfs pull                         # bring in remote changes
```

## Commands

`clone` and `pull` show a progress bar on stderr when it is a terminal
(listing, then pages downloaded with rate and ETA); piped or redirected
output gets none.

Every command except `clone` and `auth` finds the working tree by walking up
from the current directory to the nearest `.gfs/`. Paths are relative to the
current directory; no path means the whole tree.

| command | what it does |
|---------|--------------|
| `gfs clone <url> [<dir>]` | Create a working tree from a remote. Records the account it used as `[remote] email`. `-q` hides the progress bar. |
| `gfs status [<path>...]` | Changed files and the remote action each one resolves to. |
| `gfs diff [<path>...]` | Same header as `status`, then a unified diff of canonical XML against base. |
| `gfs commit [<path>...]` | Execute the resolved actions on the remote. |
| `gfs pull [<path>...]` | Update the working tree from the remote, merging where both sides changed. |
| `gfs resolve (--ours \| --theirs) <path>...` | Settle a conflicted file by taking one side. Sends nothing. |
| `gfs log [-n <count>] [<path>...]` | What past commits did on the remote (`.gfs/log`). |
| `gfs actions` | Verbs the remote understands and their policy. |
| `gfs get <path>...` | Download attachment bytes on demand (a file, a page, or a directory). |
| `gfs auth ...` | Manage stored API tokens (see below). |
| `gfs example <adapter> [<node>[/<variant>]]` | List or print body examples verified against the service. |
| `gfs schema <adapter>` | Print a RELAX NG schema for the adapter's files (`xmllint --relaxng`). |

Built-in docs: `gfs help start` (this page), `gfs help confluence`,
`gfs help confluence-storage`, `gfs help jira`, `gfs help dns`.

### commit flags

| flag | meaning |
|------|---------|
| `--dry-run` | Resolve and check everything against the remote, execute nothing. Exit code as if it had run. |
| `--allow <class>` | Treat `ask` as `allow` for this policy class in this run. Repeatable. |
| `--force` | Treat every `ask` as `allow` in this run; a `deny` still refuses. |
| `--no-merge` | If the remote changed since your base, refuse the file instead of merging. |

### pull flags

| flag | meaning |
|------|---------|
| `--force` | Replace conflicted files with the remote version, discarding local edits. |
| `--full` | Download every page instead of only what changed. Picks up label-only changes and deleted comments or attachments. |
| `-q`, `--quiet` | No progress bar. |

## Status letters

| letter | meaning |
|--------|---------|
| `A` | new file: will be created |
| `M` | changed: will be updated |
| `R` | same resource at a different path: will be moved or renamed |
| `D` | deleted file: will be deleted on the remote |
| `!` | last commit failed; details in the file's `<errors>` element |
| `C` | unresolved conflict; the file has `<conflict/>` and merge markers |

`[ask]` or `[deny]` after an action shows its policy. A `warning:` line under a
file means the edit uses content the service is not known to store unchanged;
it does not block the commit.

## Files

Every file is one XML document. The `<gfs>` envelope holds sync state; the
resource is inside `<content>`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <page id="98120" version="7">
      <title>Architecture</title>
      <body type="application/xhtml+xml">...</body>
    </page>
  </content>
</gfs>
```

* **Elements are yours** to edit: fields, bodies, sub-resources.
* **Attributes belong to the service** (ids, versions, timestamps, authors).
  Changing one is a commit error.
* A sub-resource element **without** its id attribute is new.
* A new file may be just the bare resource root (`<page>...</page>`); commit
  adds the envelope and rewrites it in canonical form.
* Files are compared in canonical form, so indentation, attribute order and
  element reordering never show up as changes.

## Conflicts

When a file changed both locally and on the remote, `commit` and `pull` try a
three-way merge. If it fails, the file gets `<conflict .../>` in the envelope
and git-style markers in the content (the file is not well-formed XML until
you fix it). Then either:

* edit it, remove the `<conflict/>` element, and `gfs commit`; or
* `gfs resolve --theirs <path>` (take the remote) or `--ours` (keep yours),
  then `gfs commit`.

Commit re-checks the remote afterwards, so a second concurrent edit gives a
fresh conflict, never a silent overwrite.

## Policy

Each action has a class (`create`, `update`, `delete`, `move`, ...) and each
class a policy in `.gfs/config`:

```ini
[policy]
delete = ask
```

* `allow`: run. `ask`: prompt `y/N` on a terminal; **without a terminal `ask`
  acts as `deny`**, so unattended runs never fire a gated action.
  `deny`: skip and report.
* Defaults: everything `allow` except `delete` (and `send`, `publish`,
  `reply`, `approve` for services that have them), which are `ask`. Jira
  `reply` is a public comment that emails a customer. Every DNS change is
  class `dns`, which asks.
* An ask shows what the action does, and some print a warning first (DNSSEC,
  a zone's own NS records).
* `gfs commit --allow delete` lifts `ask` for one run. An agent's permission
  rules can allow `gfs commit` and deny `gfs commit --allow`.

## Credentials

Tokens live in the system keyring, never in files. Tokens already stored by
`alogin` are found automatically (read only).

| command | what it does |
|---------|--------------|
| `gfs auth login <url>` | Guided: says where to get credentials for the remote (Jira, Confluence, OVH, ClouDNS), checks them and stores them. |
| `gfs auth set <email> [--host <host>]` | Store or replace the token (prompted without echo, or read from stdin). `--host` verifies it on that site first and makes the email the site's default. |
| `gfs auth rm <email>` / `<url>` | Delete gfs's token for that email, or a remote's credentials (`gfs auth rm ovh://eu`). |
| `gfs auth clear [--yes]` | Delete every token gfs stored. Asks unless `--yes`. |
| `gfs auth list` | Stored identities and remote entries, their source (`gfs` / `alogin`), and the hosts that default to them. Never prints tokens. |

An expired or revoked Atlassian token makes commands say `run gfs auth login <url>`.
OVH and ClouDNS credentials are one keyring entry per remote (`ovh:eu`,
`cloudns:sub-1234`), or environment variables in CI (`gfs help dns`).

Which Atlassian account a command uses, first match wins:

1. `GFS_CONFLUENCE_EMAIL` or `GFS_JIRA_EMAIL` (and `GFS_CONFLUENCE_TOKEN` / `GFS_JIRA_TOKEN` for the token)
2. the user in the URL: `confluence://me%40example.com@acme.atlassian.net/ENG`
3. `[remote] email` in the working tree's `.gfs/config` (written by `clone`)
4. the host default in `~/.config/gfs/config`:
   ```ini
   [host "acme.atlassian.net"]
   email = me@example.com
   ```
5. the only stored identity, if there is exactly one

## The `.gfs/` directory

| file | content |
|------|---------|
| `config` | remote URL, pinned email, policy |
| `base/` | each file as last synced; `status` and `diff` compare against it |
| `index` | resource id, version and path of every file |
| `attachments` | attachments fetched with `gfs get` and their versions |
| `log` | one line per remote action performed |

## Exit codes

| code | meaning |
|------|---------|
| 0 | everything succeeded |
| 1 | an action failed, was denied, or conflicted |
| 2 | usage or configuration error |
