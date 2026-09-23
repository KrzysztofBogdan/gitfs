# x: posts

Creating a post is unambiguous (a new file in `posts/` can only mean publish)
but it is public and effectively irreversible. So it is implicit CRUD with
policy `publish = ask`, and `--dry-run` shows exactly what goes out.

```shell
$ gfs clone x://@kbogdan x
$ ls x/posts | tail -2
'2026-09-10 Release 2.4 is out.xml'
'2026-09-17 On MCP and files.xml'
```

## Publish

```shell
$ cat > "posts/2026-09-17 On MCP and files.xml" <<'XML'
<post>
  <reply-to>https://x.com/someone/status/1234</reply-to>
  <body type="text/plain">Coding agents are good at files. MCP is a second-class citizen.
What if every service were a directory?</body>
</post>
XML

$ gfs commit --dry-run
publish posts/2026-09-17 On MCP and files.xml
        as       @kbogdan
        reply-to @someone 1234
        length   108/280
1 action would run, 1 needs confirmation (publish)

$ gfs commit
publish posts/2026-09-17 On MCP and files.xml ?  [y/N] y
publish posts/2026-09-17 On MCP and files.xml   ok   https://x.com/kbogdan/status/1758099000
```

Write-back adds `id`, `url`, `published` attributes and a `<metrics>` element
refreshed on later pulls. Editing a published post's body is a commit error
(no edit on this account tier); deleting the file deletes the post, policy
`ask`.

A thread is one file with `<post>` children in order, each replying to the
previous.
