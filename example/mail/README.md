# mail: IMAP + SMTP

The case that made the design hard: IMAP stores, SMTP sends, and a new file in
`sent/` could mean either. Resolution: a file change is always the safe
storage operation. Sending needs `action="send"` on the envelope.

```shell
$ gfs clone imap+smtp://kbogdan@dwa.ovh mail
Connecting imap://dwa.ovh ... 1 243 messages in 6 folders
$ cd mail && tree -L 1
.
├── archive
├── drafts
├── inbox
├── junk
├── sent
└── trash
```

Files are `<date> <subject>.xml`, folders mirror IMAP folders. Nothing is
reserved; if the provider has an `outbox` folder you get `outbox/` and it
means nothing to gfs.

## Write a draft, store it, then send it

```shell
$ cat > drafts/re-dh.xml <<'XML'      # bare root is fine, commit adds the envelope
<mail>
  <to>bob@example.com</to>
  <cc>alice@example.com</cc>
  <subject>Re: Diffie–Hellman key exchange</subject>
  <body type="text/plain">Hello Bob,

The shared secret is fine, the problem is that we never authenticate g^a.

Thanks,
Carol</body>
</mail>
XML

$ gfs status
On remote imap+smtp://kbogdan@dwa.ovh
  A  drafts/re-dh.xml       store in Drafts

$ gfs commit
store   drafts/re-dh.xml                                    ok
1 action, 0 failed
```

The draft now exists on the server, wrapped in the envelope, and `<mail>`
gained `id` and `date` attributes. Nothing was sent. To attach a file, drop it into the draft's `.files/` folder; the next commit
stores the draft with it (or sends it, if `action="send"` is set):

```shell
$ cp ~/dh-mitm.pdf drafts/re-dh.files/
```

To send, set the action:

```shell
$ sed -i 's/^<gfs>/<gfs action="send">/' drafts/re-dh.xml

$ gfs status
  M  drafts/re-dh.xml       SEND via smtp, then move to sent/        [ask]
        with new attachment re-dh.files/dh-mitm.pdf

$ gfs commit --dry-run
send    drafts/re-dh.xml
        to      bob@example.com          (MX ok)
        cc      alice@example.com        (MX ok)
        from    kbogdan@dwa.ovh
        then    move -> sent/2026-09-17 Re Diffie–Hellman key exchange.xml
1 action would run, 1 needs confirmation (send)

$ gfs commit
send    drafts/re-dh.xml ?  [y/N] y
send    drafts/re-dh.xml -> sent/2026-09-17 Re Diffie–Hellman key exchange.xml   ok  <a1b2@dwa.ovh>
1 action, 0 failed
```

The sent file is rewritten from the server: envelope bare again, `message-id`
attribute added, provider footer appended to the body. `gfs status` is clean.

## A failed send

`drafts/to-typo.xml` shows the other outcome. The `action` stays on the
envelope and gfs adds an `<errors>` element, one `<error>` per failed action,
with the server text as a properly escaped `<msg>`:

```xml
<gfs action="send">
  <errors>
    <error action="send" code="550" at="2026-09-17T09:11:02+02:00">
      <msg>550 5.1.1 &lt;bobb@example.com&gt;: Recipient address rejected</msg>
    </error>
  </errors>
  <content>
```

```shell
$ gfs status
  !  drafts/to-typo.xml     SEND via smtp   failed: 550 5.1.1 Recipient address rejected
```

Fix the address, `gfs commit` again. `<errors>` is dropped on the retry.

## HTML mail stays HTML

`inbox/2026-09-15 Invoice 4411.xml` has `<body type="text/html">` with the
provider's markup as children. gfs does not render it to text; you read or
grep the HTML, and if you ever edit it, it goes back as HTML.

## Attachments, including duplicate names

Attachments are `<attachment>` elements (id = MIME part), bytes are fetched on
demand into `<mail file name>.files/`. Mail allows two attachments with the
same name; the file for each is derived from its name in id order, so it is
stable: the first keeps the name, later ones get ` (2)`, ` (3)`.

```xml
<attachment id="2" name="invoice-4411.pdf" type="application/pdf" size="608"/>
<attachment id="3" name="invoice-4411.pdf" type="application/pdf" size="620"/>
```

```shell
$ gfs get "archive/2026/2026-09-15 Invoice 4411.xml"
  +  archive/2026/2026-09-15 Invoice 4411.files/invoice-4411.pdf       608 B
  +  archive/2026/2026-09-15 Invoice 4411.files/invoice-4411 (2).pdf   620 B
```

The chosen path is recorded in `.gfs/attachments`, so it never shifts. A
received message cannot change on the server, so its attachments are
read-only: editing one shows `!  attachments of received mail are read-only`.
Drafts can gain, change or lose attachments; the adapter re-stores the draft.

## Moves and deletes are just moves and deletes

```shell
$ mv "inbox/2026-09-15 Invoice 4411.xml" archive/2026/
$ rm "junk/2026-09-16 You won.xml"
$ gfs status
  R  inbox/2026-09-15 Invoice 4411.xml -> archive/2026/…    move
  D  junk/2026-09-16 You won.xml                            delete   [ask]
$ gfs commit --allow delete
move    inbox/… -> archive/2026/…    ok
delete  junk/2026-09-16 You won.xml  ok
2 actions, 0 failed
```

## Pull

```shell
$ gfs pull
  +  inbox/2026-09-17 Re Re Diffie–Hellman key exchange.xml    (new)
  ~  inbox/2026-09-12 Standup notes.xml -> archive/2026/…     (moved on server)
```
