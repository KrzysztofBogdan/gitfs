# Jira transition chains — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Jira issue file may list `<transitions><transition to="…" name="…">fields</transition>…</transitions>`; commit runs the steps in order, each with its own screen fields, resuming after a stop.

**Architecture:** A new `chain.go` in the Jira adapter parses and validates the block, plans one step against the live transitions, and runs the chain. `planEdit`/`applyEdit` switch to chain mode when the file has a block (body edit after the chain); `create` runs the chain after creating. Engine untouched.

**Tech Stack:** Go; `jira/jtest` fake.

**Spec:** `docs/superpowers/specs/2026-10-06-jira-transition-chain-design.md`

## Global Constraints

- Without a `<transitions>` block, behaviour and messages are unchanged.
- Messages verbatim from spec §2, §4.2–4.4.
- `go vet ./...` and `go test ./...` green before each commit.

---

### Task 1: Parse, validate, resume; schema and describe

**Files:**
- Create: `internal/adapter/jira/chain.go`, `internal/adapter/jira/chain_test.go`
- Modify: `internal/adapter/jira/schema.go` (issue elems), `internal/adapter/jira/relaxng.go` (issue grammar), `internal/adapter/jira/adapter.go` (`Describe`)

**Interfaces — produces:** `type step struct{ to, name string; node *xmltree.Node }`; `chainOf(local *xmltree.Node) ([]step, error)` (nil, nil without a block); `resumeAt(steps []step, current string) int`; `describeChain(root *xmltree.Node) string`.

- [ ] **Step 1: failing test** (`chain_test.go`)

```go
func TestChainOf(t *testing.T) {
	wrap := func(steps string) string {
		return `<issue><status>Closed</status><transitions>` + steps + `</transitions></issue>`
	}
	for x, want := range map[string]string{
		wrap(`<transition to="In Progress"/><transition to="Done"/>`):                                `<status> Closed does not match the last <transition to="Done">`,
		wrap(`<transition/>`):                                                                       `<transition> needs to="<status>"`,
		wrap(`<transition to="In Progress"/><transition to="in progress"/><transition to="Closed"/>`): `"in progress" appears twice in <transitions>; split the round trip over two commits`,
		wrap(`<transition to="Closed"><comment>x</comment></transition>`):                           `<comment> cannot go in a <transition>; add it to the issue`,
		wrap(`<transition to="Closed"><foo/></transition>`):                                         `<transition to="Closed">: <foo> is not an issue field`,
		wrap(``): `<transitions> needs at least one <transition to="…">`,
	} {
		if _, err := chainOf(parse(t, x)); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", x, err, want)
		}
	}
	steps, err := chainOf(parse(t, wrap(`<transition to="In Progress"/><transition to="closed" name="Resolve this issue"><resolution>Done</resolution></transition>`)))
	if err != nil || len(steps) != 2 || steps[1].name != "Resolve this issue" || steps[1].node.Child("resolution") == nil {
		t.Fatalf("%+v %v", steps, err)
	}
	if s, err := chainOf(parse(t, `<issue><status>Closed</status></issue>`)); s != nil || err != nil {
		t.Fatal(s, err)
	}
	for cur, want := range map[string]int{"Open": 0, "in progress": 1, "Closed": 2} {
		if got := resumeAt(steps, cur); got != want {
			t.Errorf("resumeAt %s = %d", cur, got)
		}
	}
	root := parse(t, wrap(`<transition to="In Progress"/><transition to="Closed"/>`))
	if got := describeChain(root); got != "transition via In Progress to Closed" {
		t.Fatal(got)
	}
}
```

- [ ] **Step 2: run** `go test ./internal/adapter/jira -run TestChainOf` — Expected: build fails (undefined).

- [ ] **Step 3: `chain.go`**

```go
package jira

import (
	"errors"
	"fmt"
	"strings"

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
```

(`elemFields` is a `map[string]string`; if its type differs, use the `, ok` form.)

