package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type apiTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
	Fields map[string]struct {
		Required      bool              `json:"required"`
		Name          string            `json:"name"`
		AllowedValues []json.RawMessage `json:"allowedValues"`
	} `json:"fields"`
}

// transitions is what the user can do from the issue's status now, with
// each transition's screen fields.
func (s *session) transitions(ctx context.Context, id string) ([]apiTransition, error) {
	var resp struct {
		Transitions []apiTransition `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(id) + "/transitions?expand=transitions.fields"
	if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("transitions: %w", err)
	}
	return resp.Transitions, nil
}

// chooseTransition picks the transition for the new <status> text (jira spec
// §7.3): the one transition to a status of that name, else the one
// transition of that name.
func chooseTransition(ts []apiTransition, want string) (apiTransition, error) {
	var byTo, byName []apiTransition
	for _, t := range ts {
		if strings.EqualFold(t.To.Name, want) {
			byTo = append(byTo, t)
		}
		if strings.EqualFold(t.Name, want) {
			byName = append(byName, t)
		}
	}
	switch {
	case len(byTo) == 1:
		return byTo[0], nil
	case len(byName) == 1:
		return byName[0], nil
	case len(byTo) > 1:
		var names []string
		for _, t := range byTo {
			names = append(names, fmt.Sprintf("%q", t.Name))
		}
		sort.Strings(names)
		return apiTransition{}, fmt.Errorf("several transitions lead to %q: %s; write the transition name in <status> instead",
			want, strings.Join(names, ", "))
	}
	seen := map[string]bool{}
	var reach []string
	for _, t := range ts {
		if !seen[t.To.Name] {
			seen[t.To.Name] = true
			reach = append(reach, fmt.Sprintf("%q", t.To.Name))
		}
	}
	sort.Strings(reach)
	if len(reach) == 0 {
		return apiTransition{}, fmt.Errorf("%q is not reachable: no transitions are available to you from the current status", want)
	}
	return apiTransition{}, fmt.Errorf("%q is not reachable from the current status in one step; reachable: %s", want, strings.Join(reach, ", "))
}

// planTransition fills plan's transition part: the chosen transition, the
// changed fields that travel with it, and the screen's required fields.
func (s *session) planTransition(ctx context.Context, ic *issueCtx, enc encoder, plan *editPlan, localStatus, baseStatus string, req editInput) {
	if strings.EqualFold(localStatus, baseStatus) {
		return // only the case changed: nothing to do
	}
	ts, err := s.transitions(ctx, ic.id)
	if err != nil {
		plan.errs[plan.status] = err
		return
	}
	tr, err := chooseTransition(ts, localStatus)
	if err != nil {
		plan.errs[plan.status] = err
		return
	}
	plan.tr = &tr
	plan.trFields = map[string]any{}
	for id := range tr.Fields {
		if id == "comment" {
			continue // new comments go through the comment API (jira spec §7.3)
		}
		if v, changed := plan.put[id]; changed {
			plan.trFields[id] = v
			delete(plan.put, id)
			continue
		}
		if !tr.Fields[id].Required {
			continue
		}
		v, present, err := s.currentValue(ctx, ic, enc, id, req)
		switch {
		case err != nil:
			plan.errs[plan.status] = err
			return
		case !present:
			plan.errs[plan.status] = fmt.Errorf("transition %q requires %s; add it", tr.Name, elemName(id))
			return
		}
		plan.trFields[id] = v
	}
}

// currentValue is the file's value of field id, for a required screen field
// the user did not change.
func (s *session) currentValue(ctx context.Context, ic *issueCtx, enc encoder, id string, req editInput) (any, bool, error) {
	if e, ok := systemElems[id]; ok {
		if child(req.local, e) == nil {
			return nil, false, nil
		}
		v, err := enc.system(ctx, e, req.local, req.base)
		return v, err == nil && v != nil, err
	}
	n := fieldsByID(req.local)[id]
	m, ok := ic.set[id]
	if n == nil || !ok {
		return nil, false, nil
	}
	v, err := enc.field(ctx, m, n, fieldsByID(req.base)[id])
	return v, err == nil && v != nil, err
}
