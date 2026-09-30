<p align="center">
    <h1 align="center">gfs</h1>
</p>
<hr>
<h3 align="center">Net services as files</h3>
<p align="center">gfs mirrors a net service as a directory of XML files, so humans and coding agents can read, search and change it with ordinary file tools.</p>
<p align="center">
    <a href="https://github.com/KrzysztofBogdan/gitfs/releases">Releases</a> ·
    <a href="start.md">Documentation</a> ·
    <a href="example/">Examples</a> ·
    <a href="https://github.com/KrzysztofBogdan/gitfs/issues">Issues</a>
</p>
<p align="center">
    <a href="https://github.com/KrzysztofBogdan/gitfs/releases/latest"><img src="https://img.shields.io/github/v/release/KrzysztofBogdan/gitfs" alt="Latest release"></a>
    &nbsp;
    <a href="https://github.com/KrzysztofBogdan/gitfs/actions/workflows/release.yml"><img src="https://github.com/KrzysztofBogdan/gitfs/actions/workflows/release.yml/badge.svg" alt="Release"></a>
    &nbsp;
    <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/KrzysztofBogdan/gitfs" alt="Go version"></a>
</p>

<hr>

(Warning: This is written by human but added em dashes so you will never be sure).

### Menu

- [Features](#features)
- [Install](#install)
    - [Linux and macOS](#linux-and-macos)
    - [Windows](#windows)
    - [With Go](#with-go)
- [Build from source](#build-from-source)
    - [For development](#for-development)
    - [With version information](#with-version-information)
    - [Releasing](#releasing)
- [Quick start](#quick-start)
- [Design](#design)
- [Status](#status)
- [Background](#background)
- [What is GitFS](#what-is-gitfs)
- [Comparison with git](#comparison-with-git)


## Features

- **One file shape for every service**: an XML document with a `<gfs>` envelope around the resource
- **Git vocabulary**: `clone`, `status`, `diff`, `commit`, `pull`, `resolve`, `log`
- **Plan before it fires**: `gfs status` and `gfs commit --dry-run` print the remote actions a change resolves to
- **Policy on irreversible verbs**: `send`, `delete` and `publish` ask first, and on a non-terminal `ask` means `deny`
- **Three-way merge** when the remote changed under you, with `gfs resolve --ours | --theirs` for conflicts
- **Errors land in the file** you are already looking at, and `.gfs/log` keeps the audit trail
- **Attachments on demand** with `gfs get`
- **Tokens in the system keyring** (macOS Keychain, Windows Credential Manager, Secret Service on Linux)
- **Single static binary**, no runtime dependencies
- Built for coding agents, validated with plain `xmllint` (`gfs schema` prints a RELAX NG schema)


## Install

The simplest way is to download `gfs` from [GitHub Releases](https://github.com/KrzysztofBogdan/gitfs/releases) and put the executable in your `PATH`.
Builds exist for Linux, macOS and Windows, on `amd64` and `arm64`.

### Linux and macOS

Pick the archive for your system: `linux_amd64`, `linux_arm64`, `darwin_amd64` (Intel Mac) or `darwin_arm64` (Apple silicon).

```bash
$ curl -sL https://github.com/KrzysztofBogdan/gitfs/releases/latest/download/gfs_linux_amd64.tar.gz | tar xz gfs
$ mkdir -p ~/.local/bin && mv gfs ~/.local/bin/
$ gfs --version
```

`~/.local/bin` must be on your `PATH`; any other directory on it works too.

_**macOS:** a binary downloaded with `curl` runs as is. If you downloaded the archive in a browser, macOS blocks the unsigned binary; clear the quarantine flag with `xattr -d com.apple.quarantine gfs`._

_**Linux:** tokens are kept through the Secret Service (GNOME Keyring, KWallet). On a headless machine without one, use the `GFS_CONFLUENCE_EMAIL` and `GFS_CONFLUENCE_TOKEN` environment variables instead._

### Windows

In PowerShell (use `gfs_windows_arm64.zip` on ARM):

```powershell
PS> Invoke-WebRequest https://github.com/KrzysztofBogdan/gitfs/releases/latest/download/gfs_windows_amd64.zip -OutFile gfs.zip
PS> Expand-Archive gfs.zip -DestinationPath "$env:LOCALAPPDATA\gfs"
PS> $p = [Environment]::GetEnvironmentVariable("Path", "User")
PS> [Environment]::SetEnvironmentVariable("Path", "$p;$env:LOCALAPPDATA\gfs", "User")
```

Open a new terminal and run `gfs --version`. SmartScreen may warn about the unsigned `gfs.exe` on first run.

### With Go

If you have [Go 1.25 or newer](https://go.dev/dl/):

```bash
$ go install github.com/KrzysztofBogdan/gitfs/cmd/gfs@latest
```

The binary lands in `$(go env GOPATH)/bin`.

Every release lists its archives and a `checksums.txt` (`sha256sum --ignore-missing -c checksums.txt`).


## Build from source

Requirements:

- [Go 1.25 or newer](https://go.dev/dl/)

### For development

```bash
$ git clone https://github.com/KrzysztofBogdan/gitfs.git
$ cd gitfs
$ go build -o gfs ./cmd/gfs
```

or build and install into `~/.local/bin` (override with `INSTALL_DIR`):

```bash
$ ./install.sh
```

These builds report `gfs version dev`. Run the tests with:

```bash
$ go test ./...
```

### With version information

The version is stamped at link time:

```bash
$ go build -ldflags "-X github.com/KrzysztofBogdan/gitfs/internal/cli.Version=v0.1.1" -o gfs ./cmd/gfs
```

To build every release archive locally, exactly as CI does, use [GoReleaser](https://goreleaser.com); the output goes to `dist/`:

```bash
$ go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

### Releasing

Push a `v*` tag. The [release workflow](.github/workflows/release.yml) runs the tests, builds all archives with [`.goreleaser.yaml`](.goreleaser.yaml) and publishes a GitHub Release:

```bash
$ git tag v0.2.0 && git push origin v0.2.0
```


## Quick start

`gfs start` prints the overview of commands, files, policy and credentials; `gfs confluence` explains the Confluence layout.

```bash
$ gfs auth set me@example.com --host acme.atlassian.net      # asks for the Atlassian API token, keeps it in the system keyring
$ gfs clone confluence://acme.atlassian.net/ENG confluence   # the working tree remembers me@example.com
$ cd confluence
$ gfs get eng/Home/Runbooks.xml      # download a page's attachments into eng/Home/Runbooks.files/
$ vim eng/Home/Architecture.xml
$ gfs status
$ gfs commit --dry-run
$ gfs commit
```

`gfs auth list` shows stored identities (tokens stored by `alogin` are used too), `gfs auth rm <email>` and
`gfs auth clear` delete gfs's own tokens. `GFS_CONFLUENCE_EMAIL` and `GFS_CONFLUENCE_TOKEN` override
everything ([credentials design](docs/superpowers/specs/2026-09-24-gfs-credentials-design.md)).


## Design

Earlier versions of this README sketched three shapes for the CLI. The one gfs follows is the git-shaped one:
the file is the interface, `commit` is the single trigger, and the intent lives in the file only when a file change cannot express it.

In one sentence: **a file change means "make the remote look like my working tree"; anything more than that is written explicitly into the file's envelope; anything irreversible is gated by policy.**

- **Every file is XML.** The `<gfs>` envelope carries sync state (identity, remote version, errors); `<content>` holds the resource in the service's native format. No Markdown conversion in either direction.
- **Implicit actions from the diff.** A new file is `create`, a changed element is `update`, a removed file or sub-resource is `delete`, a moved file is `move`. Adding a comment or a worklog is inserting one element.
- **Explicit verbs only where the change is ambiguous.** A new mail in `drafts/` is stored, never sent. Sending is a one-line edit to the envelope, `action="send"`. `gfs actions` lists the verbs a remote understands.
- **Policy, not trust.** `.gfs/config` sets each verb to `allow`, `ask` or `deny`. `send`, `delete` and `publish` default to `ask`, and `ask` without a terminal is `deny`. `gfs commit --allow send` is a distinct command shape, so an agent's permission system can allow `gfs commit` and still block `--allow`.
- **Nothing is reserved in the tree.** No `outbox/` folder that could clash with a service's own folder; gfs keeps its state in `.gfs/` (`config`, `base/`, `index`, `log`).
- **No daemon, no FUSE mount.** Everything happens in `clone`, `pull` and `commit`, so there is always a review step before anything fires.

The other two shapes were dropped. Service-specific verbs (`gfs jira transition ...`) meant learning a per-service API again, the MCP problem in small; a Jira status change is just an edit of `<status>`. A filesystem mount had no dry-run and reported errors asynchronously.

The full design is in [`docs/superpowers/specs/2026-09-23-gfs-cli-design.md`](docs/superpowers/specs/2026-09-23-gfs-cli-design.md); [`example/`](example/) shows how every adapter's files are meant to look, including the mail send-vs-store case.


## Status

Implemented: the shared core (XML file model, canonical printer, status/diff/commit/pull/resolve/log/actions,
three-way merge, policy) and one adapter, **Confluence Cloud**, with attachments listed in every page and
downloaded on demand ([attachments design](docs/superpowers/specs/2026-09-23-gfs-attachments-design.md)).

Next: **Jira** ([design](docs/superpowers/specs/2026-09-29-jira-adapter-design.md)). Mail, Slack, DNS and X exist as examples in [`example/`](example/).


## Background

AI coding agents — think Claude Code, Codex, Claw Code — changed the way we do programming.

Before we had coding agents, [MCP](https://en.wikipedia.org/wiki/Model_Context_Protocol) was created.

This way, LLM chatbots could communicate with any service using MCP.

Connect tools you are using (if they support MCP) to a chatbot and interact with them via the chatbot.
The idea is great.

For example, connect Gmail via MCP and send an email from the chat interface.

Not only chatbots can connect with MCP. Coding agents can also connect with tools via MCP.

There is a but.

Coding agents work well with files.
They can search, create, delete, or update a huge number of files instantly.

The single reason coding agents exist is to do programming.
And programming is mostly about file reads and writes.

MCP is a second-class citizen.

The problems with MCP I had so far:
Connecting MCP with a coding agent can be problematic (or maybe it is just me 🤷).
Calling MCP sometimes fails for unknown reasons. MCP calls usually take time (whereas working with files is instant).
It is not always clear what the result of calling MCP is.


## What is GitFS
What if we could represent net services (web/network tools or services, SaaS, IaaS, FaaS) as files?

Examples of net services:
* Email, Slack — communication services;
* Jira, Confluence — project management;
* X (Twitter), LinkedIn, Facebook — social media;
* Salesforce, Zoho (CRM);
* WordPress (CMS);
* Cloudflare (DNS);
* AWS, OVH, Azure (Infrastructure);
* Many, many more...

What if we could send an email just by file creation:

```text
to: bob@example.com
cc: alice@example.com
subject: Re: Diffie–Hellman key exchange
---
Hello Bob,
... email content ... 

Thanks,
Carol
```

---
What if we could send a Slack message by appending text to a file that represents a Slack channel?

Imagine someone asks a question on Slack about implementation details of a new feature that was released.

An AI agent reads the Slack thread to understand the problem,
searches related email communication and Confluence,
searches related code diffs in the Git repo.

All of those data sources are only files.

After the review is completed and the answer is appended by the coding agent to the file — it will appear in the Slack chat.

---
What if we could download a Jira project, search it with grep, add comments or time log entries by editing files that represent issues?
A coding agent could send/add comments about the progress of a task by editing the file.

---
Would you like to update DNS?
Edit the zone file.

---
What if we could write a post on social media by creating a file?
Dead internet theory at its finest.


## Comparison with git

GitFS borrows the mental model of git and the command vocabulary, but the remote is a net service instead of a git server.


| git                                     | gfs                                                    | Difference                                                                                                                             |
|-----------------------------------------|--------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------|
| `git clone <repo-url> <dir>`            | `gfs clone <service-url> <dir>`                        | `git` downloads a repo. `gfs` connects to a net service (IMAP, Jira, Slack, …) and materializes its resources as files in `<dir>`.     |
| `git status`                            | `gfs status`                                           | Both show locally modified, new, or deleted files. In `gfs`, those changes represent pending actions on the remote service.            |
| `git commit`                            | `gfs commit`                                           | `git` records a snapshot locally. `gfs commit` performs side effects on the remote service — sending emails, creating Jira issues, etc.|
| `git pull`                              | `gfs pull`                                             | `git` fetches and merges remote commits. `gfs pull` re-syncs files with the current state of the net service (new emails, moves, …).  |
| `git push`                              | — (folded into `gfs commit`)                           | In `gfs`, commit already talks to the remote, so there is no separate push step.                                                        |
| tracks a branch history (DAG)           | tracks the current remote state                        | `gfs` is not a version control system — there is no history graph, just a local mirror of what the service has now.                    |
| remote is content-addressed, immutable  | remote is a live mutating service                      | A Jira issue can change under you; an IMAP folder can be rearranged. `gfs pull` reconciles those mutations into the working tree.      |
