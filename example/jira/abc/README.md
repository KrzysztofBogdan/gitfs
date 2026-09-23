# jira: project ABC

Flat service: one file per issue, filename derived from key and summary.
Everything is CRUD on elements, so no envelope here ever carries an `action`.

```shell
$ gfs clone jira://instance/ABC jira/abc
Fetching project ABC ... 3 issues
$ cd jira/abc && ls
'ABC-118561 Retry loop never backs off.xml'
'ABC-118562 Login page: 500 on empty password.xml'
'ABC-118563 Upgrade Go to 1.27.xml'
```

## Create an issue

Any new file with an `<issue>` root and no `key` attribute.

```shell
$ cat > "Rate limiter drops burst traffic.xml" <<'XML'   # bare root, envelope added on commit
<issue>
  <summary>Rate limiter drops burst traffic</summary>
  <type>Bug</type>
  <priority>High</priority>
  <labels><label>backend</label><label>p1</label></labels>
  <body type="application/xhtml+xml">
    <p>Bursts above 200 rps are dropped instead of queued.</p>
    <p>Reproduce with <code>hey -z 10s -q 300 http://localhost:8080/api</code>.</p>
  </body>
</issue>
XML

$ gfs status
  A  Rate limiter drops burst traffic.xml     create issue (Bug)

$ gfs commit
create  Rate limiter drops burst traffic.xml -> ABC-118564 Rate limiter drops burst traffic.xml   ok
```

The file is renamed and rewritten with `key`, `id`, `created`, `reporter`
attributes and `<status>To Do</status>`. Renaming the file yourself later does
nothing; on a flat service the filename is derived and commit re-derives it.

## Comment, transition, log work: one file, three changes

`ABC-118561 Retry loop never backs off.xml` has a pending comment (the
`<comment>` without `id`), a changed `<status>`, and a new `<worklog>` entry.

```shell
$ sed -i 's#<status>In Progress</status>#<status>Done</status>#' ABC-118561*.xml
$ sed -i 's#</issue>#  <comment>Verified on staging, closing.</comment>\n</issue>#' ABC-118561*.xml

$ gfs status
  M  ABC-118561 Retry loop never backs off.xml
       transition   In Progress -> Done
       comment      + 1 new
       worklog      + 2h

$ gfs commit
update  ABC-118561   transition Done         ok
update  ABC-118561   comment #10044          ok
update  ABC-118561   worklog #4412           ok
1 file, 3 actions, 0 failed
```

Write-back adds `id`, `author`, `created` to the new elements and moves them
to their canonical position.

## Pull merges into pending work

If someone comments while your `<comment>` is still uncommitted, pull inserts
the remote comment (it has an id) and leaves yours (no id). No conflict. The
only conflict is editing a comment body that changed remotely, which pull
marks `C` with markers inside the element.

## Fields

Plain edits to elements. Attributes (`key`, `id`, `created`, `reporter`) are
read-only and commit refuses with the offending line.

```shell
$ sed -i 's#<assignee>.*</assignee>#<assignee>alice</assignee>#' ABC-118563*.xml
$ gfs commit
update  ABC-118563   assignee alice   ok
```
