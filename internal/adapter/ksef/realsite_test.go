package ksef

// Real-site checks against KSeF's TEST environment (ksef spec §10.3),
// skipped unless asked for:
//
//	GFS_KSEF_REALSITE=1 go test ./internal/adapter/ksef/ -run TestReal -v -timeout 20m
//
// Each run uses a random NIP and a self-signed certificate (TEST accepts
// both), sends two invoices of its own and reads them back. No real data.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func invoiceXML(t *testing.T, nip string, n int) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/invoice-fa3.xml")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return []byte(strings.NewReplacer("#nip#", nip, "#created#", now.Format("2006-01-02T15:04:05Z"),
		"#date#", now.Format("2006-01-02"), "#number#", fmt.Sprintf("%d-%d", now.Unix(), n)).Replace(string(b)))
}

// listUntil lists until the invoice ksef is in the listing (the HWM lags
// about 2 minutes), at most every 30 s for 6 minutes.
func listUntil(t *testing.T, s *session, cur, ksef string) ([]string, string, []byte) {
	t.Helper()
	for range 12 {
		l, err := s.List(bg, cur)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		var got []byte
		for _, r := range l.Resources {
			ids = append(ids, r.ID)
			if strings.HasSuffix(r.ID, "/"+ksef) {
				got = r.Files["fa"]
			}
		}
		if got != nil {
			return ids, l.Cursor, got
		}
		time.Sleep(30 * time.Second)
	}
	t.Fatalf("invoice %s never listed", ksef)
	return nil, "", nil
}

func TestRealTEST(t *testing.T) {
	if os.Getenv("GFS_KSEF_REALSITE") == "" {
		t.Skip("GFS_KSEF_REALSITE not set")
	}
	nip := randomNIP()
	base := apiBase["test.ksef.mf.gov.pl"]
	boot := NewClient(base, nip, "")
	if err := loginXAdES(bg, boot); err != nil {
		t.Fatal(err)
	}
	token, err := createToken(bg, boot)
	if err != nil {
		t.Fatal(err)
	}
	inv1 := invoiceXML(t, nip, 1)
	k1, err := sendInvoice(bg, boot, inv1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("NIP %s, invoice %s", nip, k1)

	s := newSession(target{host: "test.ksef.mf.gov.pl", nip: nip, base: base, token: token, since: "-1d", roles: []string{"issued"}})
	ids, cur, got := listUntil(t, s, "", k1)
	if !bytes.Equal(got, inv1) || len(ids) != 1 {
		t.Fatalf("the original comes back byte for byte, alone: %v", ids)
	}

	inv2 := invoiceXML(t, nip, 2)
	k2, err := sendInvoice(bg, boot, inv2)
	if err != nil {
		t.Fatal(err)
	}
	ids, _, got = listUntil(t, s, cur, k2)
	if !bytes.Equal(got, inv2) || len(ids) != 1 {
		t.Fatalf("an incremental listing brings only the new invoice: %v", ids)
	}

	var b bytes.Buffer
	if _, err := s.Download(bg, "issued/"+k2, "fa", &b); err != nil || !bytes.Equal(b.Bytes(), inv2) {
		t.Fatalf("Download: %v", err)
	}
}
