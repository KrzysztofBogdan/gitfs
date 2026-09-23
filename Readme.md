(Warning: This is written by human but added em dashes so you will never be sure).

# Background

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


# What is GitFS
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


# Current repo state

The CLI is implemented in Go and follows `docs/superpowers/specs/2026-09-23-gfs-cli-design.md`.
Implemented: the shared core (XML file model, canonical printer, status/diff/commit/pull/resolve/log/actions,
three-way merge, policy) and one adapter, **Confluence Cloud**. Attachments are listed in every page and
downloaded on demand with `gfs get` (`docs/superpowers/specs/2026-09-23-gfs-attachments-design.md`).

```shell
export GFS_CONFLUENCE_EMAIL=me@example.com GFS_CONFLUENCE_TOKEN=<atlassian api token>
gfs clone confluence://acme.atlassian.net/ENG confluence
cd confluence
gfs get eng/Home/Runbooks.xml      # download a page's attachments into eng/Home/Runbooks.files/
vim eng/Home/Architecture.xml
gfs status
gfs commit --dry-run
gfs commit
```

See `example/` for how every adapter's files are meant to look.


# Some thoughts 
I like the idea that commit will either edit or create a resource. But it comes with a few problems.

Email (SMTP/IMAP) is an interesting case that does not work very well with
this approach (at least I did not find a good approach).
Mostly because IMAP is about managing emails and SMTP about sending emails.
They can be used separately. You can put an email in the /sent folder but not send it to anyone.
Or you could send an email but not store it in the /sent folder.


So right now, after a successful send action, it should be stored in the sent folder.
File (gfs commit) creation/update may alter the file.
The email provider might append a footer during send, so the final sent email will look different than the one we committed.

I thought putting an email in the /draft folder and `gfs commit` would send the email, but then
how do we represent putting an email in the /draft folder (without sending)?

Alternatively, a new command could be added. If an email is in the /draft folder (committed or not), for example:

`gfs create /draft/new-email.md` # some general method (create=send)

`gfs command smtp create /draft/new-email.md` # or maybe every integration could have special commands?

`gfs smtp:create /draft/new-email.md` # alternative

Another alternative would be the creation of a file in the existing sent folder `/sent/some-email.md` — and commit — but the problem is it is hard to differentiate if we want to move the email to the /sent folder without sending,
or we want to send it and move to (keep in) the /sent folder.

Creation is tricky.
Let's take Jira for example.

```shell
gfs clone jira://instance-url/ABC gfs/jira/instance-name/abc # ABC is project-key

tree gfs/jira/instance-name/abc
```

output:
```
.
├── ABC-118561 Summary 1.md
├── ABC-118562 Summary 2.md
└── ABC-118563 Summary 3.md
```

Issues are flat, not like emails that have folders.
How do we create an issue?

Any Markdown file: `Summary 4.md` will get a key on commit and the file will be renamed to `ABC-118564 Summary 4.md`?
Will we always require a special folder `/outbox`, and any file that is created there will represent new resource creation?
What if a service that has folders (nesting) has outbox already taken?
Even IMAP (email) could have an outbox folder that has some emails in it.


# Comparison with git

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


# CLI ideas (spike)

Three shapes the CLI could take. They differ in one thing: where the *intent* lives
(send vs. store, create vs. move). Each is shown on the same two scenarios: send an email, create a Jira issue.

## Idea 1: Git-shaped, intent lives in the file

Keep the git vocabulary exactly (`clone`, `status`, `diff`, `add`, `commit`, `pull`, `log`).
No special folders. What `commit` should *do* with a file is written in the file itself, as frontmatter.
Files without an action are just stored/moved; files with an action are executed, then the action key is removed.

```shell
gfs clone imap+smtp://kbogdan@dwa.ovh mail/
cd mail

# just a draft, nothing will be sent
cat > drafts/re-dh.md <<'MD'
to: bob@example.com
subject: Re: Diffie–Hellman key exchange
---
Hello Bob, ...
MD
gfs commit                 # stores in IMAP /Drafts, sends nothing

# now send it: add an action, commit
sed -i '1i action: send' drafts/re-dh.md
gfs diff                   # shows the pending action, not only the text diff:
#   drafts/re-dh.md   action: send  -> will be sent via smtp, then moved to sent/
gfs commit --dry-run       # same as diff, but resolves everything the remote would do
gfs commit                 # sends, file lands in sent/re-dh.md, action key gone
```

```shell
gfs clone jira://instance/ABC jira/abc
cat > jira/abc/Summary 4.md <<'MD'
action: create
type: Bug
---
Steps to reproduce ...
MD
gfs commit                 # file renamed to "ABC-118564 Summary 4.md", action key removed
gfs log                    # what commits did on the remote (sent, created ABC-118564, ...)
```

