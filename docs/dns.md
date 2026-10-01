# gfs and DNS zones (OVH, ClouDNS)

gfs mirrors the DNS zones of an OVH account or a ClouDNS API user as one
XML file per zone. Each provider keeps its own model: an OVH file looks like
OVH's API, a ClouDNS file like ClouDNS's, GeoDNS and failover included.

```shell
gfs auth login ovh://eu                 # once: a consumer key for DNS zones only
gfs clone ovh://eu                      # every zone the key sees -> ovh-eu/
gfs clone 'cloudns://sub-1234?zones=qa1.pl'
```

## Remotes

| URL | meaning |
|-----|---------|
| `ovh://eu`, `ovh://ca`, `ovh://us` | an OVH account at that API endpoint |
| `cloudns://1234` | ClouDNS API user by `auth-id` |
| `cloudns://sub-1234` | ClouDNS sub-user by `sub-auth-id` (can be limited to some zones) |

Parameters (both): `zones=a.com,b.pl` (only these), `exclude=c.com`.
OVH also takes `services=dns`, the only OVH service gfs supports so far.
Changing them and running `gfs pull` adds or removes zone files.

## Credentials

`gfs auth login <url>` explains where to get credentials, checks them and
stores them in the system keyring as `ovh:<endpoint>` or `cloudns:<user>`.

* **OVH**: you create an application once (key and secret) at
  `https://eu.api.ovh.com/createApp`; gfs then asks OVH for a consumer key
  limited to `/domain/zone` and prints a link to approve it (you pick how
  long it lasts there). `--paste` takes the three keys of an existing token
  instead and warns when they reach beyond DNS zones (for example `/*`).
* **ClouDNS**: the API user's password.

For CI: `GFS_OVH_APP_KEY`, `GFS_OVH_APP_SECRET`, `GFS_OVH_CONSUMER_KEY`,
`GFS_CLOUDNS_PASSWORD`. `gfs auth list` shows entries, `gfs auth rm ovh://eu`
deletes one.

## Layout

```
ovh-eu/domain/zone/example.com.xml
cloudns-sub-1234/zone/example.com.xml
cloudns-sub-1234/.geodns.xml          (GeoDNS locations; read-only)
```

## Files

Common rules:

* Names are relative to the zone; `@` is the zone itself.
* Values are element text, unquoted, TXT included.
* An element with `id` exists on the provider; one without `id` is new.
* Change an element to update it, delete it to delete it, add one without
  `id` to create it.
* Records are sorted by name (apex first, then labels right to left), type
  and id.

### OVH

```xml
<zone name="example.com">
  <soa ttl="3600" refresh="86400" expire="3600000" nx-domain-ttl="60" email="tech.ovh.net." server="dns101.ovh.net." serial="2025101600"/>
  <dnssec>enabled</dnssec>
  <record id="5102271" name="@" type="A">203.0.113.10</record>
  <record id="5102272" name="@" type="MX">10 mx1.mail.ovh.net.</record>
  <record id="5102275" name="www" type="CNAME" ttl="300">example.com.</record>
  <redirect id="5349102139" name="old" type="visiblePermanent" title="Moved">https://example.com/new</redirect>
  <dynhost id="5300344544" name="home" ttl="60">198.51.100.5</dynhost>
  <dynhost-login login="example.com-home" name="home"/>
</zone>
```

* `<record>`: OVH stores MX, SRV and similar parameters inside the value
  (`10 mx1.mail.ovh.net.`). No `ttl` means the zone default.
* `<redirect>` (types `visible`, `visiblePermanent`, `invisible`) and
  `<dynhost>` are backed by records OVH manages (an A record with the same
  id, and a TXT `"1|…"` for a redirect). Those records do not appear as
  `<record>`; edit the redirect or DynHost instead.
* `<dynhost-login>`: a new one needs `suffix` (the login becomes
  `<zone>-<suffix>`) and `name`; gfs asks for its password at commit. The
  password is never stored in a file.
* `<soa>`: `server` and `serial` are read-only.
* `<dnssec>`: `enabled` or `disabled`; `enableInProgress` and
  `disableInProgress` are read-only while OVH works.

### ClouDNS

