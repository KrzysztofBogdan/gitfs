package ksefpdf

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
)

var (
	sharedOnce sync.Once
	shared     *Renderer
	sharedErr  error
)

// testRenderer is one renderer for the package's tests: start-up is 0.5 s.
func testRenderer(t *testing.T) *Renderer {
	t.Helper()
	sharedOnce.Do(func() { shared, sharedErr = NewRenderer() })
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return shared
}

func TestGolden(t *testing.T) {
	r := testRenderer(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []*time.Location{time.Local, ny} {
		old := time.Local
		time.Local = zone
		for _, c := range loadGolden(t) {
			got, err := r.Render(Input{XML: readTestdata(t, c.XML), KsefNumber: c.Ksef, QRBase: c.QRBase})
			if err != nil {
				t.Fatalf("%s: %v", c.XML, err)
			}
			if want := readTestdata(t, c.PDF); !bytes.Equal(got, want) {
				os.WriteFile(t.TempDir()+"/"+c.PDF, got, 0o644)
				t.Errorf("%s in %s: %d bytes, the upstream generator gives %d", c.XML, zone, len(got), len(want))
			}
		}
		time.Local = old
	}
}

func TestRenderUTF16(t *testing.T) {
	c := loadGolden(t)[0]
	x := readTestdata(t, c.XML)
	u := utf16Bytes(strings.Replace(string(x), `encoding="UTF-8"`, `encoding="UTF-16"`, 1), true, true)
	pdf, err := testRenderer(t).Render(Input{XML: u, KsefNumber: c.Ksef, QRBase: c.QRBase})
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("a UTF-16 original renders: %v", err)
	}
}

func evalJS(t *testing.T, r *Renderer, src string) goja.Value {
	t.Helper()
	v, err := r.vm.RunString(src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v
}

func TestShimTime(t *testing.T) {
	r := testRenderer(t)
	ny, _ := time.LoadLocation("America/New_York")
	old := time.Local
	time.Local = ny
	defer func() { time.Local = old }()
	r.vm.Set("__now", int64(1786357530123))
	for src, want := range map[string]string{
		`Date.now()`:           "1786357530123",
		`new Date().getTime()`: "1786357530123",
		`new Date(Date.UTC(2026,2,29,0,59)).getHours()`:                            "1", // CET
		`new Date(Date.UTC(2026,2,29,1,0)).getHours()`:                             "3", // CEST
		`new Date(Date.UTC(2026,7,10,10,0)).getTimezoneOffset()`:                   "-120",
		`new Date('2026-08-01').getDate()`:                                         "1",
		`new Date('2026-08-10T12:00:00').getTime() === Date.UTC(2026,7,10,10,0,0)`: "true",
		`new Date('2026-08-10T12:00:00.5').getMilliseconds()`:                      "500",
		`new Date(2026, 7, 10).getTime() === Date.UTC(2026,7,9,22,0,0)`:            "true",
		`new Date('2026-08-10T12:00:00Z').getHours()`:                              "14",
		`new Date(Date.UTC(2026,7,10)).getDay()`:                                   "1",
		`new Intl.DateTimeFormat('pl-PL', {timeZone: 'Europe/Warsaw', year: 'numeric', month: '2-digit', day: '2-digit'}).format(new Date(Date.UTC(2026,7,10,23,5,9)))`: "11.08.2026",
		`new Intl.DateTimeFormat('pl-PL', {hour: '2-digit', minute: '2-digit'}).format(new Date(Date.UTC(2026,7,10,10,5,9)))`:                                           "10.08.2026, 12:05",
		`new Intl.DateTimeFormat('pl-PL', {hour: '2-digit', minute: '2-digit', second: '2-digit'}).format(new Date(Date.UTC(2026,7,10,10,5,9)))`:                        "10.08.2026, 12:05:09",
		`[1, 2, 5, 12, 22, 1.5].map(n => new Intl.PluralRules('pl').select(n)).join()`:                                                                                  "one,few,many,many,few,other",
		`[1, 2].map(n => new Intl.PluralRules('en').select(n)).join()`:                                                                                                  "one,other",
		`new TextDecoder('utf-16be').decode(new Uint8Array([0, 0x41, 1, 0x7c]))`:                                                                                        "Aż",
		`new TextDecoder().decode(new TextEncoder().encode('żółw'))`:                                                                                                    "żółw",
		`typeof console.log`: "function",
	} {
		if got := evalJS(t, r, src).String(); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}

func TestUnknownForm(t *testing.T) {
	r := testRenderer(t)
	_, err := r.Render(Input{XML: []byte(`<Faktura><Naglowek><KodFormularza kodSystemowy="FA (9)">FA</KodFormularza></Naglowek></Faktura>`), KsefNumber: "1234563218-20260810-0100A1B2C3D4-5E"})
	if err == nil || !strings.Contains(err.Error(), "Unknown XML Version: FA (9)") {
		t.Fatalf("the generator's message: %v", err)
	}
	c := loadGolden(t)[0]
	if _, err := r.Render(Input{XML: readTestdata(t, c.XML), KsefNumber: c.Ksef}); err != nil || r.broken {
		t.Fatalf("the renderer survives a JavaScript error: %v", err)
	}
}

func TestInterrupt(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	old := renderTimeout
	renderTimeout = 50 * time.Millisecond
	defer func() { renderTimeout = old }()
	loop, _ := goja.AssertFunction(evalJS(t, r, `(function () { for (;;) {} })`))
	r.gen = loop
	start := time.Now()
	_, err = r.Render(Input{XML: []byte("<Faktura/>")})
	if err == nil || !strings.Contains(err.Error(), "longer than") || !r.broken || time.Since(start) > 5*time.Second {
		t.Fatalf("a looping render is interrupted: %v broken=%v", err, r.broken)
	}
	if _, err := r.Render(Input{XML: []byte("<Faktura/>")}); err == nil {
		t.Fatal("a broken renderer refuses work")
	}
	renderTimeout = old // the context, not the timeout, must stop this one
	r2, _ := NewRenderer()
	loop2, _ := goja.AssertFunction(evalJS(t, r2, `(function () { for (;;) {} })`))
	r2.gen = loop2
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r2.render(ctx, Input{XML: []byte("<Faktura/>")}, clockFromXML); err != context.DeadlineExceeded || !r2.broken {
		t.Fatalf("a cancelled context interrupts: %v", err)
	}
}

func TestTimeoutScales(t *testing.T) {
	if timeoutFor(500) != renderTimeout {
		t.Fatal("a small invoice gets the base limit")
	}
	// about 15 ms per invoice line, a line about 400 bytes of XML: a 4000-line
	// invoice (1.6 MB) needs about 60 s on a fast machine
	if got := timeoutFor(1600 << 10); got < renderTimeout+5*time.Minute {
		t.Fatalf("a large invoice gets room for slow machines: %v", got)
	}
}
