package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func ksefClone(t *testing.T, srv *kseftest.Server, query string) string {
	t.Helper()
	t.Setenv("GFS_KSEF_TOKEN", srv.Token)
	dir := t.TempDir()
	t.Chdir(dir)
	remote := "ksef://test.ksef.mf.gov.pl/" + kseftest.NIP + "?base=" + url.QueryEscape(srv.URL) + query
	mustContain(t, mustRun(t, 0, "clone", remote), "Cloned ")
	wt := filepath.Join(dir, "ksef-test-"+kseftest.NIP)
	t.Chdir(wt)
	return wt
}

func TestKSeFCloneStatusRestorePull(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject1", Stored: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)})
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)})
	ksefClone(t, srv, "")
	inv := srv.Invoices()
	orig := filepath.Join("issued", "2026", "03", inv[0].KsefNumber+".xml")
	meta := filepath.Join("_gfs", "issued", "2026", "03", inv[0].KsefNumber+".xml")
	b, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if h := sha256.Sum256(b); base64.StdEncoding.EncodeToString(h[:]) != inv[0].Hash {
		t.Fatal("the original is byte-identical to KSeF's")
	}
	m, _ := os.ReadFile(meta)
	mustContain(t, string(m), `role="issued"`, `<file id="fa"`, "sha256=\""+inv[0].Hash)
	if _, err := os.Stat(filepath.Join("received", "2026", "04", inv[1].KsefNumber+".xml")); err != nil {
		t.Fatal("received invoice beside its metadata tree")
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// read-only: an edited original, an edited metadata file, a stray
	os.WriteFile(orig, append(b, ' '), 0o644)
	replaceIn(t, meta, "<number>", "<number>X")
	os.WriteFile(filepath.Join("received", "2026", "04", "scan.pdf"), []byte("x"), 0o644)
	st := mustRun(t, 0, "status")
	mustContain(t, st, "attachments of this resource are read-only", "KSeF invoices are read-only", "not part of the mirror")
	before := len(srv.Requests())
	mustContain(t, mustRun(t, 1, "commit", "--force"), "FAIL")
	if len(srv.Requests()) != before {
		t.Fatalf("a commit sends nothing: %v", srv.Requests()[before:])
	}

	// restore: metadata from the local copy, the original with gfs get
	mustContain(t, mustRun(t, 0, "pull", "--force"), "restored")
	os.Remove(filepath.Join("received", "2026", "04", "scan.pdf"))
	os.Remove(orig)
	mustRun(t, 0, "get", orig)
	if b2, _ := os.ReadFile(orig); string(b2) != string(b) {
		t.Fatal("gfs get restores the original")
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// a deleted metadata file is refused, never forgotten
	os.Remove(meta)
	mustContain(t, mustRun(t, 0, "status"), "D  ", "KSeF invoices are read-only")
	mustRun(t, 1, "commit", "--force")
	if _, err := os.Stat(orig); err != nil {
		t.Fatal("a refused delete keeps the original")
	}
	mustRun(t, 0, "pull", "--force")
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	// pull adds only new invoices, one export per role
	n := srv.Count("POST /invoices/exports")
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Now()})
	out := mustRun(t, 0, "pull")
	mustContain(t, out, "+  _gfs/received/")
	if strings.Count(out, "+  ") != 1 || srv.Count("POST /invoices/exports")-n != 3 {
		t.Fatalf("one new invoice, 3 exports:\n%s", out)
	}
	newest := srv.Invoices()[2]
	if _, err := os.Stat(filepath.Join("received", newest.Stored.UTC().Format("2006/01"), newest.KsefNumber+".xml")); err != nil {
		t.Fatal("the new original is written with its metadata")
	}
}

func TestKSeFRolesNarrowed(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject1", Stored: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)})
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)})
	ksefClone(t, srv, "&roles=received")
	if _, err := os.Stat("issued"); !os.IsNotExist(err) {
		t.Fatal("issued invoices are outside the selection")
	}
	if ex := srv.Exports(); len(ex) == 0 || ex[0].Subject != "Subject2" {
		t.Fatalf("%+v", ex)
	}
}

func ksefFA3(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../adapter/ksef/ksefpdf/testdata/fa3.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKSeFPDF(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC), XML: ksefFA3(t)})
	ksefClone(t, srv, "")
	k := srv.Invoices()[0].KsefNumber
	pdf := filepath.Join("received", "2026", "08", k+".pdf")
	b, err := os.ReadFile(pdf)
	if err != nil || !strings.HasPrefix(string(b), "%PDF-") {
		t.Fatalf("the PDF beside the original: %v", err)
	}
	m, _ := os.ReadFile(filepath.Join("_gfs", "received", "2026", "08", k+".xml"))
	mustContain(t, string(m), `<file id="pdf" name="`+k+`.pdf"`, `generator="`)
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")

	os.WriteFile(pdf, append(b, ' '), 0o644)
	mustContain(t, mustRun(t, 0, "status"), "read-only")
	os.Remove(pdf) // a missing attachment is not downloaded, not deleted
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
	mustRun(t, 0, "get", pdf)
	if b2, _ := os.ReadFile(pdf); string(b2) != string(b) {
		t.Fatal("gfs get renders the same PDF")
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
}

func TestKSeFPullFullAddsPDFs(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC), XML: ksefFA3(t)})
	ksefClone(t, srv, "&pdf=no")
	k := srv.Invoices()[0].KsefNumber
	pdf := filepath.Join("received", "2026", "08", k+".pdf")
	if _, err := os.Stat(pdf); err == nil {
		t.Fatal("pdf=no renders nothing")
	}
	replaceIn(t, filepath.Join(".gfs", "config"), "pdf=no&", "") // canonical: ?pdf=no&base=…
	out := mustRun(t, 0, "pull", "--full")
	mustContain(t, out, "~  ")
	if _, err := os.Stat(pdf); err != nil {
		t.Fatalf("pull --full adds the PDF: %s", out)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
	if out := mustRun(t, 0, "pull", "--full"); strings.Contains(out, "~  ") {
		t.Fatalf("a second --full changes nothing: %s", out)
	}
}
