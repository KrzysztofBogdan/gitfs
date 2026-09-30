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
