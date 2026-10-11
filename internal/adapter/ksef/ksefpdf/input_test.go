package ksefpdf

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type goldenCase struct {
	XML     string `json:"xml"`
	PDF     string `json:"pdf"`
	Ksef    string `json:"ksef"`
	QRBase  string `json:"qrBase"`
	ClockMs int64  `json:"clockMs"`
	AcDate  string `json:"acDate"`
	QRCode  string `json:"qrCode"`
}

func loadGolden(t *testing.T) []goldenCase {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cs []goldenCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFactsGolden(t *testing.T) {
	for _, c := range loadGolden(t) {
		f := factsOf(Input{XML: readTestdata(t, c.XML), KsefNumber: c.Ksef, QRBase: c.QRBase})
		if f.acDate != c.AcDate || f.qrCode != c.QRCode || f.clockMs != c.ClockMs {
			t.Errorf("%s: %q %q %d, golden.json says %q %q %d", c.XML, f.acDate, f.qrCode, f.clockMs, c.AcDate, c.QRCode, c.ClockMs)
		}
	}
}

func TestFactsFallbacks(t *testing.T) {
	k := "1234563218-20260810-0100A1B2C3D4-5E"
	midnight := time.Date(2026, 8, 10, 0, 0, 0, 0, warsaw).UnixMilli()
	for _, c := range []struct {
		xml   string
		clock int64
		qr    bool
	}{
		{`<Faktura><Naglowek><DataWytworzeniaFa>2026-08-10T10:15:30.9999999Z</DataWytworzeniaFa></Naglowek></Faktura>`,
			time.Date(2026, 8, 10, 10, 15, 30, 999e6, time.UTC).UnixMilli(), false},
		{`<Faktura><Naglowek><DataWytworzeniaFa>2026-08-10T12:00:00</DataWytworzeniaFa></Naglowek></Faktura>`,
			time.Date(2026, 8, 10, 12, 0, 0, 0, warsaw).UnixMilli(), false},
		{`<Faktura><Naglowek><DataWytworzeniaFa>yesterday</DataWytworzeniaFa></Naglowek></Faktura>`, midnight, false},
		{`<Faktura/>`, midnight, false},
		{`<x:Faktura xmlns:x="u"><x:Podmiot1><x:DaneIdentyfikacyjne><x:NIP> 1234563218 </x:NIP></x:DaneIdentyfikacyjne></x:Podmiot1><x:Fa><x:P_1>2026-08-09</x:P_1></x:Fa></x:Faktura>`, midnight, true},
		{`<Faktura><Fa><P_1>9 Aug</P_1></Fa><Podmiot1><DaneIdentyfikacyjne><NIP>1234563218</NIP></DaneIdentyfikacyjne></Podmiot1></Faktura>`, midnight, false},
		{`not xml`, midnight, false},
	} {
		f := factsOf(Input{XML: []byte(c.xml), KsefNumber: k, QRBase: "https://qr-test.ksef.mf.gov.pl"})
		if f.clockMs != c.clock || (f.qrCode != "") != c.qr || f.acDate != "10.08.2026" {
			t.Errorf("%s: clock %d (want %d), qr %q", c.xml, f.clockMs, c.clock, f.qrCode)
		}
		if c.qr && !strings.HasPrefix(f.qrCode, "https://qr-test.ksef.mf.gov.pl/invoice/1234563218/09-08-2026/") {
			t.Errorf("KOD I link: %s", f.qrCode)
		}
	}
	if f := factsOf(Input{XML: []byte("<Faktura/>"), KsefNumber: "bad"}); f.acDate != "" || f.clockMs != 0 {
		t.Fatalf("a KSeF number without a date: %+v", f)
	}
	if f := factsOf(Input{XML: readTestdata(t, "fa3.xml"), KsefNumber: k}); f.qrCode != "" {
		t.Fatal("no QR base, no QR code")
	}
}

func utf16Bytes(s string, little, bom bool) []byte {
	if bom {
		s = "\ufeff" + s
	}
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		if little {
			out = append(out, byte(u), byte(u>>8))
		} else {
			out = append(out, byte(u>>8), byte(u))
		}
	}
	return out
}

func TestDecodeText(t *testing.T) {
	want := `<a>zażółć</a>`
	for name, b := range map[string][]byte{
		"utf-8":        []byte(want),
		"utf-8 bom":    append([]byte{0xef, 0xbb, 0xbf}, want...),
		"utf-16le bom": utf16Bytes(want, true, true),
		"utf-16le":     utf16Bytes(want, true, false),
		"utf-16be bom": utf16Bytes(want, false, true),
		"utf-16be":     utf16Bytes(want, false, false),
	} {
		if got := decodeText(b); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := decodeText([]byte("<a>\xff</a>")); got != "<a>\ufffd</a>" {
		t.Errorf("invalid UTF-8 becomes U+FFFD: %q", got)
	}
}
