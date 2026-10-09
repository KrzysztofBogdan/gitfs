package ksef

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/kseftest"
)

var bg = context.Background()

func testClient(srv *kseftest.Server) *Client {
	c := NewClient(srv.URL, kseftest.NIP, srv.Token)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestLogin(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	tok, err := c.access(bg)
	if err != nil || tok == "" {
		t.Fatal(tok, err)
	}
	want := []string{"GET /security/public-key-certificates", "POST /auth/challenge", "POST /auth/ksef-token", "GET /auth/", "POST /auth/token/redeem"}
	got := srv.Requests()
	if len(got) < len(want) {
		t.Fatal(got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Fatalf("%d: %s, want %s", i, got[i], w)
		}
	}
	if again, _ := c.access(bg); again != tok || len(srv.Requests()) != len(got) {
		t.Fatal("a valid access token is reused")
	}
}

func TestLoginBadToken(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	c := testClient(srv)
	c.Token = "wrong-secret"
	c.LoginHint = "gfs auth login ksef://test.ksef.mf.gov.pl/" + kseftest.NIP
	_, err := c.access(bg)
	if err == nil || !strings.Contains(err.Error(), "run gfs auth login ksef://") || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("refused token: hint, and the token never in the message: %v", err)
	}
}

func TestRefresh(t *testing.T) {
	srv := kseftest.New()
	t.Cleanup(srv.Close)
	srv.AccessTTL = 30 * time.Second // under a minute left: refresh before use
	c := testClient(srv)
	first, _ := c.access(bg)
	second, err := c.access(bg)
	if err != nil || second == first || srv.Count("POST /auth/token/refresh") != 1 {
		t.Fatalf("%v %v", err, srv.Requests())
	}
}

func TestAPIErrorShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"exception":{"exceptionDetailList":[{"exceptionCode":21405,"exceptionDescription":"Błąd walidacji danych wejściowych.","details":["dateRange too long"]}]}}`: "Błąd walidacji danych wejściowych. (21405): dateRange too long",
		`{"title":"Unauthorized","status":401,"detail":"Token expired"}`:                                                  "Unauthorized: Token expired",
		`{"status":{"code":429,"description":"Too Many Requests","details":["Przekroczono limit 20 żądań na godzinę."]}}`: "Too Many Requests: Przekroczono limit 20 żądań na godzinę.",
		`not json`: "HTTP 500",
	} {
		if got := apiMessage(500, []byte(body)); got != want {
			t.Errorf("%s\n got %q\nwant %q", body, got, want)
		}
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	c := NewClient("https://x", kseftest.NIP, "tok-secret")
	c.accessTok, c.refreshTok = "acc-secret", "ref-secret"
	tg := target{host: "ksef.mf.gov.pl", nip: kseftest.NIP, token: "tok-secret"}
	for _, s := range []string{fmt.Sprintf("%v %+v %#v", c, c, c), fmt.Sprintf("%v %+v", tg, tg)} {
		if strings.Contains(s, "secret") {
			t.Fatal(s)
		}
	}
}

func TestErrorHints(t *testing.T) {
	e := &APIError{Status: 403, Op: "POST /invoices/exports", Msg: "Forbidden", Hint: "gfs auth login ksef://x/1"}
	if !strings.Contains(e.Error(), "InvoiceRead") {
		t.Fatal(e)
	}
	e = &APIError{Status: 429, Op: "POST /invoices/exports", Msg: "Too Many Requests", RetryAfter: "1800"}
	if !strings.Contains(e.Error(), "retry after 1800 s") {
		t.Fatal(e)
	}
}
