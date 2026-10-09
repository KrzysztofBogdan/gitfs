package ksef

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

func TestLoginStoresVerifiedToken(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	stored := map[string]string{}
	storeEntry = func(n, v string) error { stored[n] = v; return nil }
	t.Cleanup(func() { storeEntry = defaultStore })
	var out bytes.Buffer
	u, _ := url.Parse("ksef://test.ksef.mf.gov.pl/" + kseftest.NIP)
	io := adapter.LoginIO{Out: &out, ReadSecret: func(string) (string, error) { return srv.Token + "\n", nil },
		Flags: map[string]string{"base": srv.URL}}
	if err := (&Adapter{}).Login(bg, u, io); err != nil {
		t.Fatal(err, out.String())
	}
	if stored["ksef:test.ksef.mf.gov.pl/"+kseftest.NIP] != srv.Token || !strings.Contains(out.String(), "InvoiceRead") {
		t.Fatalf("%v\n%s", stored, out.String())
	}
	if srv.Count("POST /invoices/query/metadata") != 1 {
		t.Fatal("verified with one metadata query")
	}
	if strings.Contains(out.String(), srv.Token) {
		t.Fatal("the token is never printed")
	}
	io.ReadSecret = func(string) (string, error) { return "wrong", nil }
	stored = map[string]string{}
	err := (&Adapter{}).Login(bg, u, io)
	var refused *adapter.LoginRefused
	if !errors.As(err, &refused) || len(stored) != 0 {
		t.Fatalf("a refused token stores nothing: %v", err)
	}
}
