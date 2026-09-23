# slack

A channel is a folder, a day is a file, a message is a `<message>` element.
Adding a `<message>` without `ts` to today's file posts it. Posting is
unambiguous for Slack (there is no "store without posting"), so it is
implicit CRUD, no `action` on the envelope.

```shell
$ gfs clone slack://warsawdynamics slack
Joined channels: 4, DMs: 7 ... 30 days of history
$ tree -L 2 slack/channels | head
slack/channels
├── general
│   ├── 2026-09-15.xml
│   ├── 2026-09-16.xml
│   └── 2026-09-17.xml
└── dev
```

## Post

```shell
$ sed -i 's#</messages>#  <message>Deploying 2.4 to staging in 10 minutes.</message>\n</messages>#' channels/general/2026-09-17.xml
$ gfs status
  M  channels/general/2026-09-17.xml      post 1 message to #general
$ gfs commit
post    #general   ok   ts=1758100920.000200
```

Write-back adds `ts` and `author`.

## Reply in a thread

A `<reply>` inside a `<message>` is a thread reply. New reply: `<reply>`
without `ts`. See the pending reply in `channels/general/2026-09-17.xml`.

## Files

A file shared in a message is a `<file>` element inside that `<message>`
(see bob's message in `channels/general/2026-09-17.xml`). Bytes are fetched on
demand into the day's `.files/` folder, prefixed with the message `ts` so
files stay grouped by message:

```shell
$ gfs get channels/general/2026-09-17.xml
  +  channels/general/2026-09-17.files/1758096060.000300-retry-backoff.png   18 KB
```

A new file in `2026-09-17.files/` named `<ts>-<name>` is shared in that
message's thread; without a `ts` prefix it is posted as a new message. Slack
cannot replace a file's bytes, so editing a fetched file is an error; removing
the `<file>` element deletes it (`ask`, your own files only).

## Formatting

Message text is Slack mrkdwn, declared once on the root
(`type="text/mrkdwn"`). `*bold*`, `<@U123>` mentions and `<https://…|label>`
links are stored as Slack sends them. Because `<` is meaningful in mrkdwn,
gfs writes such messages as CDATA.

## Edit and delete your own messages

Edit the text of a `<message>` you authored: update. Remove the element:
delete (policy `ask`). Slack rejects edits to others' messages and gfs
surfaces that as `!` with the API error.

## Pull

New messages are appended in `ts` order; a pending `<message>` of yours
stays last. Tomorrow's file does not exist until pull creates it or you do.
A new file `channels/general/2026-09-18.xml` with one message posts to
#general now (Slack does not schedule; gfs warns if the date is in the future).
