package jira

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// step is one <transition> of a <transitions> block (jira chain spec §2):
// its target status, optional transition name, and field elements.
type step struct {
	to, name string
	node     *xmltree.Node
}

// chainOf reads the <transitions> block of an issue file and checks it
// against <status>; nil without a block.
func chainOf(local *xmltree.Node) ([]step, error) {
	block := child(local, "transitions")
	if block == nil {
		return nil, nil
	}
	var steps []step
	seen := map[string]bool{}
	for _, n := range block.ChildrenNamed("transition") {
		to, _ := attr(n, "to")
		to = strings.TrimSpace(to)
		if to == "" {
			return nil, errors.New(`<transition> needs to="<status>"`)
		}
		if seen[strings.ToLower(to)] {
			return nil, fmt.Errorf("%q appears twice in <transitions>; split the round trip over two commits", to)
		}
		seen[strings.ToLower(to)] = true
		for _, c := range n.Children {
			switch {
			case c.Kind != xmltree.Element:
			case c.Name == "comment":
				return nil, errors.New("<comment> cannot go in a <transition>; add it to the issue")
			case c.Name != "field" && elemFields[c.Name] == "":
				return nil, fmt.Errorf("<transition to=%q>: <%s> is not an issue field", to, c.Name)
			}
		}
		name, _ := attr(n, "name")
		steps = append(steps, step{to: to, name: strings.TrimSpace(name), node: n})
	}
	if len(steps) == 0 {
		return nil, errors.New(`<transitions> needs at least one <transition to="…">`)
	}
	last := steps[len(steps)-1]
	if st := textOf(child(local, "status")); !strings.EqualFold(st, last.to) {
		return nil, fmt.Errorf("<status> %s does not match the last <transition to=%q>", st, last.to)
	}
	return steps, nil
}

// resumeAt is the first step to run when the issue is at status current:
// the one after the step that leads there, else the first (§4.1).
func resumeAt(steps []step, current string) int {
	for i, st := range steps {
		if strings.EqualFold(st.to, current) {
			return i + 1
		}
	}
	return 0
}

// describeChain is the status action's detail for a file with a block.
func describeChain(root *xmltree.Node) string {
	var via []string
	for _, n := range child(root, "transitions").ChildrenNamed("transition") {
		to, _ := attr(n, "to")
		via = append(via, to)
	}
	if len(via) < 2 {
		return "transition to " + textOf(child(root, "status"))
	}
	return "transition via " + strings.Join(via[:len(via)-1], ", ") + " to " + via[len(via)-1]
}

// stepFields encodes a step's elements as field values (§2).
func (s *session) stepFields(ctx context.Context, ic *issueCtx, enc encoder, st step) (map[string]any, error) {
	out := map[string]any{}
	for _, c := range st.node.Children {
		if c.Kind != xmltree.Element {
			continue
		}
		var id string
		var v any
		var err error
		if c.Name == "field" {
			id, _ = c.Attr("id")
			if m, ok := ic.set[id]; ok {
				v, err = enc.field(ctx, m, c, nil)
			} else {
				err = fmt.Errorf("<field id=%q> is not a field of %s", id, ic.p.Key)
			}
		} else {
			id = elemFields[c.Name]
			v, err = enc.system(ctx, c.Name, st.node, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("<transition to=%q>: %w", st.to, err)
		}
		out[id] = v
	}
	return out, nil
}

// planStep chooses st's transition among ts and its fields: the step's own,
// each on the screen, then the screen's required ones from the body (§4.2).
func (s *session) planStep(ctx context.Context, ic *issueCtx, enc encoder, ts []apiTransition, st step, own map[string]any, body editInput) (apiTransition, map[string]any, error) {
	want := st.to
	if st.name != "" {
		want = st.name
	}
	tr, err := chooseTransition(ts, want)
	if err != nil {
		return tr, nil, err
	}
	if !strings.EqualFold(tr.To.Name, st.to) {
		return tr, nil, fmt.Errorf("transition %q leads to %q, not %q", tr.Name, tr.To.Name, st.to)
	}
	fields := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(own)) {
		if _, on := tr.Fields[id]; !on {
			return tr, nil, fmt.Errorf("%s is not on the screen of transition %q", elemName(id), tr.Name)
		}
		fields[id] = own[id]
	}
	for _, id := range slices.Sorted(maps.Keys(tr.Fields)) {
		if _, set := fields[id]; set || id == "comment" || !tr.Fields[id].Required {
			continue
		}
		v, present, err := s.currentValue(ctx, ic, enc, id, body)
		if err != nil {
			return tr, nil, err
		}
		if !present {
			return tr, nil, fmt.Errorf("transition %q requires %s; add it", tr.Name, elemName(id))
		}
		fields[id] = v
	}
	return tr, fields, nil
}

