# DNS zones design: OVH and ClouDNS

Status: design approved in conversation 2026-09-30; field names marked
*(spike)* are confirmed against the real APIs before the plan is written.

## 1. Purpose

Mirror the DNS zones of an OVH or ClouDNS account as XML files, one file
per zone, and write edits back with `gfs commit`. Each provider keeps its
own model: nothing is translated to a common format, and every provider
feature in scope (GeoDNS, failover, web redirects, DynHost, mail
forwards) survives a round trip exactly.

The same work adds a guided `gfs auth login <url>` for every adapter,
Atlassian included.

### Goals

- `gfs clone ovh://eu` and `gfs clone cloudns://sub-1234`: every zone the
  credentials see, narrowed by `?zones=` / `?exclude=`.
- One file per zone holding SOA, DNSSEC state, records and the provider's
  extras, each element carrying its provider id.
- Per-element merge on pull; per-element actions on commit.
- Every DNS write asks (policy class `dns`); `gfs commit --force` answers
  yes to all asks of the run.
- `gfs auth login` guides the user through getting credentials, for
  `ovh://`, `cloudns://`, `jira://` and `confluence://`.
- The OVH adapter is built so other OVH services (mail, VPS, cloud,
  OKMS) can be added later as sibling folders of the same clone.

### Non-goals

- Other OVH services (each gets its own spec later).
- A provider-neutral zone format or copying zones between providers.
- Creating or deleting zones (domain ordering, billing).
- Zone transfers, secondary-zone master settings, ClouDNS cloud domains.
- Failover check history, DNS query statistics.
- Atlassian scoped API tokens (they need the api.atlassian.com gateway).

## 2. Packages

| Package | Contents |
|---|---|
| `internal/adapter/ovh` | shared OVH layer (`client.go`: signing, time sync, retry, rate limit, concurrency; `auth.go`: consumer-key flow) and the DNS service (`dns*.go`) |
| `internal/adapter/ovh/ovhtest` | fake OVH API |
| `internal/adapter/cloudns` | ClouDNS client and adapter |
| `internal/adapter/cloudns/cloudnstest` | fake ClouDNS API |
| `internal/adapter/dnsx` | shared DNS helpers: value checks per record type, name handling (`@`, relative names), record ordering |
| `internal/cli/auth.go` | `gfs auth login` |
| `internal/adapter` | new optional interface `Loginer` (§8) |

The OVH adapter routes a path to a *service* by its first folders
(`domain/zone/` → DNS). A service provides: list, fetch, schema for its
root element, check and apply. DNS is the only service in this spec.

## 3. Remote URLs and selection

```
ovh://eu            ovh://ca            ovh://us
cloudns://1234      cloudns://sub-1234
```

- OVH host names the API endpoint: `eu` → `https://eu.api.ovh.com/1.0`,
  `ca` → `https://ca.api.ovh.com/1.0`, `us` → `https://api.us.ovhcloud.com/1.0`.
- ClouDNS host is the API user: a bare number is `auth-id`, `sub-<n>` is
  `sub-auth-id`. `sub-auth-user` is not supported.
- Parameters (both): `zones=a.com,b.pl` (only these), `exclude=c.com`.
  OVH also accepts `services=dns` (default and only value now).
  Unknown parameters are refused. Normalized form keeps known parameters
  in fixed order; zone names lower-case, no trailing dot.
- Default clone directory: `ovh-eu`, `cloudns-sub-1234`.
- A named zone that is not in the account fails the clone:
  `zone x.com not found or not visible`.

### 3.1 Credentials

Keyring service `gfs`, user `ovh:<endpoint>` / `cloudns:<host>`, value
JSON: OVH `{"appKey","appSecret","consumerKey"}`, ClouDNS `{"password"}`.
Environment overrides for CI: `GFS_OVH_APP_KEY`, `GFS_OVH_APP_SECRET`,
`GFS_OVH_CONSUMER_KEY`, `GFS_CLOUDNS_PASSWORD`. Missing credentials:
`no credentials for ovh://eu: run gfs auth login ovh://eu`.
Secrets never appear in files, logs, errors or `%v` output.

