package confluence

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

// retrying returns a client whose waits are recorded instead of slept.
func retrying(t *testing.T) (*cftest.Server, *client, *[]time.Duration, *[]string) {
	t.Helper()
	s := cftest.New()
	t.Cleanup(s.Close)
	s.AddSpace("ENG", "100")
	s.AddPage(cftest.Page{ID: "1", Title: "A", SpaceID: "100", Storage: "<p>a</p>"})
	c := newClient(target{base: s.URL, email: "me@x.com", token: "t"})
	var waits []time.Duration
	var notes []string
	c.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	c.onWait = func(msg string) { notes = append(notes, msg) }
	return s, c, &waits, &notes
}

func getPage(c *client) error {
	return c.do(bg, http.MethodGet, "/wiki/api/v2/pages/1", nil, nil)
}

func TestRetry429HonoursRetryAfter(t *testing.T) {
	s, c, waits, notes := retrying(t)
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 429, Times: 2, RetryAfter: "3"}}
	if err := getPage(c); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 2 || (*waits)[0] != 3*time.Second || (*waits)[1] != 3*time.Second {
		t.Fatalf("waits %v", *waits)
	}
	if len(*notes) != 2 || !strings.Contains((*notes)[0], "Rate limited by Confluence, retrying in 3s") {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	s, c, waits, _ := retrying(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 429, Times: 1, RetryAfter: now.Add(7 * time.Second).Format(http.TimeFormat)}}
	if err := getPage(c); err != nil || len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("%v %v", *waits, err)
	}
}

func TestRetryBacksOffWithoutHeader(t *testing.T) {
	s, c, waits, notes := retrying(t)
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 503, Times: 3}}
	if err := getPage(c); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 3 {
		t.Fatalf("waits %v", *waits)
	}
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if w := (*waits)[i]; w < base || w > base+base/4 {
			t.Errorf("wait %d = %v, want %v plus up to 25%% jitter", i, w, base)
		}
	}
	if !strings.Contains((*notes)[0], "HTTP 503") {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryWritesOnlyOn429(t *testing.T) {
	s, c, _, _ := retrying(t)
	body := map[string]any{"spaceId": "100", "status": "current", "title": "New", "body": storageBody("<p>n</p>")}
	s.Failures = map[string]*cftest.Failure{"POST /wiki/api/v2/pages": {Status: 429, Times: 1}}
	if err := c.do(bg, http.MethodPost, "/wiki/api/v2/pages", body, nil); err != nil {
		t.Fatalf("POST must be retried after 429: %v", err)
	}
	s.TakeRequests()
	s.Failures = map[string]*cftest.Failure{"POST /wiki/api/v2/pages": {Status: 503, Times: 1}}
	body["title"] = "Other"
	var ae *APIError
	if err := c.do(bg, http.MethodPost, "/wiki/api/v2/pages", body, nil); !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("POST must not be retried after 503: %v", err)
	}
	if n := len(s.TakeRequests()); n != 1 {
		t.Fatalf("%d POSTs sent", n)
	}
}

func TestRetryGivesUp(t *testing.T) {
	s, c, waits, _ := retrying(t)
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 429, Times: 100}}
	var ae *APIError
	if err := getPage(c); !errors.As(err, &ae) || ae.Status != 429 {
		t.Fatalf("got %v", err)
	}
	if len(*waits) != maxAttempts-1 {
		t.Fatalf("%d waits, want %d", len(*waits), maxAttempts-1)
	}
}

func TestRetryGivesUpPastTotalWait(t *testing.T) {
	s, c, waits, _ := retrying(t)
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 429, Times: 100, RetryAfter: "200"}}
	if err := getPage(c); err == nil {
		t.Fatal("want an error")
	}
	if len(*waits) != 1 {
		t.Fatalf("waits %v: a second 200s wait would pass the %v cap", *waits, maxTotalWait)
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	s, c, _, _ := retrying(t)
	s.Failures = map[string]*cftest.Failure{"GET /wiki/api/v2/pages/1": {Status: 429, Times: 5}}
	ctx, cancel := context.WithCancel(bg)
	c.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	if err := c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/1", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestDownloadRetries429(t *testing.T) {
	s, c, waits, _ := retrying(t)
	s.AddAttachment(cftest.Attachment{ID: "att1", PageID: "1", Title: "a.bin", Data: []byte("bytes")})
	s.Failures = map[string]*cftest.Failure{"GET /media/att1": {Status: 429, Times: 1, RetryAfter: "1"}}
	var b strings.Builder
	if _, err := c.download(bg, "/wiki/download/attachments/1/a.bin", &b); err != nil || b.String() != "bytes" || len(*waits) != 1 {
		t.Fatalf("%q %v %v", b.String(), *waits, err)
	}
}
