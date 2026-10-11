package ksefpdf

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Warsaw on systems without a zone database
	"unicode/utf16"
)

// Input is one invoice to render (ksef pdf spec §4.1).
type Input struct {
	XML        []byte // the original, as KSeF stores it
	KsefNumber string
	QRBase     string // https://qr.ksef.mf.gov.pl, -test or -demo; "" draws no QR code
}

// facts are what the generator gets besides the KSeF number.
type facts struct {
	text    string // the XML as the browser's readAsText gives it
	acDate  string // dd.mm.yyyy from the KSeF number (§4.1)
	qrCode  string // KOD I link (§4.4), "" when it cannot be built
	clockMs int64  // render time (§4.3)
}

var warsaw = func() *time.Location {
	l, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		panic(err)
	}
	return l
}()

func factsOf(in Input) facts {
	f := facts{text: decodeText(in.XML)}
	nip, p1, created := fields(f.text)
	day, hasDay := ksefDay(in.KsefNumber)
	if hasDay {
		f.acDate = day.Format("02.01.2006")
		f.clockMs = day.UnixMilli()
	}
	if t, ok := parseCreated(created); ok {
		f.clockMs = t.UnixMilli()
	}
	if d, err := time.Parse("2006-01-02", p1); err == nil && nip != "" && in.QRBase != "" {
		h := sha256.Sum256(in.XML)
		f.qrCode = in.QRBase + "/invoice/" + nip + "/" + d.Format("02-01-2006") + "/" + base64.RawURLEncoding.EncodeToString(h[:])
	}
	return f
}

// ksefDay is midnight Europe/Warsaw of the date in a KSeF number
// (<nip>-<YYYYMMDD>-<technical>-<check>).
func ksefDay(k string) (time.Time, bool) {
	parts := strings.Split(k, "-")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102", parts[1], warsaw)
	return t, err == nil
}

// parseCreated reads DataWytworzeniaFa: with a zone, or as Europe/Warsaw
// local time without one.
func parseCreated(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", s, warsaw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// fields reads Podmiot1/DaneIdentyfikacyjne/NIP, Fa/P_1 and
// Naglowek/DataWytworzeniaFa under the root, by local name.
func fields(text string) (nip, p1, created string) {
	d := xml.NewDecoder(strings.NewReader(text))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // already text
	var stack []string
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) < 2 {
				continue
			}
			v := strings.TrimSpace(string(t))
			switch strings.Join(stack[1:], "/") {
			case "Podmiot1/DaneIdentyfikacyjne/NIP":
				nip = first(nip, v)
			case "Fa/P_1":
				p1 = first(p1, v)
			case "Naglowek/DataWytworzeniaFa":
				created = first(created, v)
			}
		}
	}
}

// first keeps the first value found.
func first(old, v string) string {
	if old != "" {
		return old
	}
	return v
}

// decodeText reads the XML the way FileReader.readAsText does after the
// generator's encoding check: UTF-16 by byte-order mark or a zero byte
// beside the first '<', otherwise UTF-8; a byte-order mark is dropped.
func decodeText(b []byte) string {
	switch {
	case len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == '<' && b[1] == 0):
		return utf16Text(b, binary.LittleEndian)
	case len(b) >= 2 && (b[0] == 0xfe && b[1] == 0xff || b[0] == 0 && b[1] == '<'):
		return utf16Text(b, binary.BigEndian)
	}
	return strings.TrimPrefix(strings.ToValidUTF8(string(b), "\ufffd"), "\ufeff")
}

func utf16Text(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return strings.TrimPrefix(string(utf16.Decode(u)), "\ufeff")
}
