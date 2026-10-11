# KSeF PDF design: the web app's invoice PDF next to each original

Status: written 2026-10-11 from the PDF spike (§9); the user asked for spec,
plan and inline implementation without review stops. Builds on
`2026-10-09-ksef-invoices-design.md` (the ksef adapter, branch `ksef`).

## 1. Purpose

Put `<ksef>.pdf` next to every `<ksef>.xml` in the mirror, byte-identical to
the PDF the KSeF web app gives for that invoice, so a month folder can be
zipped and sent to an accountant with a readable copy of each invoice.

KSeF's API serves no PDF. The web app renders it in the browser with the
Ministry's open-source generator, `github.com/CIRFMF/ksef-pdf-generator`
(MIT, TypeScript on pdfmake). gfs embeds a build of that generator and runs
it in goja, a JavaScript interpreter written in Go: no Node.js at run time.

### Goals

* Every invoice gfs adds gets `<yyyy>/<mm>/<role>/<ksef>.pdf` beside its
  original, with the same layout and bytes the web app produces when its
  clock reads the chosen render time (§4.3).
* The same original always renders to the same bytes, on any machine, in
  any time zone.
* `gfs get <pdf>` recreates a deleted or edited PDF; `gfs pull --full`
  adds PDFs to invoices mirrored before this feature, and re-renders all of
  them after the embedded generator changes.
* No Node.js, no network and no KSeF request to render.
* `?pdf=no` turns PDFs off for a mirror.

### Non-goals

* PDFs of UPO receipts, or KOD II (the offline-mode certificate QR code):
  the generator draws KOD II only from the issuer's KSeF certificate, which
  a mirror never has.
* Choosing the generator version per invoice, or matching a PDF someone
  downloaded on a given day: gfs embeds one release (§3).
* Rendering in Go. The spike showed a port must copy zlib's deflate,
  fontkit's subsetting and pdfmake's layout exactly, and follow a release
  every two weeks.

## 2. Packages

| package | role |
|---------|------|
| `internal/adapter/ksef/ksefpdf` (new) | embedded generator build, goja runtime with browser shims, render inputs, renderer pool |
| `internal/adapter/ksef` | renders PDFs while listing, serves `pdf` downloads, places `.pdf` files, `pdf` URL parameter |
| `internal/cli` | progress phases `render` and `warn` |

New module dependency: `github.com/dop251/goja`, pinned to commit
`39ec265` (2026-09-26), the newest one whose `go.mod` still says
`go 1.25.0`. It brings `github.com/dlclark/regexp2/v2`,
`github.com/go-sourcemap/sourcemap`, `github.com/google/pprof` and a newer
`golang.org/x/text`.

## 3. The generator build

### 3.1 What is embedded

`internal/adapter/ksef/ksefpdf/assets/`, embedded with `go:embed`:

| file | content |
|------|---------|
| `generator.js` | the UMD bundle of the gfs build (§3.2), about 1.4 MB |
| `Roboto-Regular.ttf`, `Roboto-Medium.ttf`, `Roboto-Italic.ttf`, `Roboto-MediumItalic.ttf` | the fonts the release's pdfmake ships, decoded from its `vfs_fonts.js` |
| `VERSION` | one line: release and commit, e.g. `1.1.40 f59fc4e` |
| `LICENSE` | the generator's MIT licence |
| `THIRD-PARTY.txt` | the licences of the npm packages inside the bundle, and Roboto's Apache 2.0 licence |

The first release embedded is 1.1.40 (commit `f59fc4e`, 2026-09-17), the
latest public one.

### 3.2 The gfs build

`internal/adapter/ksef/ksefpdf/build.sh <commit>` needs git, Node.js 22 and
npm. It is run by a developer to change the embedded release; nothing at
gfs build or run time needs it. It:

1. clones the generator at `<commit>` into a temporary directory and runs
   `npm ci`;
2. renders the golden files (§8.1) with the unchanged upstream source
   through a vitest runner it adds, under `TZ=Europe/Warsaw`, with the
   inputs written in `golden.json` (clock in milliseconds, `nrKSeF`,
   `acDate`, `qrCode`);
3. adds two files and changes the build configuration:
   * `src/gfs-empty-vfs.js` (`export default {}`), which the vite config
     aliases for `pdfmake/build/vfs_fonts` when `GFS=1`;
   * `src/gfs-entry.ts`, the library entry when `GFS=1`: it re-exports
     `./index` and adds `gfsAddFont(name, bytes)`, which stores decoded font
     bytes with `pdfMake.virtualfs.writeFileSync(name, new Uint8Array(bytes))`;
