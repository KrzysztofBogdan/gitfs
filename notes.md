# Idea

Make interaction with internet services a filesytem/git experience.

Do not need support 
- git providers (github, gitlab, bitbucket), 
- storage providers (s3, gdrive, dropbox, onedrive) 
they are build to work with files.
We want to make other services to work with files.

Candidates that map cleanly to the "files + commit" model from the readme:

Communication (files = messages)
- Slack/Discord: channels as dirs, messages as files, drafts + fs commit
- SMS/WhatsApp: thread per contact
- Calendar invites: .ics-style event files, commit = send

Task/project trackers (files = items, toml frontmatter for fields)
- Linear, GitHub Issues, Asana, Todoist, Trello — same shape as the Jira example

Knowledge/notes
- Notion, Obsidian Sync, OneNote, Google Docs — page = markdown file

CRM / contacts
- Salesforce, HubSpot, Google Contacts — contact with history as file

DNS / infra config
- Cloudflare, Route53, Fastly — records as files, commit = apply. Feels natural since these are already declarative.

Query-style services (the touch abc.com pattern)
These fit the "write an empty file as a question, commit, read the answer" model especially well:
- Translation, weather, stock quotes, WHOIS, DNS lookups, LLM chat, search engines, package registries (npm/pypi lookups)

Social / publishing
- Twitter/X, LinkedIn, Bluesky, Mastodon: drafts folder, commit = post
- Substack/Mailchimp: posts as files, commit = sendC

Media libraries
- Spotify/YouTube playlists: playlist = dir, track = file
- Bookmarks (Pinboard, Raindrop), RSS feeds

Monitoring / ops
- PagerDuty incidents, Datadog monitors, Sentry issues — incident as file + commentary log

### Examples

fs clone example@gmail.com example-gmail

cd example-gmail

cat <<EOF >> send.txt
to: John@bigTech.com
cc: alice@smallTech.com
subject: Good day sir

Hello John,
``
I was wondering if we can schedule call next week.


Thanks,
Krzysztof Bogdan
EOF

fs commit send.txt

## Jira: update a ticket

fs clone jira://acme/PROJ acme-proj

cd acme-proj/PROJ-1234

cat description.md
# edit status, assignee, or description
sed -i 's/^status:.*/status: In Progress/' metadata.yaml

cat <<EOF >> comments.md

---
Picking this up today, will have a PR by EOD.
EOF

fs commit metadata.yaml comments.md -m "start PROJ-1234"

## Confluence: update a page

fs clone confluence://acme/ENG eng-space

cd eng-space/runbooks/deploy-pipeline

cat <<EOF >> page.md

## Rollback

If the canary fails health checks, run \`deploy rollback <sha>\` from the release channel.
EOF

fs commit page.md -m "document rollback procedure"

## Domain search: is it taken?

fs clone domains:// domains

cd domains

# ask a question by creating an empty file named after the query,
# commit it, and the response is written back into the same file

# check a specific domain
touch abc.com
fs commit abc.com

cat abc.com
# status: taken
# registrar: MarkMonitor Inc.``
# registered: 1995-03-22
# expires: 2031-03-21

# search all TLDs for a name (no dot -> wildcard search)
touch abc
fs commit abc

cat abc
# taken:
#   abc.com
#   abc.net
#   abc.org
# available:
#   abc.dev
#   abc.io
#   abc.sh
