package confluence

import (
	"net/url"
	"os"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

//	GFS_CORPUS_URL=confluence://<host>/<SCRATCHSPACE> GFS_CONFLUENCE_EMAIL=... GFS_CONFLUENCE_TOKEN=... \
//	  go test ./internal/adapter/confluence/ -run Corpus -v
//
// WARNING: writes a new version of every page in the space.
func TestCorpusRoundTrip(t *testing.T) {
	raw := os.Getenv("GFS_CORPUS_URL")
	if raw == "" {
		t.Skip("set GFS_CORPUS_URL to a scratch space to run the corpus check")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(r *adapter.Resource) string {
		c := r.Root.Clone()
		c.DelAttr("version")
		c.DelAttr("updated")
		canon.Normalize(c, pageSchema)
		return xmltree.Print(c, 0)
	}
	for _, r := range l.Resources {
		before := canonical(&r)
		res := s.Apply(bg, adapter.ApplyRequest{Local: &r, Base: &r, Lock: r.Version,
			Actions: []adapter.Action{{Verb: "update", Group: "body"}}, IDByPath: func(string) (string, bool) { return "", false }})
		if res[0].Err != nil {
			t.Errorf("%s: push: %v", r.Path, res[0].Err)
			continue
		}
		after, err := s.Fetch(bg, r.ID)
		if err != nil {
			t.Fatalf("%s: fetch: %v", r.Path, err)
		}
		if got := canonical(after); got != before {
			t.Errorf("%s changed after an unchanged push:\n--- before\n%s\n--- after\n%s", r.Path, before, got)
		}
	}
	t.Logf("%d pages round-tripped", len(l.Resources))
}
