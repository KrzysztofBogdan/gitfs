package ksef

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func fa3(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("ksefpdf/testdata/fa3.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pdfSession(srv *kseftest.Server) (*session, *[]adapter.Progress) {
	s := testSession(srv)
	s.t.pdf = true
	var got []adapter.Progress
	s.SetProgress(func(p adapter.Progress) { got = append(got, p) })
	return s, &got
}

func TestListRendersPDF(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: t0, XML: fa3(t)})
	s, prog := pdfSession(srv)
	l, err := s.List(bg, "")
	if err != nil || len(l.Resources) != 1 {
		t.Fatal(err)
	}
	r := l.Resources[0]
	if !bytes.HasPrefix(r.Files["pdf"], []byte("%PDF-")) {
		t.Fatal("a rendered PDF rides along")
	}
	if len(r.Root.ChildrenNamed("file")) != 2 {
		t.Fatal("two file elements")
	}
	var last adapter.Progress
	for _, p := range *prog {
		if p.Phase == "render" {
			last = p
		}
		if p.Phase == "warn" {
			t.Fatalf("no warning: %+v", p)
		}
	}
	if last.Done != 1 || last.Total != 1 {
		t.Fatalf("render progress: %+v", last)
	}
	var b bytes.Buffer
	if _, err := s.Download(bg, r.ID, "pdf", &b); err != nil || !bytes.Equal(b.Bytes(), r.Files["pdf"]) {
		t.Fatalf("gfs get renders the same PDF from the downloaded original: %v", err)
	}
}

func TestListPDFOncePerInvoice(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	x := fa3(t)
	srv.Add(kseftest.Invoice{Subject: "Subject1", Stored: t0, XML: x, KsefNumber: "1234563218-20260810-0100A1B2C3D4-5E"})
	srv.Add(kseftest.Invoice{Subject: "Subject3", Stored: t0, XML: x, KsefNumber: "1234563218-20260810-0100A1B2C3D4-5E"})
	s, prog := pdfSession(srv)
	l, err := s.List(bg, "")
	if err != nil || len(l.Resources) != 2 || !bytes.Equal(l.Resources[0].Files["pdf"], l.Resources[1].Files["pdf"]) {
		t.Fatal(err)
	}
	for _, p := range *prog {
		if p.Phase == "render" && p.Total != 1 {
			t.Fatalf("an invoice in two roles renders once: %+v", p)
		}
	}
}

func TestListPDFFailureWarns(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject2", 1, t0) // the fake's minimal XML: no form code
	s, prog := pdfSession(srv)
	l, err := s.List(bg, "")
	if err != nil || len(l.Resources) != 1 || l.Resources[0].Files["pdf"] != nil {
		t.Fatalf("the invoice is listed without a PDF: %v", err)
	}
	var warns []string
	for _, p := range *prog {
		if p.Phase == "warn" {
			warns = append(warns, p.Item)
		}
	}
	k := srv.Invoices()[0].KsefNumber
	if len(warns) != 1 || !strings.HasPrefix(warns[0], "PDF of "+k+": Unknown XML Version") {
		t.Fatalf("one warning naming the invoice: %q", warns)
	}
}

func TestListPDFOff(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: t0, XML: fa3(t)})
	s := testSession(srv) // pdf off
	l, err := s.List(bg, "")
	if err != nil || l.Resources[0].Files["pdf"] != nil {
		t.Fatal("pdf=no renders nothing")
	}
	if _, err := s.Download(bg, l.Resources[0].ID, "pdf", &bytes.Buffer{}); err != adapter.ErrNotFound {
		t.Fatalf("pdf=no has no pdf downloads: %v", err)
	}
}

func TestListCancelledWhileRendering(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	for range 3 {
		srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: t0, XML: fa3(t)})
	}
	s, _ := pdfSession(srv)
	s.pdfWorkers = 1
	ctx, cancel := context.WithCancel(context.Background())
	s.SetProgress(func(p adapter.Progress) {
		if p.Phase == "render" {
			cancel()
		}
	})
	start := time.Now()
	if _, err := s.List(ctx, ""); err != context.Canceled || time.Since(start) > 10*time.Second {
		t.Fatalf("a cancelled listing stops rendering: %v", err)
	}
}
