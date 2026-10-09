package ksef

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func testSession(srv *kseftest.Server, rs ...string) *session {
	if len(rs) == 0 {
		rs = roles
	}
	s := newSession(target{host: "test.ksef.mf.gov.pl", nip: kseftest.NIP, base: srv.URL, token: srv.Token, since: "2026-02-01", roles: rs})
	s.c.Sleep = func(context.Context, time.Duration) error { return nil }
	return s
}

func TestListAndCursor(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject1", 1, t0)
	addN(srv, "Subject2", 2, t0)
	s := testSession(srv)
	l, err := s.List(bg, "")
	if err != nil || len(l.Resources) != 3 || l.Full || len(l.Deleted) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
	var cur map[string]string
	if json.Unmarshal([]byte(l.Cursor), &cur) != nil || len(cur) != 3 {
		t.Fatalf("one continuation per role: %s", l.Cursor)
	}
	r := l.Resources[0]
	if !strings.HasPrefix(r.Path, "_gfs/") || r.Root == nil || len(r.Files["fa"]) == 0 {
		t.Fatalf("%+v", r)
	}
	n := srv.Count("POST /invoices/exports")
	srv.Add(kseftest.Invoice{Subject: "Subject2", Stored: time.Now()}) // after the HWM the cursor holds
	l2, err := s.List(bg, l.Cursor)
	if err != nil || srv.Count("POST /invoices/exports")-n != 3 {
		t.Fatalf("incremental: one export per role: %v", err)
	}
	if len(l2.Resources) != 1 || !strings.HasPrefix(l2.Resources[0].ID, "received/") {
		t.Fatalf("only the new invoice: %+v", l2.Resources)
	}
}

func TestListRolesNarrowed(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	s := testSession(srv, "received")
	day := utc(time.Now().Add(-24 * time.Hour))
	l, err := s.List(bg, `{"issued":"`+day+`","received":"`+day+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.FullDirs, " ") != "_gfs/issued issued _gfs/third-party third-party" {
		t.Fatalf("roles outside the selection are full (and empty): %v", l.FullDirs)
	}
	if ex := srv.Exports(); len(ex) != 1 || ex[0].Subject != "Subject2" || ex[0].From != day {
		t.Fatalf("only the selected role, from its cursor: %+v", ex)
	}
}

func TestListErrorKeepsNothing(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject1", 1, t0)
	srv.ExportFailFor = "Subject2"
	srv.ExportFail = "boom"
	l, err := testSession(srv).List(bg, "")
	if err == nil || len(l.Resources) != 0 || l.Cursor != "" || !strings.Contains(err.Error(), "received") {
		t.Fatalf("a failed role fails the listing and names the role: %+v %v", l, err)
	}
}

func TestCheckRefusesEverything(t *testing.T) {
	s := &session{}
	res := s.Check(bg, adapter.ApplyRequest{Actions: []adapter.Action{{Verb: "update"}, {Verb: "delete"}}})
	for _, r := range res {
		if r.Err == nil || r.Err.Error() != "KSeF invoices are read-only" {
			t.Fatal(res)
		}
	}
	if (&Adapter{}).ReadOnly() != "KSeF invoices are read-only" {
		t.Fatal("the engine refuses with the same message")
	}
}

func TestDownload(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	addN(srv, "Subject2", 1, t0)
	s := testSession(srv)
	l, _ := s.List(bg, "")
	var b bytes.Buffer
	info, err := s.Download(bg, l.Resources[0].ID, "fa", &b)
	if err != nil || !bytes.Equal(b.Bytes(), l.Resources[0].Files["fa"]) || info.Size != int64(b.Len()) {
		t.Fatal(err)
	}
}

func TestPlacer(t *testing.T) {
	a := &Adapter{}
	k := "6887603807-20261006-6CA868C00000-35"
	if a.AttachmentPath(metaPath("issued", k), k+".xml") != origPath("issued", k) {
		t.Fatal(a.AttachmentPath(metaPath("issued", k), k+".xml"))
	}
	if r, ok := a.ResourceOf(origPath("issued", k)); !ok || r != metaPath("issued", k) {
		t.Fatal(r)
	}
	for _, p := range []string{"issued/2026/10/scan.pdf", "_gfs/issued/2026/10/" + k + ".xml", "other/2026/10/" + k + ".xml", k + ".xml"} {
		if _, ok := a.ResourceOf(p); ok {
			t.Error(p)
		}
	}
}