- [ ] **Step 4: schema / grammar / describe**
  - `schema.go`, after `status`: `{Name: "transitions", Kind: schema.Field},`
  - `relaxng.go`, in the issue interleave after the text fields: `w(`        <optional><element name="transitions"><ref name="any"/></element></optional>`)`
  - `adapter.go` `Describe`: in the `status` case, `if child(root, "transitions") != nil { a.Detail = describeChain(root) }`; add `case a.Verb == "update" && a.Group == "transitions": a.Detail = "steps of the transition"` before the generic update case.

- [ ] **Step 5: run** `go test ./internal/adapter/jira/...` — Expected: PASS. If a schema round-trip drops `to=`/`name=` on nested `<transition>`, fix it in the schema (attributes of a Field's children must survive canonicalisation) and add that case to the test.

- [ ] **Step 6: commit** `jira: parse and check <transitions> blocks`

---

### Task 2: Plan and run chains on existing issues

**Files:**
- Modify: `internal/adapter/jira/chain.go`, `edit.go` (`editPlan`, `planEdit`, `checkEditable`, `applyEdit`, `sendTransition`), `transition.go`, `apply.go` (`Check`)
- Test: `internal/adapter/jira/chain_test.go`

**Interfaces — produces:** `(*session).stepFields(ctx, ic, enc, st) (map[string]any, error)`; `(*session).planStep(ctx, ic, enc, ts, st, own, body) (apiTransition, map[string]any, error)`; `(*session).runChain(ctx, ic, enc, steps, own, from, body) ([]string, bool, error)`; `(*session).postTransition(ctx, ic, tr, fields) error`; `editPlan.steps/own/start/stepsIdx/enc/in`.

- [ ] **Step 1: failing tests**

```go
func chainSite(t *testing.T) (*jtest.Server, *session, string) {
	srv, now := site(t)
	supportFlow(srv)
	id := srv.ID("SUP-1")
	srv.Edit(id, func(is *jtest.Issue) { is.Status = "Open" })
	return srv, open(t, srv, now, selection{keys: []string{"SUP"}}, ""), id
}

// withChain sets <status>, a body <priority> and a block.
func withChain(t *testing.T, status, block string) func(*xmltree.Node) {
	return func(r *xmltree.Node) {
		setText(r, "status", status)
		setText(r, "priority", "Low")
		r.Children = append(r.Children, parse(t, "<transitions>"+block+"</transitions>"))
	}
}

func priorityOf(is jtest.Issue) any { p, _ := is.Fields["priority"].(map[string]any); return p["name"] }

func TestTransitionChain(t *testing.T) {
	srv, s, id := chainSite(t)
	posts := func() int { return srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") }
	steps := `<transition to="In Progress"/><transition to="Closed" name="Resolve this issue">%s</transition>`

	// a stop at step 2 keeps the reached status and holds the body edit back
	req, out := change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "")), "status", "priority", "transitions")
	apply(t, s, req, out)
	want := `stopped at "In Progress" (step 2 of 2, to "Closed"): transition "Resolve this issue" requires <resolution>; add it`
	if out[0].Err == nil || out[0].Err.Error() != want || !out[0].Partial || out[2].Err == nil || out[2].Err.Error() != want {
		t.Fatalf("%+v", out)
	}
	if out[1].Err == nil || out[1].Err.Error() != `not sent: the transitions stopped at "In Progress"` {
		t.Fatal(out[1].Err)
	}
	if is := srv.Issue(id); is.Status != "In Progress" || priorityOf(is) == "Low" || posts() != 1 {
		t.Fatalf("%+v %d", is, posts())
	}

	// the retry resumes at step 2, then sends the body edit
	req, out = change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "<resolution>Done</resolution>")), "status", "priority", "transitions")
	apply(t, s, req, out)
	for _, r := range out {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Action.Group, r.Err)
		}
	}
	if out[0].Detail != "In Progress -> Closed" || posts() != 2 {
		t.Fatalf("%q %d", out[0].Detail, posts())
	}
	is := srv.Issue(id)
	if is.Status != "Closed" || is.Fields["resolution"].(map[string]any)["name"] != "Done" || priorityOf(is) != "Low" {
		t.Fatalf("%+v", is)
	}
	lastPost, lastPut := -1, -1
	for i, r := range srv.Requests {
		switch r {
		case "POST /rest/api/3/issue/" + id + "/transitions":
			lastPost = i
		case "PUT /rest/api/3/issue/" + id:
			lastPut = i
		}
	}
	if lastPut < lastPost {
		t.Fatal("the body edit goes after the transitions", srv.Requests)
	}

	// the whole chain from Open
	srv.Edit(id, func(is *jtest.Issue) { is.Status = "Open" })
	req, out = change(t, s, id, withChain(t, "Closed", fmt.Sprintf(steps, "<resolution>Done</resolution>")), "status", "priority", "transitions")
	apply(t, s, req, out)
	if out[0].Err != nil || out[0].Detail != "Open -> In Progress -> Closed" {
		t.Fatalf("%+v", out[0])
	}
}

func TestTransitionChainRefusals(t *testing.T) {
	srv, s, id := chainSite(t)
	posts := func() int { return srv.Count("POST", "/rest/api/3/issue/"+id+"/transitions") }
	cases := []struct{ status, block, want string }{
		{"In Progress", `<transition to="In Progress"><priority>High</priority></transition>`,
			`stopped at "Open" (step 1 of 1, to "In Progress"): <priority> is not on the screen of transition "Start"`},
		{"Closed", `<transition to="Closed" name="Start"/>`,
			`stopped at "Open" (step 1 of 1, to "Closed"): transition "Start" leads to "In Progress", not "Closed"`},
		{"In Progress", `<transition to="In Progress"/><transition to="Closed"/>`,
			`<status> In Progress does not match the last <transition to="Closed">`},
	}
	for _, c := range cases {
		req, out := change(t, s, id, withChain(t, c.status, c.block), "status", "transitions")
		apply(t, s, req, out)
		if out[0].Err == nil || out[0].Err.Error() != c.want || out[0].Partial {
			t.Errorf("%s: %+v", c.block, out[0])
		}
	}
	if posts() != 0 {
		t.Fatal("nothing may move")
	}
	req, out := change(t, s, id, func(r *xmltree.Node) {
		r.Children = append(r.Children, parse(t, `<transitions><transition to="Open"/></transitions>`))
	}, "transitions")
	apply(t, s, req, out)
	if out[0].Err == nil || out[0].Err.Error() != "<transitions> needs a <status> change" {
		t.Fatal(out[0].Err)
	}
}

func TestTransitionChainDryRun(t *testing.T) {
	_, s, id := chainSite(t)
	req, _ := change(t, s, id, withChain(t, "Closed", `<transition to="In Progress"/><transition to="Closed"><resolution>Done</resolution></transition>`), "status", "transitions")
	out := s.Check(bg, req)
	if out[0].Err != nil || out[0].Detail != "transition Open -> In Progress -> Closed (step 1 checked; the rest when run)" {
		t.Fatalf("%+v", out[0])
	}
	req, _ = change(t, s, id, withChain(t, "Closed", `<transition to="In Progress"><assignee>nobody@x.com</assignee></transition><transition to="Closed"/>`), "status", "transitions")
	if out := s.Check(bg, req); out[0].Err == nil || !strings.HasPrefix(out[0].Err.Error(), `<transition to="In Progress">: `) {
		t.Fatalf("%+v", out[0])
	}
}
```

- [ ] **Step 2: run** — Expected: FAIL.

- [ ] **Step 3: `transition.go`** — split `sendTransition`:

```go
// postTransition runs transition tr with its screen fields.
func (s *session) postTransition(ctx context.Context, ic *issueCtx, tr apiTransition, fields map[string]any) error {
	body := map[string]any{"transition": map[string]any{"id": tr.ID}}
	if len(fields) > 0 {
		body["fields"] = fields
	}
	err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/transitions", body, nil)
	if err == nil {
		return nil
	}
	ids := make([]string, 0, len(fields))
	for id := range fields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fieldError(err, ids)
}
```

and `sendTransition` becomes `return s.postTransition(ctx, ic, *plan.tr, plan.trFields)`.

- [ ] **Step 4: `chain.go`** — add

```go
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
```

(imports: `context`, `maps`, `slices`.)

- [ ] **Step 5: `edit.go`**
  - `editPlan` gains
    ```go
    	stepsIdx int              // index of the <transitions> action, -1 if none
    	steps    []step           // the file's <transitions> block, nil without one
    	own      []map[string]any // each step's own fields, encoded
    	start    int              // the first step to run
    	enc      encoder          // for steps planned while the chain runs
    	in       editInput
    ```
    initialised with `stepsIdx: -1`.
  - `planEdit` loop: `case "transitions": plan.stepsIdx = i`. After the loop replace the status block with:
    ```go
    	plan.enc, plan.in = enc, editInput{local, base}
    	switch {
    	case plan.stepsIdx >= 0 || (plan.status >= 0 && child(local, "transitions") != nil):
    		plan.from = textOf(child(base, "status"))
    		s.planChain(ctx, ic, plan)
    	case plan.status >= 0:
    		plan.from = textOf(child(base, "status"))
    		s.planTransition(ctx, ic, enc, plan, textOf(child(local, "status")), plan.from, editInput{local, base})
    	}
    ```
  - `checkEditable`: first case of the switch `case id == "resolution" && plan.steps != nil: plan.errs[i] = errors.New("<resolution> is not on the edit screen; put it in the <transition> that sets it")`.
  - `applyEdit`: at the top `if plan.steps != nil || plan.stepsIdx >= 0 { s.applyChain(ctx, ic, plan, out); return }`.

- [ ] **Step 6: `chain.go`** — planning and applying:

```go
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
	err, partial := plan.errs[plan.status], false
	if plan.status >= 0 && err == nil {
		var path []string
		path, partial, err = s.runChain(ctx, ic, plan.enc, plan.steps, plan.own, plan.from, plan.in)
		reached = path[len(path)-1]
		if len(path) > 1 {
			detail = strings.Join(path, " -> ")
		}
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
```

- [ ] **Step 7: `apply.go` `Check`** — after `if plan.tr != nil …`: `if plan.steps != nil && plan.errs[plan.status] == nil { out[plan.status].Detail = chainDetail(plan) }`.

- [ ] **Step 8: run** `go test ./internal/adapter/jira/...` — Expected: PASS.

- [ ] **Step 9: commit** `jira: run a <transitions> chain on an existing issue, resuming after a stop`

---

### Task 3: Chains on new issues

**Files:** Modify `internal/adapter/jira/create.go` (`createPlan`, `planCreate`, `create`, `createStatus`). Test: `chain_test.go`.

- [ ] **Step 1: failing test**

```go
func TestCreateWithChain(t *testing.T) {
	srv, s, _ := chainSite(t)
	req := newFile(t, "sup/Licence stuck.xml", `<issue><type>Support</type><summary>Licence stuck</summary><status>Closed</status>
<transitions><transition to="In Progress"/><transition to="Closed" name="Resolve this issue"><resolution>Done</resolution></transition></transitions></issue>`)
	out := s.create(bg, req)
	if len(out) != 1 || out[0].Err != nil {
		t.Fatalf("%+v", out)
	}
	if is := srv.Issue(out[0].ID); is.Status != "Closed" {
		t.Fatalf("%+v", is)
	}
	req = newFile(t, "sup/Second.xml", `<issue><type>Support</type><summary>Second</summary><status>Closed</status>
<transitions><transition to="In Progress"/><transition to="Closed" name="Resolve this issue"/></transitions></issue>`)
	out = s.create(bg, req)
	if len(out) != 3 || out[0].Err != nil || out[1].Action.Group != "status" || out[2].Action.Group != "transitions" ||
		!strings.HasPrefix(out[1].Err.Error(), `stopped at "In Progress" (step 2 of 2`) {
		t.Fatalf("%+v", out)
	}
	if _, err := s.planCreate(bg, newFile(t, "sup/Bad.xml", `<issue><type>Support</type><summary>x</summary><status>Open</status><transitions><transition to="Closed"/></transitions></issue>`)); err == nil ||
		err.Error() != `<status> Open does not match the last <transition to="Closed">` {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: run** — FAIL.

- [ ] **Step 3: implement** — `createPlan` gains `steps []step; own []map[string]any`. In `planCreate`, before building fields: `steps, err := chainOf(root)`; error → return it; encode `own` with `s.stepFields(ctx, ic, enc, st)` for each step. In `create`, call `s.createStatus(ctx, ic, req, plan)`; on error append the `status` failure and, when `plan.steps != nil`, a second failure `adapter.Action{Verb: "update", Group: "transitions"}`. `createStatus(ctx, ic, req, plan *createPlan)` reads the current status as now, then with `plan.steps != nil` returns the error of `s.runChain(ctx, ic, enc, plan.steps, plan.own, cur, editInput{local: req.Local.Root})`; otherwise today's single transition to `plan.status`.

- [ ] **Step 4: run** `go test ./internal/adapter/jira/...` — PASS.

- [ ] **Step 5: commit** `jira: a new issue may carry a <transitions> chain`

---

### Task 4: End to end and docs

**Files:** Test `internal/cli/jira_e2e_test.go`; modify `docs/jira.md` ("Status and transitions", "What each change does").

- [ ] **Step 1: test**

```go
// A workflow without To Do -> Done: the file lists the steps; a stop keeps
// the block and the next commit resumes.
func TestJiraTransitionChain(t *testing.T) {
	srv := jiraSite(t)
	srv.SetFields("GEN", "1",
		jtest.Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		jtest.Field{ID: "issuetype", Name: "Issue Type", Type: "issuetype", Required: true},
		jtest.Field{ID: "resolution", Name: "Resolution", Type: "resolution"})
	srv.EditHidden = []string{"resolution"}
	srv.SetWorkflow("GEN", "1", jtest.Workflow{Name: "Dev flow",
		Statuses: []jtest.Status{{ID: "1", Name: "To Do", Category: "new"}, {ID: "2", Name: "In Progress", Category: "indeterminate"},
			{ID: "3", Name: "Done", Category: "done"}},
		Transitions: []jtest.Transition{
			{ID: "21", Name: "Start", To: "In Progress", From: []string{"To Do"}},
			{ID: "31", Name: "Finish", To: "Done", From: []string{"In Progress"}, Screen: []jtest.ScreenField{{ID: "resolution", Required: true}}},
		}})
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "jira://acme.atlassian.net/GEN?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "gen"))
	file := "gen/GEN-1 Migrate auth.xml"
	replaceIn(t, file, "<status>To Do</status>",
		`<status>Done</status><transitions><transition to="In Progress"/><transition to="Done"/></transitions>`)
	mustContain(t, mustRun(t, 0, "commit", "--dry-run"), "transition To Do -> In Progress -> Done (step 1 checked; the rest when run)")
	mustContain(t, mustRun(t, 1, "commit"), `stopped at "In Progress" (step 2 of 2, to "Done")`)
	if is := srv.Issue(srv.ID("GEN-1")); is.Status != "In Progress" {
		t.Fatal(is.Status)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "<status>Done</status>") || !strings.Contains(string(b), "<transitions>") || !strings.Contains(string(b), "<errors>") {
		t.Fatalf("%s", b)
	}
	replaceIn(t, file, `<transition to="Done"/>`, `<transition to="Done"><resolution>Done</resolution></transition>`)
	mustRun(t, 0, "commit")
	if is := srv.Issue(srv.ID("GEN-1")); is.Status != "Done" {
		t.Fatal(is.Status)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "<transitions>") {
		t.Fatalf("%s", b)
	}
	mustContain(t, mustRun(t, 0, "status"), "nothing to commit")
}
```

(If write-back pretty-prints the block so `<transition to="Done"/>` is not found verbatim, match the written form.)

- [ ] **Step 2: run** `go test ./internal/cli -run JiraTransitionChain` — PASS (fix seams).

- [ ] **Step 3: docs** — in `docs/jira.md` replace the bullet "A status two steps away is refused … never runs a chain of transitions for you." with a sub-section "Several transitions": the example of spec §2, the rules (`to` is the status, `name` picks among several, step fields on that screen only, `<status>` equals the last `to`, body edits after the last step, no status twice), resume and failure, and that the block disappears after success. Add a table row: `<transitions>` added with `<status>` | `transitions in order, then the body edit` | `transition`.

- [ ] **Step 4: full run** `go vet ./... && go test ./...` — PASS.

- [ ] **Step 5: commit** `jira: transition chains end to end; docs`
