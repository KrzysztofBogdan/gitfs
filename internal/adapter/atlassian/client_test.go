package atlassian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var bg = context.Background()

// flaky answers the first times requests with status (and Retry-After, if
// set), then 200 with body.
type flaky struct {
	mu         sync.Mutex
	status     int
	times      int
	retryAfter string
	body       string
	seen       []string // "METHOD body" per request
}

func (f *flaky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Method+" "+string(b))
	if f.times > 0 {
		f.times--
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		http.Error(w, `{"errorMessages":["slow down"]}`, f.status)
		return
	}
	io.WriteString(w, f.body)
}

// retrying returns a client for f whose waits and notices are recorded.
func retrying(t *testing.T, f *flaky) (*Client, *[]time.Duration, *[]string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(Target{Base: srv.URL, Email: "me@x.com", Token: "t"}, "Jira")
	var waits []time.Duration
	var notes []string
	c.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	c.OnWait = func(msg string) { notes = append(notes, msg) }
	return c, &waits, &notes
}

func TestRetry429HonoursRetryAfter(t *testing.T) {
	f := &flaky{status: 429, times: 2, retryAfter: "3", body: `{"displayName":"Jay"}`}
	c, waits, notes := retrying(t, f)
	var out struct{ DisplayName string }
	if err := c.Do(bg, "GET", "/rest/api/3/myself", nil, &out); err != nil || out.DisplayName != "Jay" {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(*waits, []time.Duration{3 * time.Second, 3 * time.Second}) {
		t.Fatalf("waits %v", *waits)
	}
	if len(*notes) != 2 || (*notes)[0] != "Rate limited by Jira, retrying in 3s…" {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	c, waits, _ := retrying(t, &flaky{status: 429, times: 1, retryAfter: now.Add(7 * time.Second).Format(http.TimeFormat)})
	c.Now = func() time.Time { return now }
	if err := c.Do(bg, "GET", "/x", nil, nil); err != nil || !reflect.DeepEqual(*waits, []time.Duration{7 * time.Second}) {
		t.Fatalf("%v %v", *waits, err)
	}
}

func TestRetryBacksOffWithoutHeader(t *testing.T) {
	c, waits, notes := retrying(t, &flaky{status: 503, times: 3})
	if err := c.Do(bg, "GET", "/x", nil, nil); err != nil {
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
	if !strings.Contains((*notes)[0], "Jira unavailable (HTTP 503)") {
		t.Fatalf("notes %q", *notes)
	}
}

func TestRetryWritesOnlyOn429(t *testing.T) {
	f := &flaky{status: 429, times: 1}
	c, _, _ := retrying(t, f)
	if err := c.Do(bg, "POST", "/x", map[string]int{"a": 1}, nil); err != nil {
		t.Fatalf("POST must be retried after 429: %v", err)
	}
	if !reflect.DeepEqual(f.seen, []string{`POST {"a":1}`, `POST {"a":1}`}) {
		t.Fatalf("the body must be sent again: %q", f.seen)
	}
	f.status, f.times, f.seen = 503, 1, nil
	if err := c.Do(bg, "POST", "/x", nil, nil); Code(err) != "503" || len(f.seen) != 1 {
		t.Fatalf("POST must not be retried after 503: %v, %d sent", err, len(f.seen))
	}
}

func TestRetryGivesUp(t *testing.T) {
	c, waits, _ := retrying(t, &flaky{status: 429, times: 100})
	err := c.Do(bg, "GET", "/x", nil, nil)
	if Code(err) != "429" || !strings.Contains(err.Error(), "jira: HTTP 429: slow down") || len(*waits) != MaxAttempts-1 {
		t.Fatalf("%v after %d waits", err, len(*waits))
	}
}

func TestRetryGivesUpPastTotalWait(t *testing.T) {
	c, waits, _ := retrying(t, &flaky{status: 429, times: 100, retryAfter: "200"})
	if err := c.Do(bg, "GET", "/x", nil, nil); err == nil || len(*waits) != 1 {
		t.Fatalf("waits %v: a second 200s wait would pass the %v cap", *waits, MaxTotalWait)
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	c, _, _ := retrying(t, &flaky{status: 429, times: 5})
	ctx, cancel := context.WithCancel(bg)
	c.Sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	if err := c.Do(ctx, "GET", "/x", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestAPIErrors(t *testing.T) {
	cases := []struct {
		product string
		status  int
		body    string
		want    string
		fields  map[string]string
	}{
		{"Confluence", 400, `{"message":"bad title"}`, "confluence: HTTP 400: bad title", nil},
		{"Jira", 400, `{"errorMessages":["one"],"errors":{"summary":"required","customfield_1":"bad"}}`,
			"jira: HTTP 400: one; customfield_1: bad; summary: required", map[string]string{"summary": "required", "customfield_1": "bad"}},
		{"Jira", 403, `{"errorMessage":"not a customer"}`, "jira: HTTP 403: not a customer", nil},
		{"Jira", 500, "boom\n", "jira: HTTP 500: boom", nil},
		{"Confluence", 404, `{"errors":[{"title":"Not Found"}]}`, `confluence: HTTP 404: {"errors":[{"title":"Not Found"}]}`, nil},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}))
		cl := New(Target{Base: srv.URL}, c.product)
		cl.Sleep = func(context.Context, time.Duration) error { return nil }
		err := cl.Do(bg, "POST", "/x", nil, nil)
		srv.Close()
		var ae *APIError
		if !errors.As(err, &ae) || ae.Error() != c.want || !reflect.DeepEqual(ae.Fields, c.fields) {
			t.Errorf("%d %s: got %v (%+v)", c.status, c.body, err, ae)
		}
		if (c.status == 404) != errors.Is(err, adapter.ErrNotFound) {
			t.Errorf("%d: ErrNotFound mapping", c.status)
		}
	}
}

func TestLockAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "me@x.com" || p != "t" || r.Header.Get("X-ExperimentalApi") != "opt-in" {
			t.Errorf("auth or headers: %v", r.Header)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL, Email: "me@x.com", Token: "t"}, "Jira")
	c.Header.Set("X-ExperimentalApi", "opt-in")
	if err := c.Do(bg, "PUT", "/x", nil, nil); !errors.Is(err, adapter.ErrLock) || Code(err) != "409" {
		t.Fatalf("err %v", err)
	}
}

func TestUploadAndDownload(t *testing.T) {
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			if r.Header.Get("X-Atlassian-Token") != "no-check" {
				t.Error("no XSRF header")
			}
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			if h.Filename != "a b.txt" || string(b) != "bytes" || r.FormValue("minorEdit") != "true" {
				t.Errorf("upload %q %q %q", h.Filename, b, r.FormValue("minorEdit"))
			}
			fmt.Fprint(w, `[{"id":"77"}]`)
		case "GET":
			if downloads++; downloads == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			io.WriteString(w, "content")
		}
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL}, "Jira")
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	var out []struct{ ID string }
	if err := c.Upload(bg, "/up", "a b.txt", strings.NewReader("bytes"), map[string]string{"minorEdit": "true"}, &out); err != nil || out[0].ID != "77" {
		t.Fatalf("%v %+v", err, out)
	}
	var b strings.Builder
	if n, err := c.Download(bg, "/down", &b); err != nil || n != 7 || b.String() != "content" {
		t.Fatalf("download retries a 429: %d %v %q", n, err, b.String())
	}
}