## 4. Layout

```
ovh-eu/
  .gfs/
  domain/zone/example.com.xml
cloudns-sub-1234/
  .gfs/
  .geodns.xml
  zone/example.com.xml
```

`.geodns.xml` (ClouDNS, read-only) lists the GeoDNS locations the plan
allows: `<location code="EU">Europe</location>`… Written on clone,
refreshed on `pull --full` or when missing; a local edit is refused on
commit. It exists only when the account has GeoDNS.

## 5. File format

Common rules:

- Root `<zone name="example.com">`.
- Record names are relative to the zone; `@` is the apex. Names are
  lower-case; wildcards as `*` / `*.dev`.
- Values are element text, unquoted (TXT included; long TXT values are
  one string, the provider splits them).
- Elements with an `id` exist remotely; an element without `id` is new.
- Canonical order: `soa`, `dnssec`, then records sorted by name (apex
  first, then by labels right to left), type, id; then the provider's
  extras, each kind sorted by name, id. Attributes in schema order.
- Every attribute is the provider's parameter name, lower-case with `-`.

### 5.1 OVH

```xml
<zone name="example.com">
  <soa ttl="3600" refresh="86400" retry="3600" expire="3600000" minimum="60"/>
  <dnssec>enabled</dnssec>
  <record id="5102271" name="@"   type="A"     ttl="3600">203.0.113.10</record>
  <record id="5102272" name="@"   type="MX"    ttl="3600">10 mx1.mail.ovh.net.</record>
  <record id="5102275" name="www" type="CNAME">example.com.</record>
  <redirect id="881" name="old" type="visiblePermanent" title="t" keywords="k" description="d">https://example.com/new</redirect>
  <dynhost id="42" name="home">198.51.100.5</dynhost>
  <dynhost-login login="example.com-home" name="home"/>
</zone>
```

- `<record>`: OVH `fieldType` → `type`, `subDomain` → `name` (`""` ↔ `@`),
  `target` → text, `ttl` (omitted when 0 = zone default). MX priority,
  SRV priority/weight/port etc. stay inside `target`, as OVH stores them.
- `<soa>`: OVH SOA fields that `PUT /soa` accepts *(spike)*; `server` and
  `email` as read-only attributes if OVH returns them.
- `<dnssec>`: `enabled` / `disabled`; absent when OVH reports the zone
  cannot use DNSSEC.
- `<redirect>`: `/redirection`: `subDomain`, `target`, `type`
  (`visible`, `visiblePermanent`, `invisible`), `title`, `keywords`,
  `description` *(spike)*.
- `<dynhost>`: `/dynHost/record`: `subDomain`, `ip`.
- `<dynhost-login>`: `/dynHost/login`: `login` (identifies it, no `id`),
  `subDomain` → `name`. The password is never read or written to files;
  creating a login asks for it at commit (§7.4).

### 5.2 ClouDNS

```xml
<zone name="example.com" type="master" active="true">
  <soa primary="ns1.cloudns.net" admin="support@cloudns.net" refresh="7200" retry="1800" expire="1209600" ttl="3600"/>
  <dnssec>enabled</dnssec>
  <record id="11" name="@"   type="MX" ttl="3600" priority="10">mx1.example.com</record>
  <record id="12" name="api" type="A"  ttl="60" geo="EU">203.0.113.20
    <failover check="http" host="api.example.com" path="/health" region="eu" period="60">
      <backup>198.51.100.7</backup>
    </failover>
  </record>
  <record id="14" name="api" type="A"  ttl="60">192.0.2.1</record>
  <record id="15" name="_sip._tcp" type="SRV" ttl="3600" priority="0" weight="5" port="5060">sip.example.com</record>
  <record id="20" name="go"  type="WR" ttl="3600" redirect-type="301">https://example.com/landing</record>
  <record id="21" name="@"   type="CAA" ttl="3600" caa-flag="0" caa-type="issue">letsencrypt.org</record>
  <record id="22" name="old" type="A"  ttl="3600" status="0">192.0.2.9</record>
  <mail-forward id="7" box="info" destination="me@gmail.com"/>
</zone>
```

