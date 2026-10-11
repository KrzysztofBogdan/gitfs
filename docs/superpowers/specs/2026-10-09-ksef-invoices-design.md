# KSeF invoices design: read-only mirror of a company's e-invoices

Status: design approved in conversation 2026-10-09; updated to match the build (plan `2026-10-09-ksef-invoices.md`). Date: 2026-10-09.
Adds a read-only `ksef` adapter (`ksef://`) for KSeF 2.0, the Polish
national e-invoicing system, and three engine features: attachments placed
outside `X.files/` (§2.1), attachment bytes delivered by a listing (§2.2)
and read-only adapters (§2.3). Builds on `2026-09-23-gfs-cli-design.md`,
`2026-09-23-gfs-attachments-design.md` and
`2026-09-24-gfs-credentials-design.md`.

API facts come from the Ministry of Finance documentation
(`github.com/CIRFMF/ksef-docs`, `open-api.json`) and the read spike of
2026-10-06 (§10.4). Facts marked *(verify)* are not yet measured; the
real-site checks (§10.3) confirm them before the adapter is called done.

## 1. Purpose

Mirror the invoices of one company (one NIP) as files: the original FA(3)
XML exactly as KSeF stores it, in folders a person can open in a file
manager, select and send to an accountant, plus one gfs metadata file per
invoice that `grep` and agents can read. KSeF invoices are immutable, so
the mirror only grows: a pull adds new invoices and never re-reads old ones.

### Goals

* `gfs clone ksef://ksef.mf.gov.pl/<nip>` mirrors every invoice the
  company issued, received, or appears on as a third party, since
  2026-02-01 (when KSeF became mandatory) or a chosen `since`.
* The original invoice XML is byte-identical to KSeF's copy: its SHA-256
  equals KSeF's `invoiceHash`.
* A month folder (`2026/10/`) holds a folder per role with only original
  invoices, so it can be zipped and sent as it is.
* `gfs pull` adds new invoices with 3 export requests, within KSeF's
  production limits.
* `gfs auth login ksef://…` guides the user to a KSeF token and stores it.

### Non-goals

* Sending invoices, UPO receipts, offline mode, QR codes.
* PDF visualisations: see `2026-10-11-ksef-pdf-design.md`.
* The `SubjectAuthorized` role, PEF and FA_RR forms beyond mirroring their
  XML as they come.
* Managing tokens, permissions or certificates.
* Login by XAdES signature (qualified signature or seal, trusted profile,
  KSeF certificate). The user generates a KSeF token once in the web app
  with their signature; gfs uses the token. Self-signed XAdES exists only
  in test helpers for the TEST environment (§10.3).

## 2. Packages

| package | role |
|---------|------|
| `internal/adapter/ksef` (new) | the adapter: URL, client, auth, export, mapping, login |
| `internal/adapter/ksef/kseftest` (new) | fake KSeF API (§10.1) |
| `internal/adapter` | new optional interfaces `Placer` (§2.1) and `ReadOnly` (§2.3); `Resource.Files` (§2.2) |
| `internal/attach` | `SidecarDir` and its inverse go through a `Placement` value built from the adapter |
| `internal/workdir` | `Scan` takes the placement: with a `Placer`, resources are the files under `ResourceDir()` |
| `internal/changes` | `Compute` classifies files outside `ResourceDir()` through `ResourceOf`; attachment claims use the placement |
| `internal/engine` | `attach.go`, `get.go`, `pull.go`: placement instead of `attach.SidecarDir`; pull writes `Resource.Files` (§2.2) |
| `internal/cli` | registers the adapter; `gfs help ksef` |

### 2.1 Placement of attachments (`Placer`)

Today the bytes of the attachments of `a/X.xml` live in `a/X.files/`, `Scan`
skips `*.files/` folders, and every other file is a resource. An adapter
may now place them elsewhere:

