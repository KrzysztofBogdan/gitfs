package bitbucket

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func items(a adapter.Advice) string {
	var out []string
	for _, it := range a.Items {
		out = append(out, it.Name+"->"+it.To)
	}
	return strings.Join(out, "; ")
}

func TestAvailable(t *testing.T) {
	srv, s := prSite(t)
	srv.EditPR("api", 1, func(p *bbtest.PR) {
		p.Participants = []bbtest.Participant{{Account: "712020:bo", Role: "REVIEWER", State: "approved"}, {Account: "712020:cy", Role: "PARTICIPANT", State: "approved"}}
	})
	srv.SetStatus("api", srv.PR("api", 1).SourceCommit, bbtest.Status{Key: "p", State: "FAILED", Name: "Pipeline"})
	base, _ := s.Fetch(bg, "api/pr/1")
	adv, err := s.Available(bg, "api/pr/1", base)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "open, 2 approvals, builds: 1 failed" ||
		items(adv) != "merge_commit, squash (default)->merged; decline->declined; approve->approved; request changes->changes_requested" {
		t.Fatalf("%q\n%s", adv.State, items(adv))
	}
	local := base.Root.Clone()
	setText(local, "state", "merged")
	if adv, _ = s.Available(bg, "api/pr/1", &adapter.Resource{Root: local}); adv.Note != "commit will merge with the default strategy (squash)" {
		t.Fatal(adv.Note)
	}
	req := change(t, s, "api/pr/1", func(r *xmltree.Node) { setText(r, "my-review", "approved") })
	s.Apply(bg, req)
	if adv, _ = s.Available(bg, "api/pr/1", base); !strings.Contains(items(adv), "withdraw->none") || strings.Contains(items(adv), "approve->") {
		t.Fatal(items(adv))
	}
	srv.EditPR("api", 1, func(p *bbtest.PR) { p.State = "MERGED" })
	if adv, _ = s.Available(bg, "api/pr/1", base); len(adv.Items) != 0 || !strings.HasPrefix(adv.State, "merged") {
		t.Fatalf("%+v", adv)
	}
}
