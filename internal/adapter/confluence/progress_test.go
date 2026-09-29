package confluence

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

func record(sess *session) *[]adapter.Progress {
	var got []adapter.Progress
	var r adapter.Reporter = sess
	r.SetProgress(func(p adapter.Progress) { got = append(got, p) })
	return &got
}

func TestListReportsProgress(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	got := record(sess)
	if _, err := sess.List(bg, ""); err != nil {
		t.Fatal(err)
	}
	var pages, fetches []adapter.Progress
	for _, p := range *got {
		switch p.Phase {
		case "pages":
			pages = append(pages, p)
		case "fetch":
			fetches = append(fetches, p)
		}
	}
	if len(pages) != 2 || pages[1].Done != 2 || pages[1].Total != 2 || pages[1].Item != "OPS (1 page)" {
		t.Fatalf("pages: %+v", pages)
	}
	if len(fetches) != 0 {
		t.Fatalf("downloads are the engine's to report: %+v", fetches)
	}
	if last := (*got)[len(*got)-1]; last.Phase != "done" {
		t.Fatalf("last: %+v", last)
	}
}

func TestQuietListReportsNoFetches(t *testing.T) {
	srv, sess := engSpace(t)
	clocked(srv, sess)
	l0, _ := sess.List(bg, "")
	got := record(sess)
	sess.List(bg, l0.Cursor)
	for _, p := range *got {
		if p.Phase == "fetch" {
			t.Fatalf("stubs must not count as fetches: %+v", *got)
		}
	}
}

// A full listing leaves the downloads to the engine, which reports their progress.
func TestFullListingDownloadsNoBodies(t *testing.T) {
	s := site(t)
	sess := open(t, s, selection{typ: "global"})
	s.TakeRequests()
	l, err := sess.List(bg, "")
	if err != nil || len(l.Resources) != 3 || len(full(l)) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
	if n := bodyFetches(s.TakeRequests()); n != 0 {
		t.Fatalf("%d page downloads in a full listing", n)
	}
}
