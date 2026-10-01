package cloudns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/cloudns/cloudnstest"
)

var bg = context.Background()

func testClient(srv *cloudnstest.Server) *Client {
	c := NewClient(srv.URL, Auth{SubAuthID: srv.SubAuthID, Password: srv.Password})
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	c.LoginHint = "gfs auth login cloudns://sub-95884"
	return c
}

func TestLoginAndFailed(t *testing.T) {
	srv := cloudnstest.New()
	defer srv.Close()
	c := testClient(srv)
	if err := c.Do(bg, "login", nil, nil); err != nil {
		t.Fatal(err)
	}
	bad := NewClient(srv.URL, Auth{AuthID: srv.AuthID, Password: "wrong"})
	err := bad.Do(bg, "login", nil, nil)
	if err == nil || err.Error() != "cloudns: Invalid authentication, incorrect auth-id or auth-password." {
		t.Fatal(err)
	}
	c.Password = "wrong"
	if err := c.Do(bg, "login", nil, nil); err == nil || !strings.HasSuffix(err.Error(), "(run gfs auth login cloudns://sub-95884)") {
		t.Fatal(err)
	}
	srv.Fail["records"] = "Missing domain-name"
	err = testClient(srv).Do(bg, "records", url.Values{"domain-name": {"x.pl"}}, nil)
	if err == nil || err.Error() != "cloudns: records: Missing domain-name" {
		t.Fatal(err)
	}
}

// A rate-limit answer (HTTP 200, Failed) is waited out with backoff.
func TestRateLimitText(t *testing.T) {
	srv := cloudnstest.New()
	defer srv.Close()
	c := testClient(srv)
	var waits []string
	c.OnWait = func(m string) { waits = append(waits, m) }
	srv.RateLimit = 2
	if err := c.Do(bg, "login", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || !strings.HasPrefix(waits[0], "Rate limited by ClouDNS") {
		t.Fatal(waits)
	}
}

// Reads are retried on 503; writes are not (they may have been applied).
func TestRetryReadsOnly(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if calls[r.URL.Path] == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"Success","statusDescription":"ok"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, Auth{AuthID: "1", Password: "p"})
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	if err := c.Do(bg, "soa-details", nil, nil); err != nil || calls["/dns/soa-details.json"] != 2 {
		t.Fatal(err, calls)
	}
	if err := c.Do(bg, "add-record", nil, nil); err == nil || calls["/dns/add-record.json"] != 1 {
		t.Fatal(err, calls)
	}
}

func TestIsRead(t *testing.T) {
	for a, want := range map[string]bool{"login": true, "list-zones": true, "get-geodns-locations": true, "records": true,
		"soa-details": true, "mail-forwards": true, "failover-settings": true, "is-dnssec-available": true,
		"add-record": false, "mod-record": false, "delete-record": false, "modify-soa": false, "change-status": false} {
		if isRead(a) != want {
			t.Errorf("%s: %v", a, !want)
		}
	}
}