4. builds with `GFS=1 npm run build` and copies `dist/*.umd.cjs` to
   `assets/generator.js`;
5. writes the four TTFs, `VERSION`, `LICENSE` and `THIRD-PARTY.txt`.

The script fails when the vite config no longer has the lines it changes.

Why the gfs build: upstream registers pdfmake's base64 fonts once per
invoice-form module, seven times, and goja spends 7 s decoding them. The
alias makes those registrations empty; Go hands the TTF bytes in once.
Start-up drops to about 0.5 s and the output stays byte-identical (§9).
The change touches only how fonts reach pdfmake, never the layout.

## 4. Rendering

### 4.1 Renderer

A `Renderer` is one goja runtime with the shims (§4.2) and the bundle
loaded and the fonts added. It is not safe for concurrent use. Creating
one takes about 0.5 s and 140 MB; a render takes about 1 s.

```go
type Input struct {
	XML        []byte // the original, as KSeF stores it
	KsefNumber string
	QRBase     string // https://qr.ksef.mf.gov.pl, -test or -demo; "" draws no QR code
}

func Version() string               // "1.1.40 f59fc4e"
func NewRenderer() (*Renderer, error)
func (r *Renderer) Render(in Input) ([]byte, error)
```

`Render` calls the bundle's `generateInvoice(file, additionalData,
'base64')` with:

* `nrKSeF` = the KSeF number;
* `acDate` = the date inside the KSeF number as `dd.mm.yyyy` (the PDF's
  "Data nadania numeru KSeF");
* `qrCode` = the KOD I link (§4.4), when it can be built;

then runs the timer queue until the promise settles and decodes the
base64. A JavaScript exception is an error carrying its message, e.g.
`Unknown XML Version: FA (9)`. A render that runs longer than 60 s plus
200 ms per KB of XML (five times what a fast machine needs) is
interrupted (`Runtime.Interrupt`) and fails; the renderer is then
discarded.

The XML is passed as text: UTF-16 (a byte-order mark, or `<` followed by a
zero byte, or the reverse) is decoded to text by Go, UTF-8 loses a leading
byte-order mark, the way the browser's `FileReader.readAsText` reads it.

### 4.2 Browser shims

goja has no browser. Before the bundle runs, gfs defines:

| name | behaviour |
|------|-----------|
| `self`, `window` | `globalThis` |
| `navigator` | `{userAgent: 'gfs'}` |
| `console` | methods that do nothing (releases before 1.1.39 log i18next debug output) |
| `setTimeout`, `setInterval`, `setImmediate`, `clear*` | a FIFO queue the renderer drains; delays are ignored |
| `TextEncoder` | UTF-8, encoded by Go |
| `TextDecoder` | `utf-8`, `utf-16be`, `utf-16le`, `ascii`, `latin1`, decoded by Go (fontkit reads font names as UTF-16BE) |
| `File`, `FileReader` | one in-memory text; `slice().arrayBuffer()` reports UTF-8, `readAsText` hands the text over on the timer queue |
| `Intl.DateTimeFormat` | `pl-PL` in Europe/Warsaw, by Go: `dd.mm.yyyy`, plus `, HH:MM` and `:SS` when asked |
| `Intl.PluralRules` | Polish and English categories |
| `Intl.getCanonicalLocales` | its argument as an array |
| `Date` | see below |

`Date` is wrapped so that:

* `Date.now()` and `new Date()` return the render time (§4.3);
* the local-time getters (`getFullYear`, `getMonth`, `getDate`, `getDay`,
  `getHours`, `getMinutes`, `getSeconds`, `getMilliseconds`,
  `getTimezoneOffset`) answer in Europe/Warsaw, computed by Go;
* `new Date(y, m, …)` and `Date.parse`/`new Date(s)` of a date-time
  without a zone (`2026-08-10T12:00:00`) read the time as Europe/Warsaw.

goja otherwise answers local time from the process's `time.Local`; gfs never
changes `time.Local`. The web app runs in the user's browser, so its PDFs
use the user's zone; the Ministry's generator is used in Poland, so gfs fixes
Europe/Warsaw.

### 4.3 Render time

The clock is frozen at `Naglowek/DataWytworzeniaFa` of the invoice (the
issuer's creation time, always present in FA(1)–FA(3)), cut to whole
milliseconds; when it is missing or does not parse, at midnight Europe/Warsaw
of the date inside the KSeF number. It fixes the PDF's `CreationDate` and its
`/ID` (an MD5 of the time), so the same XML always gives the same bytes. It
comes from the XML, so `gfs get` can render again from the original alone.

### 4.4 KOD I link

`<QRBase>/invoice/<seller NIP>/<P_1 as DD-MM-YYYY>/<SHA-256 of the XML,
Base64URL without padding>`, where the seller NIP is
`Podmiot1/DaneIdentyfikacyjne/NIP` and `P_1` is `Fa/P_1` (element names
matched without their namespace). The spike rebuilt the link of all 38
real PDFs this way. When the NIP or `P_1` is missing the PDF has no QR
code. `QRBase` comes from the host: `qr.ksef.mf.gov.pl`,
`qr-test.ksef.mf.gov.pl`, `qr-demo.ksef.mf.gov.pl`.

### 4.5 Pool

```go
func NewPool(n int) *Pool
func (p *Pool) Render(ctx context.Context, in Input) ([]byte, error)
func (p *Pool) Close()
```

`Render` is safe for concurrent use. The pool creates renderers on demand,
at most `n`, and reuses idle ones; a renderer whose render failed with an
interrupt is dropped. `ctx` cancels a wait for a free renderer and
interrupts a running render.

## 5. The adapter

### 5.1 Layout and metadata

```text
2026/10/received/5261040828-20261003-0A12BC340000-7F.xml   original
2026/10/received/5261040828-20261003-0A12BC340000-7F.pdf   PDF
_gfs/2026/10/received/5261040828-20261003-0A12BC340000-7F.xml
```

The attachment element of the metadata file is renamed from `original` to
`file`, one per file:

```xml
<file id="fa" name="5261040828-20261003-0A12BC340000-7F.xml" size="3371" sha256="qAdJRK0E…"/>
<file id="pdf" name="5261040828-20261003-0A12BC340000-7F.pdf" size="36794" sha256="Hx2c…" generator="1.1.40"/>
```

`sha256` is base64 like KSeF's hash; `generator` is the release that
rendered the PDF. All attributes are read-only. The engine supports one
attachment element per schema, so both files share it. The branch is not
merged, so no released mirror has `<original>`; a mirror cloned from the
branch changes to `<file>` on its next `gfs pull --full` (§5.4).

### 5.2 Listing

`List` collects every role's invoices as now, then renders one PDF per
distinct KSeF number (an invoice in two roles is rendered once) through a
pool of `min(GOMAXPROCS, 4)` renderers created for this listing and closed
after it. Progress is the phase `render` with `Done/Total` PDFs and the
KSeF number. A failed render leaves that invoice without a `pdf` file and
reports phase `warn` with `PDF of <ksef>: <error>`; the listing goes on.
A cancelled context fails the listing.

`resourceOf(role, inv, pdf)` adds the `pdf` element and `Files["pdf"]`
when `pdf` is not nil.

### 5.3 Downloads and placement

* `Download(id, "pdf")` reads the original from KSeF as `Download(id,
  "fa")` does (one of the 64 single-invoice downloads an hour), checks
  its hash, renders it with a renderer of its own and writes the PDF.
* `ResourceOf` accepts `<yyyy>/<mm>/<role>/<name>.pdf` as well as `.xml`,
  both owned by `_gfs/<yyyy>/<mm>/<role>/<name>.xml`.
* Read-only rules apply to PDFs as to originals: an edited PDF is refused
  by `gfs commit`; a removed one is, like a removed original, merely not
  downloaded; `gfs get <pdf>` restores either.

### 5.4 Old invoices and new releases

A plain pull renders only the invoices it adds; KSeF invoices never change,
so it never touches older ones. `gfs pull --full` exports everything since
`since` again and renders every PDF:

* an invoice whose metadata comes out identical (same generator, same
  bytes) is left alone;
* one without a PDF (mirrored before this feature, or a failed render) or
  with a PDF from another release gets the new metadata and PDF (`~`);
* a PDF changed locally is kept, with the engine's existing "changed
  locally; kept" line.

### 5.5 URL parameter

`pdf=no` turns rendering off; `pdf=yes` is the default and is dropped from
the canonical URL. Any other value is an error. With `pdf=no`, listings carry
no `pdf` files and `Download(…, "pdf")` returns not found.

## 6. CLI

`internal/cli/progress.go` shows:

* `render`: `Rendering %d/%d PDFs  <item>`;
* `warn`: clears the bar and prints `warning: <item>` on its own line,
  terminal or not.

## 7. Documentation

`docs/ksef.md`: layout with the PDF, the `file` element, `pdf=no`, how
`pull --full` adds and re-renders PDFs, start-up and speed figures, the
embedded release, and that the web app's PDF of the same invoice matches
when downloaded with the same generator release. `docs/start.md`, if it
lists ksef parameters. The ksef spec's non-goal line points here.

## 8. Testing

### 8.1 Golden PDFs

`ksefpdf/testdata/` holds invoices that are not real data and their PDFs as
the unchanged upstream generator renders them in Node.js (§3.2, step 2):

* `fa3.xml`: FA(3) built from the adapter's test invoice, with a delivery
  period `P_6_Od`/`P_6_Do` (formatted with local-time getters) so a wrong
  time zone shows;
* `upstream-example.xml`: the generator repository's `assets/invoice.xml`
  (MIT);
* `golden.json`: for each, the inputs and the expected output file.

`golden.json` is written by hand once (the inputs follow §4.3–4.4) and
read by both sides. `TestGolden` checks that the Go-derived inputs equal
the file's, renders each invoice with the embedded build and compares
bytes, once with `time.Local` as found and once with `time.Local` set to
`America/New_York` (restored after).

### 8.2 Unit tests

* inputs: KOD I link, `acDate`, render time (with the 7-digit fractions
  KSeF XML carries, missing and bad `DataWytworzeniaFa`), UTF-8 with BOM,
  UTF-16LE and BE;
* shims: Warsaw getters around a DST change, local-time parsing,
  `Intl.DateTimeFormat` options, plural rules;
* an unknown form fails with the generator's message; a render that loops
  is interrupted (a test hook shortens the 60 s limit);
* the pool reuses renderers, never exceeds `n`, and honours a cancelled
  context.

### 8.3 Adapter and end to end

* `List` with a real FA(3) invoice gives `Files["pdf"]` and the `pdf`
  element; with the fake server's minimal XML it gives no PDF and one
  `warn` report; `pdf=no` gives no PDF.
* `Download(…, "pdf")` returns the PDF of the downloaded original.
* `ResourceOf` and `AttachmentPath` place `.pdf` files.
* CLI end to end: a clone shows `X.pdf` beside `X.xml`; deleting it shows a
  refused change and `gfs get` brings it back byte-identical; `pull --full`
  over a mirror with a PDF missing adds it.

### 8.4 Real data

The spike's check stays manual: `GFS_KSEF_PDF_DIR=<dir of web-app PDFs>
go test ./internal/adapter/ksef/ksefpdf -run TestDownloadedPDFs` pairs each
`<ksef>.pdf` there with `<ksef>.xml` in `GFS_KSEF_MIRROR`, renders it with
the embedded build and the downloaded PDF's own time (recovered from its
`/ID`, §9; the package-internal `render(in, clockMs)` takes it) and
reports how many match. PDFs downloaded while another
release was live do not match; the test prints the count and fails only
when none match.

## 9. Spike results (2026-10-11)

* The web app's PDFs are the public releases' output: 38 of 38 PDFs the
  user downloaded between April and September 2026 are byte-identical to
  the release that was live on the download day (1.1.0/1.1.4, 1.1.9,
  1.1.18, 1.1.25, 1.1.31, 1.1.39), rendered in Node with the download time.
* Only the download time varies between downloads: `CreationDate` (whole
  seconds) and `/ID = MD5("<ms>\nProducer: pdfmake\nCreator:
  pdfmake\nCreationDate: <ms>\n")`.
* Inputs the web app passes: `nrKSeF`, `qrCode` (KOD I), `acDate`.
* In goja with the shims: 34 of 34 identical for 1.1.18–1.1.39 with the gfs
  build (1.1.0 and 1.1.9 use pdfmake 0.2 and were not tried).
* Speed: start-up 7.1 s with upstream fonts, 0.5 s with the gfs build;
  about 1 s per PDF; 36 PDFs take 40 s on 1 renderer, 11.6 s on 4, 7.3 s on 8.
  Memory: about 140 MB for one renderer, 565 MB for eight.
* Go's `compress/zlib` does not reproduce the generator's streams; inside
  goja the generator uses its own pako, so this does not matter.