- Zone attributes: `type` (`master`, `parked`, … read-only), `active`.
- `<record>`: `type`, `host` → `name`, `record` → text, `ttl`, and the
  type-specific parameters of `add-record` as attributes: `priority`,
  `weight`, `port`; WR `redirect-type`, `frame`, `frame-title`,
  `frame-keywords`, `frame-description`, `frame-favicon`, `mobile-meta`,
  `save-path`; CAA `caa-flag`, `caa-type` (text is `caa_value`); TLSA
  `tlsa-usage`, `tlsa-selector`, `tlsa-matching-type`; SSHFP
  `algorithm`, `fp-type`; DS `key-tag`, `algorithm`, `digest-type`;
  NAPTR `order`, `pref`, `flag`, `params`, `regexp`, `replace`; CERT
  `cert-type`, `cert-key-tag`, `cert-algorithm`; SMIMEA `smimea-usage`,
  `smimea-selector`, `smimea-matching-type`; RP `mail`, `txt`; HINFO
  `cpu`, `os`; LOC `lat-deg` … `v-precision`; HTTPS/SVCB `parameters`.
  Exact list and which of them the list API returns *(spike)*.
- `status="0"` marks an inactive record (default active, omitted).
- `geo`: GeoDNS location code; absent is the default location. Allowed
  on A, AAAA, CNAME, NAPTR, SRV.
- `<failover>` inside the record it watches: check type and its
  parameters, monitoring region, period, notification settings, up/down
  handlers, `<backup>` IPs (1–5) in order *(spike: names)*.
- `<mail-forward>`: `box`, `host` (omitted for the apex), `destination`.
- `<soa>`: `modify-soa` fields.
- `<dnssec>`: as OVH; activation state from the DNSSEC API *(spike)*.

### 5.3 Schemas

RELAX NG per provider (`docs/dns/ovh.rng`, `docs/dns/cloudns.rng`),
shown by `gfs schema ovh` / `gfs schema cloudns`, driving validation and
canonical order like the Jira schemas. `gfs example ovh|cloudns` prints
fragments per record type.

## 6. Pull

### 6.1 OVH

1. `GET /domain/zone` → zone names, filtered by the selection.
2. Per zone `GET /domain/zone/{z}` → `lastUpdate`. It is the zone's
   version in the index. Equal to the indexed version → zone skipped
   (no further calls), unless `--full`.
3. Changed or new zone: `soa`, `dnssec`, `record` ids then each record,
   `redirection` ids then each, `dynHost/record` ids then each,
   `dynHost/login` names then each. Up to 8 requests in flight per
   adapter.
4. If the spike shows `lastUpdate` does not move for redirection or
   DynHost edits, those parts are refetched on every pull for every
   zone, and the version becomes `lastUpdate` plus a hash of them.

### 6.2 ClouDNS

1. `list-zones.json` paged (100 rows) → zones, filtered.
2. Per zone: `records.json` paged (100 rows), `soa-details.json`, DNSSEC
   state, `mail-forwards.json`, `failover-settings.json` for each record
   the list marks as having failover. Version = hash of the canonical
   file content. Unchanged hash → file untouched.
3. `.geodns.xml` from `get-available-geodns-locations` *(spike: name)*.

### 6.3 Both

- The listing is `Full`: a zone gone from the account (and inside the
  selection) is deleted locally; with uncommitted local edits it is a
  conflict (`C`), resolved with `gfs resolve`.
- Changing `zones=`/`exclude=` and pulling adds or removes zone files.
- Merge is per element by id (`record`, `redirect`, `dynhost`,
  `mail-forward`, `dynhost-login` by `login`); `soa`, `dnssec` and zone
  attributes merge as single elements. A remote change to an element the
  user also changed is the only conflict.
- Clone writes each zone as soon as it is read and saves the index on
  error, so `gfs pull` finishes an interrupted clone.
- ClouDNS answers `{"status":"Failed","statusDescription":…}` with HTTP
  200: that is an error carrying the description. `records.json`
  returns an object keyed by id, or `[]` when empty; ids and TTLs are
  strings in lists, numbers in `add-record` answers.

