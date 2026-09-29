package confluence

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

var t0 = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// clocked shares one settable clock between the fake and the session.
func clocked(srv *cftest.Server, sess *session) func(time.Duration) {
	now := t0
	clock := func() time.Time { return now }
	srv.Clock, sess.now = clock, clock
	return func(d time.Duration) { now = now.Add(d) }
}

var pageGet = regexp.MustCompile(`^GET /wiki/api/v2/pages/[^/]+$`)

func bodyFetches(reqs []string) int {
	n := 0
	for _, r := range reqs {
		if pageGet.MatchString(r) {
			n++
		}
	}
	return n
}

func full(l adapter.Listing) map[string]bool {
	m := map[string]bool{}
	for _, r := range l.Resources {
		if r.Root != nil {
			m[r.ID] = true
		}
	}
	return m
}

func TestListCursorAndStubs(t *testing.T) {
	srv, sess := engSpace(t)
	advance := clocked(srv, sess)
	l0, err := sess.List(bg, "")
	if err != nil || l0.Cursor != "2026-09-29T12:00:00Z" || len(l0.Resources) != 3 || len(full(l0)) != 0 {
		t.Fatalf("%+v %v", l0, err)
	}
	advance(5 * time.Minute)
	srv.TakeRequests()
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil || !l1.Full || len(l1.Resources) != 3 || len(full(l1)) != 0 {
		t.Fatalf("quiet listing must be all stubs: %+v %v", l1, err)
	}
	if n := bodyFetches(srv.TakeRequests()); n != 0 {
		t.Fatalf("%d page fetches on a quiet listing", n)
	}
	for _, r := range l1.Resources {
		if r.ID == "98120" && (r.Version != "1" || r.Path != "eng/Home/Architecture.xml") {
			t.Fatalf("stub %+v", r)
		}
	}
	if l1.Cursor != "2026-09-29T12:05:00Z" {
		t.Fatal(l1.Cursor)
	}
}

func TestListRefetchesPagesWithNewCommentsAndAttachments(t *testing.T) {
	srv, sess := engSpace(t) // filter=ENG: the scoped CQL shape
	advance := clocked(srv, sess)
	l0, _ := sess.List(bg, "")
	advance(2 * time.Minute)
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>new</p>", CreatedAt: srv.Stamp()})
	srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "x.png", CreatedAt: srv.Stamp()})
	advance(3 * time.Minute)
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := full(l1); !got["98120"] || !got["98130"] || got["98001"] {
		t.Fatalf("full resources: %v", got)
	}
}

func TestListIgnoresChangesBeforeWindow(t *testing.T) {
	srv, sess := engSpace(t)
	advance := clocked(srv, sess)
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>old</p>", CreatedAt: t0.Add(-20 * time.Minute).Format("2006-01-02T15:04:05.000Z")})
	l0, _ := sess.List(bg, "")
	advance(5 * time.Minute) // window 15m reaches back to t0-10m
	l1, _ := sess.List(bg, l0.Cursor)
	if len(full(l1)) != 0 {
		t.Fatalf("%v", full(l1))
	}
}

func TestListWholeSiteSearch(t *testing.T) {
	srv := site(t)
	sess := open(t, srv, selection{typ: "global"}) // no filter: the unscoped CQL shape
	advance := clocked(srv, sess)
	l0, _ := sess.List(bg, "")
	srv.AddComment(cftest.Comment{PageID: "82001", CreatedAt: srv.Stamp()})
	srv.AddComment(cftest.Comment{PageID: "84001", CreatedAt: srv.Stamp()}) // archived space: not in the tree
	advance(time.Minute)
	l1, err := sess.List(bg, l0.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := full(l1); len(got) != 1 || !got["82001"] {
		t.Fatalf("%v", got)
	}
}

// An unreadable cursor is a full listing: every page, no change search.
func TestListUnreadableCursorListsEverything(t *testing.T) {
	srv, sess := engSpace(t)
	srv.TakeRequests()
	l, err := sess.List(bg, "garbage")
	if err != nil || len(l.Resources) != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	for _, r := range srv.TakeRequests() {
		if strings.Contains(r, "/content/search") {
			t.Fatalf("searched with an unreadable cursor: %s", r)
		}
	}
}

func TestListFailsWhenASpaceFails(t *testing.T) {
	srv := site(t)
	sess := open(t, srv, selection{typ: "global"})
	srv.Fail = map[string]int{"GET /wiki/api/v2/spaces/200/pages": 403}
	_, err := sess.List(bg, t0.Format(time.RFC3339))
	if err == nil || !strings.Contains(err.Error(), "space OPS") {
		t.Fatalf("got %v", err)
	}
}

func TestListSearchFailureSuggestsFull(t *testing.T) {
	srv, sess := engSpace(t)
	srv.Fail = map[string]int{"GET /wiki/rest/api/content/search": 400}
	_, err := sess.List(bg, t0.Format(time.RFC3339))
	if err == nil || !strings.Contains(err.Error(), "gfs pull --full") {
		t.Fatalf("got %v", err)
	}
}

func TestSearchWindow(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{{0, 10}, {30 * time.Second, 11}, {5 * time.Minute, 15}, {5*time.Minute + time.Second, 16}, {-2 * time.Minute, 10}}
	for _, c := range cases {
		if got := searchWindow(t0, t0.Add(c.d)); got != c.want {
			t.Errorf("%v: got %d, want %d", c.d, got, c.want)
		}
	}
}