// runChain runs steps from status from, each planned against the
// transitions available when it runs (§4.1–4.4). It returns the statuses
// passed through, from first, and whether any step ran.
func (s *session) runChain(ctx context.Context, ic *issueCtx, enc encoder, steps []step, own []map[string]any, from string, body editInput) ([]string, bool, error) {
	path := []string{from}
	for k := resumeAt(steps, from); k < len(steps); k++ {
		ts, err := s.transitions(ctx, ic.id)
		var tr apiTransition
		var fields map[string]any
		if err == nil {
			tr, fields, err = s.planStep(ctx, ic, enc, ts, steps[k], own[k], body)
		}
		if err == nil {
			err = s.postTransition(ctx, ic, tr, fields)
		}
		if err != nil {
			return path, len(path) > 1, stopped(path[len(path)-1], k, steps, err)
		}
		path = append(path, tr.To.Name)
	}
	return path, len(path) > 1, nil
}

func stopped(at string, k int, steps []step, err error) error {
	return fmt.Errorf("stopped at %q (step %d of %d, to %q): %w", at, k+1, len(steps), steps[len(steps)-1].to, err)
}

// chainActs is the indexes of the status and transitions actions.
func (p *editPlan) chainActs() []int {
	var out []int
	for _, i := range []int{p.status, p.stepsIdx} {
		if i >= 0 {
			out = append(out, i)
		}
	}
	return out
}

// planChain checks the block, encodes every step's fields and plans the
// first step to run against the live transitions (§2, §5).
func (s *session) planChain(ctx context.Context, ic *issueCtx, plan *editPlan) {
	fail := func(err error) {
		for _, i := range plan.chainActs() {
			plan.errs[i] = err
		}
	}
	if plan.status < 0 {
		fail(errors.New("<transitions> needs a <status> change"))
		return
	}
	steps, err := chainOf(plan.in.local)
	if err != nil {
		fail(err)
		return
	}
	for _, st := range steps {
		own, err := s.stepFields(ctx, ic, plan.enc, st)
		if err != nil {
			fail(err)
			return
		}
		plan.own = append(plan.own, own)
	}
	plan.steps, plan.start = steps, resumeAt(steps, plan.from)
	if plan.start == len(steps) {
		return
	}
	ts, err := s.transitions(ctx, ic.id)
	if err == nil {
		_, _, err = s.planStep(ctx, ic, plan.enc, ts, steps[plan.start], plan.own[plan.start], plan.in)
	}
	if err != nil {
		fail(stopped(plan.from, plan.start, steps, err))
	}
}

// applyChain runs the chain, then the body edit when the chain completed.
func (s *session) applyChain(ctx context.Context, ic *issueCtx, plan *editPlan, out []adapter.Result) {
	reached, detail := plan.from, ""
	var err error
	partial := false
	if plan.status >= 0 {
		err = plan.errs[plan.status]
	}
	if plan.steps != nil && err == nil {
		var path []string
		path, partial, err = s.runChain(ctx, ic, plan.enc, plan.steps, plan.own, plan.from, plan.in)
		reached = path[len(path)-1]
		if len(path) > 1 {
			detail = strings.Join(path, " -> ")
		}
	}
	if err == nil && plan.stepsIdx >= 0 {
		err = plan.errs[plan.stepsIdx]
	}
	for _, i := range plan.chainActs() {
		out[i].Err, out[i].Code, out[i].Partial = err, atlassian.Code(err), partial && err != nil
		if i == plan.status {
			out[i].Detail = detail
		}
	}
	if err != nil {
		held := fmt.Errorf("not sent: the transitions stopped at %q", reached)
		for i := range plan.covers {
			if plan.errs[i] == nil {
				out[i].Err = held
			}
		}
	} else if len(plan.put) > 0 {
		errPut := s.c.Do(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(ic.id), map[string]any{"fields": plan.put}, nil)
		for i, ids := range plan.covers {
			if plan.errs[i] == nil && errPut != nil {
				out[i].Err, out[i].Code = fieldError(errPut, ids), atlassian.Code(errPut)
			}
		}
	}
	for i, e := range plan.errs {
		if e != nil && !slices.Contains(plan.chainActs(), i) {
			out[i].Err = e
		}
	}
}

// chainDetail is how a dry run shows a chain.
func chainDetail(plan *editPlan) string {
	rest := plan.steps[plan.start:]
	if len(rest) == 0 {
		return "already at " + plan.steps[len(plan.steps)-1].to
	}
	path := []string{plan.from}
	for _, st := range rest {
		path = append(path, st.to)
	}
	d := "transition " + strings.Join(path, " -> ")
	if len(rest) > 1 {
		d += fmt.Sprintf(" (step %d checked; the rest when run)", plan.start+1)
	}
	return d
}