## 7. Commit

### 7.1 Actions

| Element | OVH | ClouDNS |
|---|---|---|
| record create / update / delete | `POST` / `PUT` / `DELETE /domain/zone/{z}/record[/{id}]` | `add-record` / `mod-record` / `delete-record` |
| record type change | delete + create (new id) | delete + create |
| `soa` | `PUT /soa` | `modify-soa` |
| `dnssec` | `POST` / `DELETE /dnssec` | `activate-dnssec` / `deactivate-dnssec` |
| `redirect` | `/redirection` POST/PUT/DELETE | — |
| `dynhost` | `/dynHost/record` POST/PUT/DELETE | — |
| `dynhost-login` | `/dynHost/login` POST (password asked) / PUT / DELETE | — |
| `failover` | — | `failover-activate` / `failover-modify` / `failover-deactivate` |
| `mail-forward` | — | `add-mail-forward` / `modify-mail-forward` / `delete-mail-forward` |
| zone `active` | — | `change-status` |
| read-only attributes, `.geodns.xml` | refused | refused |

ClouDNS `mod-record` takes the full record: gfs sends every attribute
of the local element, not only the changed ones.

### 7.2 Order

Per zone: deletes, then updates, then creates (so an A can be replaced
by a CNAME of the same name in one commit). Zones in path order. OVH:
one `POST /domain/zone/{z}/refresh` per changed zone after its actions;
a failed refresh is reported as its own failed action, and the next
commit that touches the zone (or an explicit re-run) refreshes again.

### 7.3 Checks before sending

`Check` refuses, per action, before anything of that file is sent:

- value syntax per type: A IPv4, AAAA IPv6, CNAME/MX/NS/PTR/ALIAS
  hostnames, SRV/CAA/TLSA/SSHFP/DS attribute presence and ranges;
- CNAME not at `@`, and not alongside other records of the same name;
- ClouDNS TTL in {60, 300, 900, 1800, 3600, 21600, 43200, 86400,
  172800, 259200, 604800, 1209600, 2592000};
- `geo` present in `.geodns.xml` and allowed for the type;
- names inside the zone (no absolute name for another zone);
- edits to read-only attributes.

A refused action fails alone; other actions of the file still run
unless they depend on it (the create half of a type change whose delete
was refused is not sent).

### 7.4 Asks and `--force`

- New policy class `dns`, default `ask`, covering every DNS action (the
  OVH refresh is not asked: it follows the actions already approved). Each
  ask prints a one-line summary:
  `create  zone/example.com.xml  record api A 203.0.113.20 ttl 300 geo EU`.
- DNSSEC toggles, apex NS changes and SOA changes add a warning line
  (e.g. disabling DNSSEC: the registrar's DS record must be removed
  first, or the domain stops resolving).
- New `gfs commit --force`: every `ask` of the run counts as `allow`
  (any adapter). `deny` is never overridden. `--allow dns` still skips
  only DNS asks. `--dry-run` prints the actions and asks nothing.
- `dynhost-login` create prompts for the password (no echo) after its
  ask; with `--force` and no terminal it fails:
  `dynhost-login needs a password: run without --force in a terminal`.

### 7.5 Results and re-read

Each action reports ok / failed / denied, counted in the summary. A type
change whose delete succeeded and create failed reports
`deleted, create failed: …`; the local element stays without `id`, so
the next commit creates it. After the actions, every changed zone is
re-read (§6) and stored: new elements get ids, the index version moves.

## 8. `gfs auth login <url>`

New optional adapter interface:

```go
// Loginer guides the user to credentials for u and stores them.
type Loginer interface {
	Login(ctx context.Context, u *url.URL, io LoginIO) error
}
```

`LoginIO` gives the adapter: print, read a line, read a secret (no
echo), open a URL in the browser (best effort) and the keyring store.

### 8.1 OVH

1. Print how to create an application at
   `https://<endpoint>/createApp`; read application key and secret.
2. `POST /auth/credential` with the access rules
   `GET /domain/zone`, `GET|POST|PUT|DELETE /domain/zone/*`; print the
   rules and the `validationUrl`, try to open it.
