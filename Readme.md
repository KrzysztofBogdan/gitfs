# Introduction
(Warning: This is written by human but added em dashes so you will never be sure).

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


# Idea
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
I have played with the CLI UX/DX.
The CLI is implemented in Go.

What is ready: IMAP protocol to download emails, SMTP to send emails.

Init, fetch emails:
```shell
gfs clone imap://kbogdan@dwa.ovh gfs/mail/kbogdan_dwa_ovh
```

Emails created in the special folder /outbox (gfs/mail/kbogdan_dwa_ovh/outbox) will be sent during `git commit`.
Emails are plain text files with Markdown with frontmatter, as in the example earlier.

```shell
cd gfs/mail/kbogdan_dwa_ovh
gfs status # show locally modified, new or deleted files 
gfs commit
# This will send emails and move them from /outbox to /send after success
```

Emails are usually immutable, but technically nothing prevents an email provider from changing email content.
For email, `gfs pull` will fetch new emails, delete locally emails deleted on remote, or move emails between folders.

```shell
gfs pull # can be called to fetch new emails or, if emails were rearranged in a folder, update them
```

In the case of other integrated services, pull could update the content of files the same as git pull does.


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