func TestVerifyToken(t *testing.T) {
	site := func(routes map[string]int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			code, ok := routes[r.URL.Path]
			if !ok {
				code = 404
			}
			w.WriteHeader(code)
			if code == 200 {
				fmt.Fprint(w, `{"displayName":"Jay","values":[]}`)
			}
		}))
	}
	cases := []struct {
		name   string
		routes map[string]int
		want   string
		code   string
	}{
		{"jira", map[string]int{"/rest/api/3/myself": 200}, "Jay", ""},
		{"confluence only", map[string]int{"/wiki/rest/api/user/current": 200}, "Jay", ""},
		{"customer only", map[string]int{"/rest/servicedeskapi/request": 200}, "me@x.com", ""},
		{"bad token", map[string]int{"/rest/api/3/myself": 401, "/wiki/rest/api/user/current": 401, "/rest/servicedeskapi/request": 401}, "", "401"},
		{"nothing there", map[string]int{}, "", "404"},
	}
	for _, c := range cases {
		srv := site(c.routes)
		name, err := VerifyToken(bg, srv.URL, "me@x.com", "t")
		srv.Close()
		if name != c.want || Code(err) != c.code {
			t.Errorf("%s: got %q %v", c.name, name, err)
		}
	}
}

// Jira names the limit it hit and, without Retry-After, when its window resets
// (developer.atlassian.com/cloud/jira/platform/rate-limiting).
func TestRetryUsesAtlassianRateLimitHeaders(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.Header().Set("RateLimit-Reason", "jira-burst-based")
			w.Header().Set("X-RateLimit-Reset", now.Add(9*time.Second).Format(time.RFC3339))
			w.WriteHeader(429)
		case 2:
			w.Header().Set("Beta-Retry-After", "4")
			w.WriteHeader(429)
		default:
			io.WriteString(w, "{}")
		}
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL}, "Jira")
	c.Now = func() time.Time { return now }
	var waits []time.Duration
	var notes []string
	c.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	c.OnWait = func(msg string) { notes = append(notes, msg) }
	if err := c.Do(bg, "GET", "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{9 * time.Second, 4 * time.Second}) {
		t.Fatalf("waits %v", waits)
	}
	if notes[0] != "Rate limited by Jira (jira-burst-based), retrying in 9s…" || notes[1] != "Rate limited by Jira, retrying in 4s…" {
		t.Fatalf("notes %q", notes)
	}
}

// A read sent as POST (Jira search) is retried on 503 like a GET.
func TestDoReadRetriesUnavailable(t *testing.T) {
	f := &flaky{status: 503, times: 1, body: "{}"}
	c, waits, _ := retrying(t, f)
	if err := c.DoRead(bg, "POST", "/rest/api/3/search/jql", map[string]int{"a": 1}, nil); err != nil || len(*waits) != 1 {
		t.Fatalf("%v %v", err, *waits)
	}
}

// A 401 says how to get a new token.
func TestUnauthorizedHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorMessages":["Client must be authenticated"]}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(Target{Base: srv.URL}, "Jira")
	c.LoginHint = "gfs auth login jira://acme.atlassian.net"
	err := c.Do(context.Background(), http.MethodGet, "/rest/api/3/myself", nil, nil)
	if err == nil || err.Error() != "jira: HTTP 401: Client must be authenticated (run gfs auth login jira://acme.atlassian.net)" {
		t.Fatal(err)
	}
}