```go
// Placer is implemented by adapters whose attachments live outside X.files/.
type Placer interface {
	// ResourceDir is the top folder holding every resource file: "_gfs".
	ResourceDir() string
	// AttachmentPath is where the attachment called name of the resource
	// file resPath lives.
	AttachmentPath(resPath, name string) string
	// ResourceOf is the inverse: the resource file an attachment path
	// belongs to, or false for a path no attachment can have.
	ResourceOf(attPath string) (resPath string, ok bool)
}
```

* `attach.Placement` wraps either a `Placer` or the default `X.files/`
  rules; every current caller of `attach.SidecarDir`, `attach.ResourceOf`
  and the derived-path code takes a placement instead. Adapters without a
  `Placer` behave exactly as today (their tests do not change).
* With a `Placer`: `Scan` lists resource files under `ResourceDir()` only
  (`*.files` folders there are still skipped). A file outside it is an
  attachment when `ResourceOf` maps it to a resource file that exists,
  otherwise a stray: `status` prints it as
  `!  <path>   invalid: not part of the mirror; gfs does not track it (move it elsewhere)`,
  and `commit` reports it as invalid, like a sidecar folder without its
  resource today. A tracked attachment whose resource file was deleted is
  not a stray.
* Each shared placed folder is listed once per `status`, not once per
  resource; tracking lines are saved once per batch.
* De-duplication within a sidecar (attachments spec §3.2) applies per
  resource as before; KSeF has one attachment per resource.

### 2.2 Attachment bytes delivered by a listing

The attachments spec keeps bytes on demand. A listing may now carry them:

```go
type Resource struct {
	…
	Files map[string][]byte // attachment id -> bytes, written by pull as if fetched
}
```

* When pull stores a resource with `Files`, it writes each attachment's
  bytes to its placed path and adds its tracking line to
  `.gfs/attachments`, the same line `gfs get` would add.
* An attachment element without bytes in `Files` stays not fetched, as
  today. Adapters that never set `Files` are unaffected.
* A placed path occupied by an untracked file is the existing "untracked
  collision" case: reported, nothing overwritten. A tracked file changed
  locally is kept and reported.

### 2.3 Read-only adapters (`ReadOnly`)

```go
// ReadOnly is implemented by adapters whose resources cannot be changed.
type ReadOnly interface{ ReadOnly() string }
```

* `changes.Compute` sets the message as the error of every new, changed,
  moved or deleted resource file, so `status` shows it and `commit` refuses
  the file before any remote call (commit fetches before applying, and a
  read-only service may not support fetching: a refused delete must never
  forget the resource).
* `pull --force` rewrites changed, moved and deleted resource files from
  their base copies, with no remote call: a read-only service's listings
  need not list known resources again. New files have no base and stay.

## 3. Remote URL and selection

```text
ksef://ksef.mf.gov.pl/<nip>[?since=<date>&roles=issued,received,third-party]
ksef://demo.ksef.mf.gov.pl/<nip>          DEMO (pre-production)
ksef://test.ksef.mf.gov.pl/<nip>          TEST
```

| host | API base |
|------|----------|
| `ksef.mf.gov.pl` | `https://api.ksef.mf.gov.pl/v2` |
| `demo.ksef.mf.gov.pl` | `https://api-demo.ksef.mf.gov.pl/v2` |
| `test.ksef.mf.gov.pl` | `https://api-test.ksef.mf.gov.pl/v2` |