```xml
<zone name="example.com" type="master" kind="geodns">
  <soa primary="gns1.cloudns.net" admin="support@cloudns.net" refresh="7200" retry="1800" expire="1209600" ttl="3600" serial="2026092802"/>
  <dnssec status="enabled">
    <ds key-tag="12626" algorithm="13" digest-type="2">B156B918…</ds>
  </dnssec>
  <active>true</active>
  <record id="11" name="@" type="MX" ttl="3600" priority="10">mx1.example.com</record>
  <record id="12" name="api" type="A" ttl="60" geo="EUR">203.0.113.20</record>
  <record id="13" name="api" type="A" ttl="60">192.0.2.1</record>
  <record id="15" name="_sip._tcp" type="SRV" ttl="3600" priority="0" weight="5" port="5060">sip.example.com</record>
  <record id="20" name="go" type="WR" ttl="3600" redirect-type="301">https://example.com/landing</record>
  <record id="21" name="@" type="CAA" ttl="3600" caa-flag="0" caa-type="issue">letsencrypt.org</record>
  <failover record="12" check-type="17" host="api.example.com" path="/health" monitoring-region="eu">
    <backup>198.51.100.7</backup>
  </failover>
  <mail-forward id="7" box="info" destination="me@example.com"/>
</zone>
```

* `ttl` is required and must be one ClouDNS allows for the account (60,
  300, 600, 900, 1800, 3600, …).
* Type-specific fields are attributes named after ClouDNS's parameters,
  with `-` for `_` (`caa-flag`, `redirect-type`, `frame-title`, …).
* `geo` is a GeoDNS location code from `.geodns.xml` (`EUR`, `NAM`, `US`,
  …); no `geo` is the default location. Only in GeoDNS zones, on A, AAAA,
  CNAME, NAPTR, SRV and ALIAS. One name may hold a CNAME per location.
* `status="0"` is an inactive record.
* `<failover record="12">` watches record 12 (A, AAAA or CNAME): add the
  record first, commit, then add its failover. `<backup>` elements are the
  backup IPs in order.
* `<dnssec>`: `status` is editable; the `<ds>` records are read-only and
  show what to publish at the registrar.
* `<active>false</active>` stops the zone from being served.
* The zone's `type` and `kind` and the SOA `serial` are read-only.

## Commit

Every DNS change asks, because a DNS mistake breaks mail and websites at
once. Each ask names the change:

```
create domain/zone/example.com.xml  create record api A 203.0.113.20 ttl 300 ? [y/N]
    ⚠ if the registrar publishes a DS record for this domain, remove it first or the domain stops resolving
update domain/zone/example.com.xml  dnssec enabled → disabled ? [y/N]
```

* `gfs commit --dry-run` lists what would run; `--force` answers yes to
  every ask (a policy `deny` still refuses); `--allow dns` lifts only the
  DNS asks. Without a terminal an ask counts as no.
* DNSSEC changes, the zone's own NS records, SOA changes and disabling a
  ClouDNS zone print a warning first.
* Before anything is sent gfs checks values per type (IPv4 for A, IPv6 for
  AAAA, host names, MX/SRV parts), the CNAME rules (none at `@`, a CNAME
  alone at its name), ClouDNS TTLs, record types and GeoDNS codes.
* Per zone, deletes run first, then updates, then creates, so an A record
  can become a CNAME of the same name in one commit. A record's type (and
  an OVH redirect's name) cannot change in place: gfs deletes and creates;
  if the create fails, the record stays in the file without `id` and the
  next commit creates it.
* OVH: one zone refresh after the zone's changes, shown as `refresh`. If it
  fails the changes are saved and reach the name servers with the next
  refresh.

## Pull

* OVH: a zone is re-read when its export or DNSSEC state changed.
* ClouDNS: a zone is re-read when its serial or mail forwards changed.
  A change to failover settings alone shows with the zone's next change or
  on `gfs pull --full`.
* Your uncommitted edits are merged per element by id; editing an element
  that changed on the provider is the only conflict.
* A zone removed from the account (or from `zones=`) is removed locally.

## Not covered

Creating or deleting zones, zone transfers and secondary zones, ClouDNS
cloud domains, failover history, DNS statistics, and other OVH services.
`gfs schema ovh` and `gfs schema cloudns` print RELAX NG grammars for the
files.
