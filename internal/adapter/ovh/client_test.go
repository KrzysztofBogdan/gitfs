package ovh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh/ovhtest"
)

var bg = context.Background()

func testClient(srv *ovhtest.Server, ck string) *Client {
	c := NewClient(srv.URL+"/1.0", Creds{AppKey: srv.AppKey, AppSecret: srv.AppSecret, ConsumerKey: ck})
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	c.LoginHint = "gfs auth login ovh://eu"
	return c
}

type credential struct {
	Status string
	Rules  []ovhtest.Rule
}

// Every request is signed with the server's clock, even when ours is off.
func TestSignedRequest(t *testing.T) {
	srv := ovhtest.New()
	defer srv.Close()
	srv.Skew = 10 * time.Minute
	srv.AddConsumer("ck", ovhtest.DNSRules...)
	var cred credential
	if err := testClient(srv, "ck").Do(bg, http.MethodGet, "/auth/currentCredential", nil, &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Status != "validated" || len(cred.Rules) != 5 {
		t.Fatalf("%+v", cred)
	}
	if srv.Requests[0] != "GET /1.0/auth/time" {
		t.Fatalf("clock not synced first: %v", srv.Requests)
	}
}

func TestErrors(t *testing.T) {
	srv := ovhtest.New()
	defer srv.Close()
	srv.AddConsumer("ck", ovhtest.Rule{Method: "GET", Path: "/auth/*"})
	c := testClient(srv, "ck")
	err := c.Do(bg, http.MethodGet, "/domain/zone", nil, nil)
	if err == nil || err.Error() != "ovh: HTTP 403: This call has not been granted (run gfs auth login ovh://eu)" {
		t.Fatal(err)
	}
	srv.Fail["GET /1.0/auth/currentCredential"] = 404
	if err := c.Do(bg, http.MethodGet, "/auth/currentCredential", nil, nil); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatal(err)
	}
	delete(srv.Fail, "GET /1.0/auth/currentCredential")
	bad := NewClient(srv.URL+"/1.0", Creds{AppKey: srv.AppKey, AppSecret: "wrong", ConsumerKey: "ck"})
	if err := bad.Do(bg, http.MethodGet, "/auth/currentCredential", nil, nil); err == nil || !strings.Contains(err.Error(), "Invalid signature") {
		t.Fatal(err)
	}
}

// 429 is waited out for any method.
func TestRateLimit(t *testing.T) {
	srv := ovhtest.New()
	defer srv.Close()
	srv.AddConsumer("ck", ovhtest.DNSRules...)
	c := testClient(srv, "ck")
	var waits []string
	c.OnWait = func(m string) { waits = append(waits, m) }
	srv.RateLimit = 2
	if err := c.Do(bg, http.MethodGet, "/auth/currentCredential", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || !strings.HasPrefix(waits[0], "Rate limited by OVH") {
		t.Fatal(waits)
	}
}

// At most MaxInFlight requests run at once, whatever the callers do.
func TestInFlightLimit(t *testing.T) {
	var cur, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0/auth/time" {
			fmt.Fprint(w, time.Now().Unix())
			return
		}
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL+"/1.0", Creds{AppKey: "a", AppSecret: "s", ConsumerKey: "c"})
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() { defer wg.Done(); c.Do(bg, http.MethodGet, "/x", nil, nil) }()
	}
	wg.Wait()
	if peak.Load() > MaxInFlight || peak.Load() < 2 {
		t.Fatalf("peak %d", peak.Load())
	}
}
