# Examples

Each folder is a `gfs` working tree cloned from one net service, frozen at an
interesting moment: some files are at rest, some have pending changes, one has
a failed action, one is in conflict. Every folder has a `README.md` with the
shell session that got it there. Nothing here is implemented; this is what the
CLI *should* feel like.

| folder        | remote                          | shows                                                   |
|---------------|---------------------------------|---------------------------------------------------------|
| `mail/`       | `imap+smtp://kbogdan@dwa.ovh`   | store vs. send, the `<gfs>` envelope with `action` and `<errors>`, a failed send, an HTML mail kept as HTML |
| `jira/abc/`   | `jira://instance/ABC`           | create by new file, comments and worklog as child elements, transition by editing `<status>`, pull merging into pending work |
| `slack/`      | `slack://warsawdynamics`        | post by adding a `<message>`, threads, day files        |
| `confluence/` | `confluence://instance/ENG`     | storage format untouched, version lock, a real merge conflict with `<conflict/>` in the envelope |
| `dns/`        | `cloudflare://example.com`      | records as elements, provider flags as attributes       |
| `x/`          | `x://@kbogdan`                  | irreversible create, `--dry-run`                        |

## Conventions used everywhere

### One format: XML, with a `<gfs>` envelope

Every file is one XML document with the same shape:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <mail id="1873" date="2026-09-01T10:14:02+02:00">   <!-- the resource, in the service's own vocabulary -->
      ...
    </mail>
  </content>
</gfs>
```

* **`<gfs>` is the envelope.** It carries sync state and nothing else. At rest
  it is bare: no attributes, `<content>` as its only child.
* **`<content>` holds exactly one resource root** (`<mail>`, `<issue>`,
  `<messages>`, `<page>`, `<zone>`, `<post>`). Nothing inside `<content>` is
  reserved by gfs; adapters own that vocabulary entirely.
* **Elements are what you write.** Fields (`<to>`, `<status>`, `<labels>`),
  bodies (`<body>`), sub-resources (`<comment>`, `<reply>`, `<record>`).
* **Attributes are what the service owns.** Identity, versions, timestamps,
  authors: `id`, `key`, `version`, `created`, `updated`, `author`, `ts`.
  Editing one is a commit error. A sub-resource element *without* its id
  attribute is new.
* **Body content is the service's native format,** declared by `type`:
  `text/plain` (escaped text or CDATA), `text/html` (mail), `text/mrkdwn`
  (Slack), `application/xhtml+xml` (Jira, Confluence storage with `ac:`/`ri:`
  namespaces as children). Nothing is translated to or from Markdown.
* **Lists are repeated elements** (`<cc>` twice, `<label>` twice).
* **Canonical form**: XML declaration on line 1, `<gfs>` on line 2, two-space
  indent, one element per line for element-only content, no whitespace
  changes inside mixed content, deterministic attribute order, CDATA for any
  text containing `<` or `&`. The printer is idempotent, so a `pull` right
  after a `commit` shows no diff.
* **Lenient input.** A new file may be a bare resource root without the
  envelope; commit wraps it on write-back.

### Envelope contents while something is pending

| in `<gfs>`                      | written by | meaning                                                          |
|---------------------------------|------------|------------------------------------------------------------------|
| `action="<verb>"` attribute     | user       | explicit verb, imperative. Needed only where a file change is ambiguous (mail `send`) |
| `<param>="..."` attribute       | user       | parameter of the verb, see `gfs actions`                         |
| `<errors><error action= target= code= at=><msg/></error></errors>` | gfs | last commit failed, one entry per failed action; dropped on the next attempt |
| `<conflict remote-version= by= at= elements= hunks=/>` | gfs | content has merge markers; commit refuses until you remove this element |

Setting the action is a one-line edit: `sed -i 's/^<gfs>/<gfs action="send">/' file.xml`.

### Implicit actions

Without an `action` on the envelope, commit makes the remote look like your working tree:

| working tree change                 | remote action |
|-------------------------------------|---------------|
| new file                            | create        |
| changed element text                | update        |
| new child element without id        | create sub-resource |
| removed child element               | delete sub-resource |
| deleted file                        | delete        |
| renamed / moved file                | move (tree services) or no-op (flat services, filename is derived) |

### Policy

`.gfs/config` classes each verb as `allow`, `ask`, or `deny`. Defaults:
everything `allow` except `send`, `delete`, `publish` which are `ask`. On a
non-TTY `ask` behaves as `deny`. `gfs commit --allow send` overrides for one
run, so an agent's permission system can permit `gfs commit` and deny
`gfs commit --allow`.

### Status letters

`A` new, `M` modified, `D` deleted, `R` renamed, `!` failed last commit,
`C` conflict. Each line also prints the *resolved* remote action.

### Editing tip for agents

Adding a sub-resource is an insert before the closing tag, not an append:

```shell
sed -i 's#</issue>#  <comment>Verified on staging, closing.</comment>\n</issue>#' ABC-118561*.xml
```

`gfs` accepts the un-indented result; canonical form is restored on write-back.
