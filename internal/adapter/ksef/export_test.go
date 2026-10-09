package ksef

import (
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func addN(srv *kseftest.Server, subject string, n int, from time.Time) {
	for i := range n {
		srv.Add(kseftest.Invoice{Subject: subject, Stored: from.Add(time.Duration(i) * time.Hour)})
	}
}

func TestExportRole(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject2", 3, t0)
	c := testClient(srv)
	now := t0.Add(24 * time.Hour)
	srv.HWM = func() time.Time { return now.Add(-2 * time.Minute) }
	invs, next, err := c.exportRole(bg, "Subject2", t0.Add(-time.Hour), now)
	if err != nil || len(invs) != 3 {
		t.Fatalf("%d %v", len(invs), err)
	}
	for _, inv := range invs {
		if b64sha(inv.xml) != inv.meta.InvoiceHash || !strings.HasPrefix(string(inv.xml), "<?xml") {
			t.Fatalf("original bytes with their hash: %s", inv.meta.KsefNumber)
		}
	}
	if !next.Equal(now.Add(-2 * time.Minute)) {
		t.Fatalf("continue from the HWM: %v", next)
	}
	if srv.Count("POST /invoices/exports") != 1 || srv.Count("GET /invoices/ksef/") != 0 {
		t.Fatal(srv.Requests())
	}
}

func TestWindowsFromOldCursor(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	now := t0.AddDate(0, 7, 0)
	if _, _, err := c.exportRole(bg, "Subject1", t0, now); err != nil {
		t.Fatal(err)
	}
	ex := srv.Exports()
	if len(ex) != 3 {
		t.Fatalf("7 months in 90-day windows: %+v", ex)
	}
	for i, e := range ex[:2] {
		from, _ := time.Parse(time.RFC3339Nano, e.From)
		to, _ := time.Parse(time.RFC3339Nano, e.To)
		if to.Sub(from) > 90*24*time.Hour || (i > 0 && e.From != ex[i-1].To) {
			t.Fatalf("adjacent windows of at most 90 days: %+v", ex)
		}
	}
	if ex[2].To != "" || ex[2].From != ex[1].To {
		t.Fatalf("the last window is open: %+v", ex)
	}
}

func TestExportDedupAcrossTruncation(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.TruncateAt = 2
	addN(srv, "Subject1", 5, t0)
	c := testClient(srv)
	invs, _, err := c.exportRole(bg, "Subject1", t0.Add(-time.Hour), t0.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, inv := range invs {
		if seen[inv.meta.KsefNumber] {
			t.Fatalf("duplicate %s", inv.meta.KsefNumber)
		}
		seen[inv.meta.KsefNumber] = true
	}
	if len(seen) != 5 || srv.Count("POST /invoices/exports") < 3 {
		t.Fatalf("%d invoices, %d exports", len(seen), srv.Count("POST /invoices/exports"))
	}
}

func TestExportMultiPart(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.PartSize = 700
	addN(srv, "Subject2", 4, t0)
	c := testClient(srv)
	invs, _, err := c.exportRole(bg, "Subject2", t0.Add(-time.Hour), t0.Add(48*time.Hour))
	if err != nil || len(invs) != 4 || srv.Count("GET /storage/") < 2 {
		t.Fatalf("%d %v %v", len(invs), err, srv.Requests())
	}
}

func TestExport429(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Fail429 = 1
	c := testClient(srv)
	var waits []string
	c.OnWait = func(m string) { waits = append(waits, m) }
	if _, _, err := c.exportRole(bg, "Subject1", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || !strings.Contains(waits[0], "Rate limited by KSeF") {
		t.Fatal(waits)
	}
}

func TestExportFailed(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.ExportFail = "Błąd eksportu"
	_, _, err := testClient(srv).exportRole(bg, "Subject1", t0, t0.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "Błąd eksportu") || !strings.Contains(err.Error(), "Subject1") {
		t.Fatal(err)
	}
}

func TestExportDamaged(t *testing.T) {
	for name, set := range map[string]func(*kseftest.Server){
		"part": func(s *kseftest.Server) { s.DamagePart = true },
		"hash": func(s *kseftest.Server) { s.Add(kseftest.Invoice{Subject: "Subject1", Stored: t0, BadHash: true}) },
	} {
		srv := kseftest.New()
		addN(srv, "Subject1", 1, t0)
		set(srv)
		_, _, err := testClient(srv).exportRole(bg, "Subject1", t0.Add(-time.Hour), t0.Add(time.Hour))
		srv.Close()
		if err == nil || !(strings.Contains(err.Error(), "damaged") || strings.Contains(err.Error(), "does not match KSeF's hash")) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