3. Poll `GET /auth/currentCredential` with the pending consumer key every
   3 s until `validated`, for at most 10 minutes (Ctrl-C stops).
4. Verify with `GET /domain/zone` (the rules do not reach `/me`); print
   the zone count, store.

Flags: `--validity 30d` (default unlimited), `--paste` (read all three
keys, verify, read the key's rules from `/auth/currentCredential` and
warn when they reach beyond `/domain/zone`, e.g. `/*`). Re-running with
an application already stored reuses it and asks only for a new
consumer key.

### 8.2 ClouDNS

Print where API users are managed (and that a sub-user can be limited to
zones), read the password, verify with `login.json` *(spike)* and
`list-zones.json`, print the zone count, store.

### 8.3 Atlassian (`jira://`, `confluence://`, customer URLs)

Print the steps at `https://id.atlassian.com/manage-profile/security/api-tokens`
("Create API token", not "with scopes"; expiry at most one year), read
the email (default: the site's remembered email, else
`git config user.email`) and the token, verify with `VerifyToken`, store
as `gfs auth set --host` does today. 401 errors from Atlassian adapters
gain `(run gfs auth login <url>)`.

`gfs auth list` and `gfs auth rm <url>` handle the new entries;
`gfs auth set` stays for scripts.

## 9. Errors

- Signature or clock problems (OVH 403 `Invalid signature`, `This call
  has not been granted`): the message plus `run gfs auth login ovh://eu`.
- OVH 429 and ClouDNS rate-limit answers wait and retry with the shared
  retry policy (at most 6 attempts, 5 minutes in total); 5xx and network
  errors retry on reads only.
- ClouDNS `Failed` answers: the `statusDescription`, with the action.
- A zone that fails to read fails the pull with the zone named; zones
  already stored stay stored.

## 10. Testing

### 10.1 Fakes

- `ovhtest`: verifies every signature and timestamp (403 on mismatch);
  zones, records, SOA, DNSSEC, redirections, DynHost records and logins;
  `refresh` recorded; `lastUpdate` moved by writes; consumer-key flow
  (`/auth/credential`, pending then validated `/auth/currentCredential`);
  injected 429 with `Retry-After` and 5xx; request log.
- `cloudnstest`: form-encoded POST API with auth checks; `Failed` with
  HTTP 200; `records.json` keyed by id and `[]` when empty; paging;
  string vs number ids/TTLs; GeoDNS locations; failover; mail forwards;
  SOA; DNSSEC; injected rate-limit answers; request log.

### 10.2 Unit and end-to-end tests

- Round trip per element and record type (API → XML → API body).
- Canonical order; schema validation; checks of §7.3.
- Action order, type change, partial failures, refresh once per zone.
- Pull: OVH skip by `lastUpdate`, ClouDNS hash, per-element merge,
  conflicts, deleted zones, selection changes, interrupted clone.
- Auth: guide text, polling, `--paste` breadth warning, ClouDNS and
  Atlassian flows, keyring naming, env overrides, secrets never printed.
- CLI end-to-end per provider: clone, edit, status, commit with asks,
  `--force`, `--dry-run`, pull.

### 10.3 Real-site checks

Gated by `GFS_OVH_SITE` / `GFS_CLOUDNS_SITE` (read-only: every zone
round-trips unchanged) and `GFS_OVH_SCRATCH` / `GFS_CLOUDNS_SCRATCH`
(a scratch zone: create, update, type change, delete, GeoDNS, failover,
mail forward, redirect, DynHost, refresh; everything created is deleted).

### 10.4 Spike (before the plan, read-only)

Against the user's test accounts, with credentials the user stores and
Claude reads only in memory: exact JSON of every endpoint in §6, the
fields marked *(spike)*, whether `lastUpdate` moves for redirection and
DynHost edits (answered later by the real-site write checks if it needs
a write), and the ClouDNS rate-limit answer text if one occurs.

## 11. Docs

`docs/dns.md` and `gfs help dns`; `docs/start.md` gains the DNS URLs;
`gfs help auth` covers `auth login`; examples in `example/ovh/` and
`example/cloudns/`.
