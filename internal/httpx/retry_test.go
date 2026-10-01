package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func retrier(waits *[]time.Duration) *Retrier {
	return &Retrier{Service: "OVH", Now: time.Now, OnWait: func(string) {},
		Sleep: func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }}
}

// 429 is retried for any method after Retry-After; 503 only for reads.
func TestSendRetries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case calls == 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case calls == 2 && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	r := retrier(&waits)
	resp, err := r.Send(context.Background(), srv.Client(), false, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil || resp.StatusCode != 200 || calls != 3 || waits[0] != 7*time.Second {
		t.Fatalf("GET: %v %v calls=%d waits=%v", resp, err, calls, waits)
	}
	calls, waits = 1, nil // next answer is 503 for a GET only
	resp, _ = r.Send(context.Background(), srv.Client(), false, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	})
	if resp.StatusCode != 200 || len(waits) != 0 {
		t.Fatalf("POST: %d waits=%v", resp.StatusCode, waits)
	}
}

// Pause is for services that report limits in the body (ClouDNS): it waits
// with backoff until MaxAttempts or MaxTotalWait.
func TestPause(t *testing.T) {
	var waits []time.Duration
	r := retrier(&waits)
	var waited time.Duration
	n := 0
	for attempt := 1; ; attempt++ {
		if err := r.Pause(context.Background(), attempt, &waited, nil, "Rate limited by ClouDNS"); err != nil {
			if !strings.Contains(err.Error(), "gave up") {
				t.Fatal(err)
			}
			break
		}
		n++
	}
	if n != MaxAttempts-1 || waits[0] < time.Second || waits[0] > 1250*time.Millisecond {
		t.Fatalf("%d pauses, waits %v", n, waits)
	}
}
