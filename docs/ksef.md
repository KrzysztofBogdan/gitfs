# gfs and KSeF

gfs mirrors the invoices of one company in KSeF 2.0, the Polish national
e-invoicing system: every invoice it issued, received, or appears on as a
third party. Each invoice is three files: the original FA(3) XML exactly as
KSeF stores it, its PDF as the KSeF web app draws it, and a small gfs file
with its metadata. The mirror is read-only and only grows: KSeF invoices
never change. General `gfs` usage:
[start.md](../start.md).

```shell
gfs auth login ksef://ksef.mf.gov.pl/6887603807    # once: a KSeF token
gfs clone ksef://ksef.mf.gov.pl/6887603807         # -> ksef-6887603807/
gfs pull                                           # new invoices since the last pull
```

## Remote URL

```text
ksef://ksef.mf.gov.pl/<nip>[?since=<date>&roles=issued,received,third-party&pdf=no]
ksef://demo.ksef.mf.gov.pl/<nip>        DEMO (pre-production)
ksef://test.ksef.mf.gov.pl/<nip>        TEST
```

| parameter | meaning | default |
|-----------|---------|---------|
| `since` | invoices stored in KSeF on or after: `2026-02-01`, `-90d`, `-2w`, `-6m` | `2026-02-01` (KSeF became mandatory) |
| `roles` | which invoices: `issued` (you are the seller), `received` (the buyer), `third-party` | all three |
| `pdf` | a PDF beside each original: `yes` or `no` | `yes` |

`since` applies to a clone and `gfs pull --full`; a pull continues where the
last one stopped. Removing a role from `roles` removes its folders.

## Layout

```text
ksef-6887603807/
├── issued/2026/10/6887603807-20261006-6CA868C00000-35.xml       original FA(3)
├── issued/2026/10/6887603807-20261006-6CA868C00000-35.pdf       its PDF
├── received/2026/10/5261040828-20261003-0A12BC340000-7F.xml
├── received/2026/10/5261040828-20261003-0A12BC340000-7F.pdf
├── third-party/2026/09/…
└── _gfs/
    ├── issued/2026/10/6887603807-20261006-6CA868C00000-35.xml   metadata
    └── received/2026/10/5261040828-20261003-0A12BC340000-7F.xml
```

* A month folder (`received/2026/10/`) holds only original invoices and
  their PDFs: zip it and send it to your accountant as it is. Each original
  is byte-identical to KSeF's copy; its SHA-256 is the `sha256` in its
  metadata file.
* The month is the day KSeF accepted the invoice, taken from the KSeF
  number (`<nip>-<YYYYMMDD>-…`), so files never move.
* An invoice on which your company has two roles is in both folders.
* Any other file you put in these folders is reported by `gfs status` as
  not part of the mirror.

## Metadata file

```xml
<invoice id="received/5261040828-20261003-0A12BC340000-7F" ksef="5261040828-20261003-0A12BC340000-7F" role="received" version="qAdJRK0E…">
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
  <amounts currency="PLN" net="43.60" vat="10.03" gross="53.63"/>
  <file id="fa" name="5261040828-20261003-0A12BC340000-7F.xml" size="3371" sha256="qAdJRK0E…"/>
  <file id="pdf" name="5261040828-20261003-0A12BC340000-7F.pdf" size="36794" sha256="Hx2cT1fQ…" generator="1.1.40"/>
</invoice>
```

The fields are KSeF's invoice metadata. `<third-party>`, `<corrects hash>`
(a correction), `self-invoicing="true"` and `has-attachment="true"` appear
only when they apply; `<file id="pdf">` only when the PDF was rendered, with
the generator release that rendered it. Times are UTC. `gfs schema ksef` prints the grammar.

## Pull and limits

Every clone and pull reads invoices through KSeF exports: one encrypted
package per role and window of at most 90 days, never one invoice at a
time. On production KSeF allows 20 exports an hour (per company and IP
address):

* a clone since 2026-02-01 takes one export per role per 90 days: about 9
  in October 2026;
* a pull takes 3 (one per role), so about 6 pulls an hour; the Ministry
  asks for at most one every 15 minutes;
* when KSeF answers "too many requests", gfs waits as long as KSeF asks,
  up to about an hour, and shows the wait: a large first clone takes hours
  rather than failing. A longer block stops it with when to retry.

KSeF makes an invoice available to exports about 2 minutes after it
accepts it: an invoice sent a moment ago comes with the next pull.

## PDFs

KSeF's API serves no PDF: the KSeF web app draws one in the browser with
the Ministry of Finance's `ksef-pdf-generator`. gfs carries that program
(release 1.1.40) and runs it itself, with no Node.js, no network and no
KSeF request.

* The PDF is byte for byte the web app's when the web app runs the same
  release and its clock reads the invoice's creation time
  (`DataWytworzeniaFa`). A PDF downloaded from the web app differs in its
  creation date and file ID, and in layout once the Ministry releases a new
  version.
* Rendering takes about half a second to start, then about a second per PDF
  on up to 4 cores, with about 140 MB of memory per core: a clone of 500
  invoices spends about 2 minutes on PDFs.
* An invoice the generator cannot draw is mirrored without a PDF, and gfs
  prints a warning naming it.
* `gfs pull --full` adds PDFs to invoices mirrored before gfs drew them,
  and draws all of them again when a gfs upgrade brings a new generator
  release; a PDF you changed is kept.
* `?pdf=no` turns PDFs off.
* The XML is the invoice; the PDF is a picture of it.

## Read-only

Nothing is ever sent to KSeF. `gfs status` shows edited, new and deleted
files as refused ("KSeF invoices are read-only"), and `gfs commit` refuses
them. To undo local changes:

* `gfs pull --force` restores edited or deleted metadata files from the
  local copy (no KSeF request);
* `gfs get <original>` downloads a deleted or edited original again
  (KSeF allows 64 such downloads an hour);
* `gfs get <pdf>` downloads the original once more and draws the PDF from it.

## Getting a token

`gfs auth login ksef://ksef.mf.gov.pl/<nip>` explains the steps: log in to
the KSeF taxpayer application (linked from https://ksef.podatki.gov.pl)
with a trusted profile, e-signature or seal in the company's context,
generate a token with only the permission to view invoices
(InvoiceRead), and paste it. gfs checks it with one login and one
invoice query, then keeps it in the system keyring as
`ksef:<host>/<nip>`. In CI set `GFS_KSEF_TOKEN` instead.
`gfs auth rm ksef://ksef.mf.gov.pl/<nip>` deletes it.

## Not yet

Sending invoices, UPO receipts.
