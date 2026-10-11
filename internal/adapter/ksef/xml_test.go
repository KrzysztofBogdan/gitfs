package ksef

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/ksefpdf"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func sampleMeta() apiInvoice {
	var m apiInvoice
	m.KsefNumber = "5261040828-20261003-0A12BC340000-7F"
	m.InvoiceNumber, m.InvoiceType, m.FormCode.SystemCode = "FA/123/10/2026", "Kor", "FA (3)"
	m.IssueDate, m.InvoicingDate, m.AcquisitionDate, m.PermanentStorageDate = "2026-10-03", "2026-10-03T08:12:30.5+00:00", "2026-10-03T10:12:30.6+02:00", "2026-10-03T08:12:31+00:00"
	m.InvoicingMode = "Online"
	m.Seller.Nip, m.Seller.Name = "5261040828", "ORLEN S.A."
	m.Buyer.Identifier.Type, m.Buyer.Identifier.Value, m.Buyer.Name = "Nip", "6887603807", "Acme sp. z o.o."
	m.Currency, m.NetAmount, m.VatAmount, m.GrossAmount = "PLN", 43.6, 10.03, 53.63
	m.HashOfCorrectedInvoice, m.InvoiceHash = "abc=", "qAdJ="
	m.IsSelfInvoicing = true
	return m
}

func TestResourceOf(t *testing.T) {
	m := sampleMeta()
	r := resourceOf("received", invoice{meta: m, xml: []byte("<x/>")}, nil)
	if r.ID != "received/"+m.KsefNumber || r.Path != "_gfs/received/2026/10/"+m.KsefNumber+".xml" || r.Version != "qAdJ=" {
		t.Fatalf("%+v", r)
	}
	if string(r.Files["fa"]) != "<x/>" {
		t.Fatal("original bytes carried as attachment fa")
	}
	canon.Normalize(r.Root, invoiceSchema)
	got := xmltree.Print(r.Root, 0)
	for _, want := range []string{
		`<invoice id="received/5261040828-20261003-0A12BC340000-7F" ksef="5261040828-20261003-0A12BC340000-7F" role="received" version="qAdJ=" self-invoicing="true">`,
		`<number>FA/123/10/2026</number>`, `<type>Kor</type>`, `<form>FA (3)</form>`, `<issued>2026-10-03</issued>`,
		`<invoicing>2026-10-03T08:12:30.5Z</invoicing>`, `<acquired>2026-10-03T08:12:30.6Z</acquired>`, `<stored>2026-10-03T08:12:31Z</stored>`,
		`<mode>online</mode>`, `<seller nip="5261040828">ORLEN S.A.</seller>`, `<buyer type="nip" id="6887603807">Acme sp. z o.o.</buyer>`,
		`<amounts currency="PLN" net="43.60" vat="10.03" gross="53.63"/>`, `<corrects hash="abc="/>`,
		`<file id="fa" name="5261040828-20261003-0A12BC340000-7F.xml" size="4" sha256="qAdJ="/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "has-attachment") || strings.Contains(got, "third-party") {
		t.Errorf("absent optionals stay absent:\n%s", got)
	}
}

func TestResourceOfThirdParty(t *testing.T) {
	m := sampleMeta()
	m.ThirdSubjects = append(m.ThirdSubjects, struct {
		Identifier party  `json:"identifier"`
		Name       string `json:"name"`
		Role       int    `json:"role"`
	}{party{"Nip", "1111111111"}, "Factor\x01 S.A.", 4})
	r := resourceOf("third-party", invoice{meta: m, xml: []byte("<x/>")}, nil)
	got := xmltree.Print(r.Root, 0)
	if !strings.Contains(got, `<third-party type="nip" id="1111111111" role="4">Factor� S.A.</third-party>`) {
		t.Fatalf("characters XML cannot hold are replaced:\n%s", got)
	}
}

func TestPaths(t *testing.T) {
	k := "6887603807-20261006-6CA868C00000-35"
	if metaPath("issued", k) != "_gfs/issued/2026/10/"+k+".xml" || origPath("issued", k) != "issued/2026/10/"+k+".xml" {
		t.Fatal(metaPath("issued", k), origPath("issued", k))
	}
}

func TestRelaxNG(t *testing.T) {
	g := RelaxNG()
	if !strings.Contains(g, `<element name="file">`) || !strings.Contains(g, `<element name="invoice">`) {
		t.Fatal(g)
	}
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	write := func(name, s string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := resourceOf("received", invoice{meta: sampleMeta(), xml: []byte("<x/>")}, nil)
	canon.Normalize(r.Root, invoiceSchema)
	doc := write("inv.xml", xmltree.Print(r.Root, 0))
	rng := write("ksef.rng", g)
	if out, err := exec.Command("xmllint", "--noout", "--relaxng", rng, doc).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestResourceOfWithPDF(t *testing.T) {
	m := sampleMeta()
	r := resourceOf("received", invoice{meta: m, xml: []byte("<x/>")}, []byte("%PDF-1.3 x"))
	if string(r.Files["pdf"]) != "%PDF-1.3 x" || string(r.Files["fa"]) != "<x/>" {
		t.Fatalf("%v", r.Files)
	}
	canon.Normalize(r.Root, invoiceSchema)
	got := xmltree.Print(r.Root, 0)
	want := `<file id="pdf" name="5261040828-20261003-0A12BC340000-7F.pdf" size="10" sha256="` + b64sha([]byte("%PDF-1.3 x")) + `" generator="` + ksefpdf.Release() + `"/>`
	if !strings.Contains(got, want) {
		t.Fatalf("missing %s in\n%s", want, got)
	}
}