Service-specific actions are just values: `action: send`, `action: create`, `action: transition/Done`, `action: publish`.
`gfs actions` lists what the current service understands.

Pros: pure "edit a file" model, an agent needs zero CLI knowledge beyond `commit`.
Move-vs-send ambiguity is gone because a move never has an action key.
Cons: intent mixed with content, and a stale `action:` line committed by mistake fires a side effect.
`--dry-run` is the safety net, so it must be first-class.

## Idea 2: Verb-shaped, intent lives in the command

Drop `commit` as the universal side-effect trigger. Files are the *payload*; the command is the *operation*.
Generic CRUD verbs work everywhere, service-specific verbs live under the service name.
`status` and `pull` stay for sync, plain edits + `gfs update` cover the "edit an existing resource" case.

```shell
gfs clone imap+smtp://kbogdan@dwa.ovh mail/
cd mail

vim drafts/re-dh.md
gfs create drafts/re-dh.md              # stores in /Drafts (the folder decides the IMAP target)
gfs smtp send drafts/re-dh.md           # sends; result file moves to sent/
gfs mv sent/re-dh.md archive/2026/      # a move is always just a move
gfs rm spam/*.md
```

```shell
gfs clone jira://instance/ABC jira/abc
vim "jira/abc/Summary 4.md"
gfs create "jira/abc/Summary 4.md"      # -> ABC-118564 Summary 4.md
vim "jira/abc/ABC-118564 Summary 4.md"  # edit description
gfs update jira/abc/ABC-118564*         # or: gfs update .  (everything modified per status)
gfs jira transition ABC-118564 --to "In Progress"
gfs jira worklog ABC-118564 2h "reviewing PR"
gfs jira --help                         # every service exposes its verbs here
```

Pros: zero ambiguity, discoverable via `--help`, easy to grant/deny per verb (an agent may `update` but never `send`).
Cons: it is no longer "just files"; the agent has to learn per-service verbs, which is the MCP problem again in small.
Also two ways to edit (`update` vs. a hypothetical `commit`) must not coexist.

## Idea 3: Filesystem-shaped, intent lives in the directory

No commands after `mount`. Every operation is a filesystem operation; the CLI is only a daemon
plus a control directory. This is the most agent-friendly shape, because agents already know `mv`, `cat`, `grep`.

```shell
gfs mount imap+smtp://kbogdan@dwa.ovh ~/gfs/mail    # FUSE, or a watcher that applies on save
tree -a ~/gfs/mail
# .gfs/
#   log          # append-only: what happened, one line per action
#   errors/      # one file per failed action, with the original payload
#   pull         # touch it to force a re-sync (daemon also syncs periodically)
# inbox/  drafts/  sent/  archive/
# outbox/        # the ONE reserved dir per service: create/execute happens here
```

```shell
cp draft.md ~/gfs/mail/drafts/      # stored as a draft, nothing sent
mv ~/gfs/mail/drafts/draft.md ~/gfs/mail/outbox/   # sent; daemon moves it to sent/ (or errors/)
tail -f ~/gfs/mail/.gfs/log
# 12:01:03 send    outbox/draft.md -> sent/draft.md  (message-id <...>)
```

```shell
gfs mount jira://instance/ABC ~/gfs/jira/abc
echo "..." > ~/gfs/jira/abc/outbox/Summary 4.md      # appears as "ABC-118564 Summary 4.md" once created
echo "- 2h reviewing PR" >> ~/gfs/jira/abc/ABC-118564*/worklog.md   # sub-files for sub-resources
mv ~/gfs/jira/abc/ABC-118564* ~/gfs/jira/abc/.gfs/transition/Done/  # state changes as moves
```

The reserved-name clash ("what if IMAP already has an outbox") is solved by namespacing: the reserved
directory is `.gfs/outbox/` (or `_outbox/`), never a plain name the service could own.

Pros: nothing to learn, works from any language, shell, or agent; observable via `log`.
Cons: no dry-run and no "review before it fires", so it needs a `.gfs/hold` mode (queue, apply on `touch .gfs/go`)
which quietly reinvents `commit`. Errors are asynchronous, so the agent must read `errors/` to know it failed.

## Where I lean

Idea 1 for the default (`commit` stays the single trigger, `--dry-run` shows the plan),
with the reserved `.gfs/` namespace from idea 3 for logs/errors,
and idea 2's `gfs <service> <verb>` as an escape hatch for actions that do not map to a file edit at all
(transition, worklog, webhook replay). The frontmatter `action:` key is what resolves the send-vs-move problem.
