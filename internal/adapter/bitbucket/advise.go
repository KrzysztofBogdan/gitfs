package bitbucket

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var _ adapter.Advisor = (*session)(nil)

// Available says what the account can do to a pull request now (bitbucket
// spec §7.7). Pipeline runs have nothing to do but watch.
func (s *session) Available(ctx context.Context, id string, local *adapter.Resource) (adapter.Advice, error) {
	if _, _, ok := parseRunID(id); ok {
		return adapter.Advice{State: "pipeline run: read-only", Note: "gfs tail " + local.Path + " streams its logs"}, nil
	}
	repo, n, ok := parsePRID(id)
	if !ok {
		return adapter.Advice{}, fmt.Errorf("%s: %w", id, adapter.ErrNotFound)
	}
	res, err := s.fetchPR(ctx, repo, n)
	if err != nil {
		return adapter.Advice{}, err
	}
	root := res.Root
	state := text(child(root, "state"))
	approvals := 0
	for _, name := range []string{"reviewer", "participant"} {
		for _, r := range root.ChildrenNamed(name) {
			if attr(r, "state") == "approved" {
				approvals++
			}
		}
	}
	parts := []string{state, plural(approvals, "approval")}
	counts := map[string]int{}
	for _, b := range root.ChildrenNamed("build") {
		counts[attr(b, "state")]++
	}
	var builds []string
	for _, k := range []struct{ state, word string }{{"FAILED", "failed"}, {"STOPPED", "stopped"}, {"INPROGRESS", "running"}, {"SUCCESSFUL", "passed"}} {
		if c := counts[k.state]; c > 0 {
			builds = append(builds, fmt.Sprintf("%d %s", c, k.word))
		}
	}
	if len(builds) > 0 {
		parts = append(parts, "builds: "+strings.Join(builds, ", "))
	}
	adv := adapter.Advice{State: strings.Join(parts, ", ")}
	if state != "open" {
		return adv, nil
	}
	st := s.strategies[id]
	adv.Items = append(adv.Items,
		adapter.Available{Verb: "merge", Name: strategyList(st), To: "merged"},
		adapter.Available{Verb: "decline", Name: "decline", To: "declined"})
	mine := text(child(root, "my-review"))
	if mine != "approved" {
		adv.Items = append(adv.Items, adapter.Available{Verb: "review", Name: "approve", To: "approved"})
	}
	if mine != "changes_requested" {
		adv.Items = append(adv.Items, adapter.Available{Verb: "review", Name: "request changes", To: "changes_requested"})
	}
	if mine != "none" {
		adv.Items = append(adv.Items, adapter.Available{Verb: "review", Name: "withdraw", To: "none"})
	}
	if local != nil {
		switch ls := child(local.Root, "state"); text(ls) {
		case "merged":
			strategy := attr(ls, "strategy")
			if strategy == "" {
				strategy = "the default strategy"
				if st.def != "" {
					strategy += " (" + st.def + ")"
				}
			}
			adv.Note = "commit will merge with " + strategy
		case "declined":
			adv.Note = "commit will decline the PR"
		}
	}
	return adv, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