| parameter | meaning | default |
|-----------|---------|---------|
| `since` | invoices stored in KSeF on or after: `2026-02-01`, `-90d`, `-2w`, `-6m` (Bitbucket's forms) | `2026-02-01` |
| `roles` | which roles to mirror: `issued`, `received`, `third-party` | all three |

* The NIP must be 10 digits with a valid check digit; any other host,
  path or parameter is refused, listing the accepted forms.
* `since` applies to full listings only (§6.2).
* Normalised form: `ksef://<host>/<nip>` plus non-default parameters in the
  order `since roles`; `roles` in the order above.
* Default folder: `ksef-<nip>` (`ksef-test-<nip>`, `ksef-demo-<nip>` off
  production).

### 3.1 Credentials

* A KSeF token, stored in the keyring (service `gfs`, user
  `ksef:<host>/<nip>`). `GFS_KSEF_TOKEN` overrides it for CI. Missing:
  `no KSeF token for ksef://ksef.mf.gov.pl/<nip>: run gfs auth login ksef://ksef.mf.gov.pl/<nip>`.
* `Identity()` is `NIP <nip>`.
* The KSeF token, access and refresh tokens and AES keys never appear in
  files, logs, errors or `%v` output.

## 4. Layout

```text
ksef-6887603807/
├── 2026/09/third-party/…
├── 2026/10/issued/6887603807-20261006-6CA868C00000-35.xml        original FA(3), exact bytes
├── 2026/10/received/5261040828-20261003-0A12BC340000-7F.xml
└── _gfs/
    ├── 2026/10/issued/6887603807-20261006-6CA868C00000-35.xml    metadata (§5)
    └── 2026/10/received/5261040828-20261003-0A12BC340000-7F.xml
```

* Roles are KSeF subject types: `issued` = `Subject1` (seller),
  `received` = `Subject2` (buyer), `third-party` = `Subject3`.
* `<year>/<month>` is the date inside the KSeF number
  (`<nip>-<YYYYMMDD>-<technical>-<check>`), the day KSeF accepted the
  invoice. It never changes, so files never move. (Amended 2026-10-12: the
  date comes before the role; the first layout was `<role>/<yyyy>/<mm>/`,
  and `gfs pull --full` moves a tree mirrored under it.)
* File name: `<ksef number>.xml` in both trees.
* Resource id: `<role>/<ksef number>`. An invoice on which the company has
  two roles (a self-invoice, a third party that is also the buyer) is two
  resources, one per role folder.
* Path model `Flat`: the path derives from the metadata; nothing moves.
* `Placer` (§2.1): `ResourceDir()` is `_gfs`;
  `AttachmentPath("_gfs/<y>/<m>/<r>/<k>.xml", "<k>.xml")` is
  `<y>/<m>/<r>/<k>.xml`; `ResourceOf` is the inverse for paths of exactly
  that shape with a role folder.

## 5. File format

```xml
<invoice id="received/5261040828-20261003-0A12BC340000-7F" ksef="5261040828-20261003-0A12BC340000-7F" role="received" version="qAdJRK0E1++A1QWRGVeSOC/TAdSIvdeH1zIwsQKAm5I=">
  <number>FA/123/10/2026</number>
  <type>Vat</type>
  <form>FA (3)</form>
  <issued>2026-10-03</issued>
  <invoicing>2026-10-03T08:12:30Z</invoicing>
  <acquired>2026-10-03T08:12:30Z</acquired>
  <stored>2026-10-03T08:12:31Z</stored>
  <mode>online</mode>
  <seller nip="5261040828">ORLEN S.A.</seller>
  <buyer type="nip" id="6887603807">Acme sp. z o.o.</buyer>
  <third-party type="nip" id="…" role="…">…</third-party>
  <amounts currency="PLN" net="43.60" vat="10.03" gross="53.63"/>
  <corrects hash="…"/>
  <original id="fa" name="5261040828-20261003-0A12BC340000-7F.xml" size="3371" sha256="qAdJRK0E1++A1QWRGVeSOC/TAdSIvdeH1zIwsQKAm5I="/>
</invoice>
```

Every field comes from KSeF's `InvoiceMetadata` (export `_metadata.json`):

| element / attribute | `InvoiceMetadata` |
|---|---|
| `@id` | `<role>/<ksefNumber>`: the engine identity (an invoice can be in two roles) |
| `@ksef`, `@version` | `ksefNumber`, `invoiceHash` |
| `number`, `type` | `invoiceNumber`, `invoiceType` (KSeF's value as is: `Vat`, `Kor`, `Zal`, …) |
| `form` | `formCode.systemCode` |
| `issued`, `invoicing`, `acquired`, `stored` | `issueDate`, `invoicingDate`, `acquisitionDate`, `permanentStorageDate`, in UTC |
| `mode` | `invoicingMode`, lower-cased |
| `seller` | `seller.nip`, `seller.name` |
| `buyer` | `buyer.identifier.type` lower-cased (`nip`, `vatue`, `other`, `none`), `.value`, `buyer.name` |
| `third-party` (0..n) | `thirdSubjects[]`: identifier, `role`, name |
| `amounts` | `currency`, `netAmount`, `vatAmount`, `grossAmount`, printed with two decimals |
| `corrects` | `hashOfCorrectedInvoice`, present only for corrections |
| `@self-invoicing="true"`, `@has-attachment="true"` | `isSelfInvoicing`, `hasAttachment`, present only when true |
| `original` | the attachment element (attachments spec §3.1): id `fa`, the original's file name, size and SHA-256 |

* Optional elements are absent when KSeF gives no value. Unknown future
  metadata fields are ignored.
* `version` is the `invoiceHash`. It never changes (§6.4).
* The whole resource and its attachment are read-only (§2.3): an edited,
  new or deleted metadata file is refused with `KSeF invoices are
  read-only`; an edited or new original with `attachments of this resource
  are read-only`. A deleted original is "evicted" like any attachment
  (silent in `status`) and `gfs get` downloads it again.
* The schema (`schema.go`, `relaxng.go`) declares `<invoice>`, every
  attribute service-owned, and `original` as `schema.Attachment`.

## 6. Pull

### 6.1 Exports

Every listing reads invoices through exports, never one by one. For each
selected role:

1. `POST /invoices/exports` with `filters.subjectType`,
   `dateRange{dateType: PermanentStorage, from, [to], restrictToPermanentStorageHwmDate: true}`
   and `encryption`: a fresh AES-256 key and IV, the key encrypted with
   RSA-OAEP-SHA256 under the Ministry's public key whose `usage` holds
   `SymmetricKeyEncryption` (`GET /security/public-key-certificates`, read
   once per session).
2. Poll `GET /invoices/exports/<ref>` (1 s, doubling to 5 s) until status
   200. Status ≥ 400 fails the listing (§9).
3. Download each part from its pre-signed `url` with its `method` (no
   bearer token), check `encryptedPartHash`, decrypt (AES-256-CBC, PKCS#7),
   check `partHash`, concatenate in `ordinalNumber` order, unzip.
4. For each entry of `_metadata.json`: take `<ksefNumber>.xml` from the
   ZIP, check that its SHA-256 equals `invoiceHash`, build the resource
   (§5) with the bytes in `Files["fa"]`.

The roles are exported one after another (KSeF would allow 10 concurrent
exports per context; gfs does not need them).

### 6.2 Windows

* **Full listing** (clone, `pull --full`, a role new to the selection):
  from `since` to now in windows of at most 90 days (KSeF's `dateRange`
  limit is 100 days in UTC or Polish time; 90 leaves room). A window is
  closed (has `to`) only when it ends more than an hour before now; the
  last one omits `to`, so the HWM bounds it and no invoice between the HWM
  and a near-now window end is skipped.
* **Incremental listing:** one window per role from the role's cursor,
  without `to`.
* A package with `isTruncated` (10,000 invoices or 1 GB compressed) is
  followed by another export from its `lastPermanentStorageDate`; otherwise
  the role continues from `permanentStorageHwmDate`.
* Windows overlap at their edges, and an invoice can appear twice; the
  listing de-duplicates by resource id.

### 6.3 Cursor

`Listing.Cursor` is JSON: `{"issued":"<time>","received":"<time>","third-party":"<time>"}`,
each the role's continuation point (§6.2). A role missing from it gets a
full listing. A failed export fails the listing and the engine keeps the
old cursor; invoices from exports already finished are not written
(the listing returns nothing on error).

### 6.4 Immutability

* A listed invoice whose id is already in the index is skipped: not
  rewritten, not compared, its bytes not written. Pull only adds.
* `Listing.Full` is false and `Deleted` empty: KSeF never deletes
  invoices. A role removed from `roles` is put in `FullIDPrefixes`
  (`<role>/`) with no resources, and its files are removed: with the date
  first, a role is no longer one folder.
* `Fetch` returns `adapter.ErrNotFound`: listings always carry full
  content, so the engine never needs it.
* An edited or deleted metadata file is restored from the engine's base
  copy by `gfs pull --force` (§2.3, no KSeF request). A deleted original is
  downloaded again with `gfs get <path>` (an edited one: delete it first):
  `Download` calls `GET /invoices/ksef/<n>` and checks it against the
  `X-Ms-Meta-Hash` header.
* `pull --full` re-runs every window from `since`; with the skip above it
  only adds invoices missing locally (an earlier `since`, a lost cursor).

### 6.5 Rate limits and duration

Production limits per context and IP (`limity/limity-api.md`, 2026-09-14):

| endpoint | req/s | req/min | req/h |
|---|---|---|---|
| `POST /invoices/exports` | 8 | 16 | 20 |
| `GET /invoices/exports/<ref>` | 10 | 60 | 600 |
| `GET /invoices/ksef/<n>` | 8 | 16 | 64 |
| other (auth, keys) | 10 | 30 | 120 |

* A clone since 2026-02-01 costs ⌈days/100⌉ exports per role: 9 on
  2026-10-09. A pull costs 3, so about 6 pulls an hour before KSeF answers
  429. `gfs help ksef` shows this and recommends pulling at most every 15
  minutes (the Ministry's guidance).
* 429 waits for `Retry-After` through the shared retry policy and shows the
  `wait` progress phase with the time left. KSeF lengthens blocks for
  repeated overruns, so requests are never retried before `Retry-After`.
* Invoices appear about 2 minutes after KSeF accepts them (the high water
  mark lag measured in the spike); a pull right after misses them and the
  next pull brings them.
* TEST limits are ten times higher.

## 7. Authentication

Per session (one gfs run):

1. `POST /auth/challenge` → `challenge`, `timestampMs`.
2. `POST /auth/ksef-token` with `challenge`,
   `contextIdentifier {type: Nip, value: <nip>}` and `encryptedToken`:
   `<token>|<timestampMs>` encrypted with RSA-OAEP-SHA256 under the key
   whose `usage` holds `KsefTokenEncryption`, base64.
3. Poll `GET /auth/<ref>` (bearer: the authentication token) until status
   200; ≥ 400 fails with KSeF's description.
4. `POST /auth/token/redeem` → access token (15 minutes) and refresh token
   (7 days).

The client refreshes the access token with `POST /auth/token/refresh`
when less than a minute is left, so long clones keep working. Nothing but
the KSeF token is stored.

### 7.1 `gfs auth login ksef://…`

`Loginer` (DNS spec §8):

1. Print how to generate a token in the KSeF web app *(verify the URL and
   menu path)*: log in with a trusted profile or signature in the
   company's context, generate a token with only the **InvoiceRead**
   permission, copy it.
2. Read the token without echo.
3. Verify: authenticate (§7) and run one metadata query of the last day
   for `Subject1` (`POST /invoices/query/metadata`, one of the 20 an hour).
   A refused token or missing permission stores nothing.
4. Store; print `stored KSeF token for NIP <nip>`.

`--paste` is the same without the guide. `gfs auth list` and
`gfs auth rm <url>` handle the entry.

## 8. Commit

Nothing is ever sent. The adapter is `ReadOnly` (§2.3): the engine refuses
every change before calling the adapter. `Check` and `Apply` refuse every
action too, as a second line. `Verbs()` is empty. `gfs commit` in a clone with no local changes says
`nothing to commit`.

## 9. Errors

| case | message |
|---|---|
| 401, expired or revoked token | KSeF's description + `(run gfs auth login ksef://<host>/<nip>)` |
| 403, token without InvoiceRead | KSeF's message + `(does the KSeF token have the InvoiceRead permission?)` + login hint |
| auth status ≥ 400 | KSeF's `status.description` and `details` |
| 429 | wait `Retry-After` (§6.5): the shared policy waits up to 5 minutes; an export start also waits out up to 65 minutes, three times (the hourly limit of a large clone); longer fails with KSeF's message and `KSeF asks to retry after <n> s` |
| export status ≥ 400 | `export of <role> from <from>: <description>` |
| part hash mismatch, bad padding, bad ZIP | `export of <role>: package part <n> is damaged`; nothing from it is written |
| original hash ≠ `invoiceHash` | `invoice <ksef>: content does not match KSeF's hash`; the listing fails |
| 5xx, network | reads retry by the shared policy (at most 6 attempts, 5 minutes) |

Errors are read in KSeF's default shapes: `ExceptionResponse`
(`exceptionDetailList[]`: description, code, details), `problem+json`
(`title`, `detail`) for 401/403, and the `status` object of a 429. No
`X-Error-Format` header is sent.

## 10. Testing

### 10.1 Fake (`kseftest`)

An `httptest` server with a generated RSA key pair published at
`/security/public-key-certificates`: challenge, ksef-token auth (decrypts
and checks token and timestamp), auth status, redeem, refresh, exports
built from an in-memory invoice list (real ZIP, real AES, parts split at a
configurable size), truncation at a configurable count, `GET
/invoices/ksef/<n>`, injectable 401/403/429/5xx and a damaged part.

### 10.2 Unit and end-to-end tests

* `url_test.go`: hosts, NIP check digit, `since` forms, `roles`,
  normalisation, refused forms.
* `export_test.go`: windows (100-day split, last window open), truncation
  continuation, HWM cursor, de-duplication at window edges, hash checks.
* `xml_test.go`: `InvoiceMetadata` → `<invoice>` for every field of §5,
  absent optionals, a correction, a self-invoice.
* Engine (`internal/engine`, `internal/changes`, fake adapter):
  * `Placer`: scan, claims, strays, `ResourceOf` both ways;
  * `Resource.Files` written and tracked on pull;
  * every existing attachment test passes unchanged without a `Placer`.
* End to end (`internal/cli/ksef_e2e_test.go` against `kseftest`):
  * clone; originals byte-identical to the fake's invoices;
  * a second pull adds only new invoices and sends 3 exports;
  * `status` on an edited or deleted file of each kind;
  * `commit` refusal;
  * `pull --force` restoring metadata; `gfs get` restoring an original;
  * `roles` narrowed: the dropped role's folders removed.

### 10.3 Real-site checks (`realsite_test.go`, build tag as for Jira)

On TEST only, with a random valid NIP each run. A test helper (never the
adapter) ports the spike's self-signed XAdES login: personal certificate
with `serialNumber=TINPL-<nip>`, enveloped XAdES-BES built in exclusive
c14n form, `POST /auth/xades-signature?verifyCertificateChain=false`. It
creates a KSeF token and sends invoices through an online session; the
test then clones with that token, sends one more invoice, waits past the
HWM, pulls, and checks every original against `invoiceHash`. Confirms the
*(verify)* items.

### 10.4 Spike (done 2026-10-06, TEST, read-only path)

XAdES login with a hand-built signature (Go standard library only),
KSeF-token login, one FA(3) invoice sent, then metadata query, single
download and export: all worked in 38 requests. The downloaded and the
exported XML equal the sent bytes (SHA-256 = `invoiceHash`; the download
also returns it as `X-Ms-Meta-Hash`). Access token 15 minutes, refresh
token 7 days. An export with `restrictToPermanentStorageHwmDate` missed an
invoice stored 2 minutes earlier and included it 2 minutes later. No
`ETag` headers. Export parts are pre-signed blob URLs needing no bearer
token.

Real-site check (2026-10-09, TEST, `TestRealTEST`, 256 s): self-signed
XAdES login, KSeF token created and used by the session, two FA(3) invoices
(our own `testdata/invoice-fa3.xml`, valid against the FA(3) XSD) sent; the
first listing returned the first invoice byte for byte, the incremental
listing from its cursor only the second, and `Download` the same bytes.

## 11. Docs

* `gfs help ksef`: URL forms, roles and layout, getting a token, the limits
  arithmetic (§6.5), immutability and restoring files.
* `start.md`: a KSeF section next to the other adapters.
* Attachments spec: a note that `Placer` and `Resource.Files` extend §3.2
  and the "automatic download" non-goal for adapters that need them (done).
