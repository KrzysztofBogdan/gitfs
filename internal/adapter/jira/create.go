package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
)

// createPlan is a new issue before it is sent.
type createPlan struct {
	ic     *issueCtx
	fields map[string]any
	status string // the <status> the file asks for; "" keeps the initial one
}

// skipOnCreate are elements a new file may carry that are not create fields.
var skipOnCreate = map[string]bool{"type": true, "status": true, "creator": true}

// planCreate checks a new file and builds the create request (jira spec §7.4).
func (s *session) planCreate(ctx context.Context, req adapter.ApplyRequest) (*createPlan, error) {
	root := req.Local.Root
	p, err := s.projectFor(req.Local.Path)
	if err != nil {
		return nil, err
	}
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, err
		}
	}
	var names []string
	for _, t := range p.Types {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	typeName := textOf(child(root, "type"))
	if typeName == "" {
		return nil, fmt.Errorf("<type> is required; %s has %s", p.Key, strings.Join(names, ", "))
	}
	t := p.typeNamed(typeName)
	if t == nil {
		return nil, fmt.Errorf("%s has no issue type %q; types: %s", p.Key, typeName, strings.Join(names, ", "))
	}
	ic := &issueCtx{p: p, t: t, set: fieldSet(t)}
	enc := encoder{s: s, project: p.Key}
	fields := map[string]any{"project": map[string]any{"key": p.Key}, "issuetype": map[string]any{"id": t.ID}}
	elems := make([]string, 0, len(elemFields))
	for e := range elemFields {
		elems = append(elems, e)
	}
	sort.Strings(elems)
	for _, e := range elems {
		id := elemFields[e]
		if skipOnCreate[e] || child(root, e) == nil {
			continue
		}
		if _, on := t.Fields[id]; !on {
			return nil, fmt.Errorf("%s is not on the create screen of %s in %s", elemName(id), t.Name, p.Key)
		}
		v, err := enc.system(ctx, e, root, nil)
		if err != nil {
			return nil, err
		}
		fields[id] = v
	}
	custom := fieldsByID(root)
	ids := make([]string, 0, len(custom))
	for id := range custom {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m, on := t.Fields[id]
		if !on {
			return nil, fmt.Errorf("%s is not on the create screen of %s in %s", elemName(id), t.Name, p.Key)
		}
		v, err := enc.field(ctx, m, custom[id], nil)
		if err != nil {
			return nil, err
		}
		fields[id] = v
	}
	var missing []string
	for id, m := range t.Fields {
		if m.Required && fields[id] == nil && id != "reporter" {
			missing = append(missing, elemName(id))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("a new %s in %s needs %s", t.Name, p.Key, strings.Join(missing, ", "))
	}
	return &createPlan{ic: ic, fields: fields, status: textOf(child(root, "status"))}, nil
}

// create makes the issue of a new file, then its status, new comments,
// worklogs, links and attachments (jira spec §7.4). The first result is the
// create; later ones report the extras that failed, and every attachment.
func (s *session) create(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	plan, err := s.planCreate(ctx, req)
	if err != nil {
		return []adapter.Result{{Action: act, Err: err}}
	}
	var resp struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue", map[string]any{"fields": plan.fields}, &resp); err != nil {
		return []adapter.Result{{Action: act, Err: err, Code: atlassian.Code(err)}}
	}
	ic := plan.ic
	ic.id, ic.key = resp.ID, resp.Key
	out := []adapter.Result{{Action: act, ID: resp.ID, Detail: resp.Key}}
	failed := func(a adapter.Action, err error) {
		out = append(out, adapter.Result{Action: a, Err: err, Code: atlassian.Code(err)})
	}
	root := req.Local.Root
	if plan.status != "" {
		if err := s.createStatus(ctx, ic, req, plan.status); err != nil {
			failed(adapter.Action{Verb: "update", Group: "status", Class: "transition"}, err)
		}
	}
	for _, name := range []string{"comment", "worklog", "link"} {
		for n := 1; nthNew(root, name, "id", n) != nil; n++ {
			a := adapter.Action{Verb: "create", Target: fmt.Sprintf("%s[%d]", name, n)}
			if err := s.sub(ctx, ic, req, a); err != nil {
				failed(a, err)
			}
		}
	}
	for _, a := range req.Actions {
		if a.IsAttachment() {
			id, err := s.attachment(ctx, resp.ID, req, a)
			out = append(out, adapter.Result{Action: a, ID: id, Version: "-", Err: err, Code: atlassian.Code(err)})
		}
	}
	return out
}

// createStatus moves a new issue from its initial status to want.
func (s *session) createStatus(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest, want string) error {
	var cur struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"?fields=status", nil, &cur); err != nil {
		return err
	}
	plan := &editPlan{put: map[string]any{}, covers: map[int][]string{}, errs: map[int]error{}, status: 0}
	enc := encoder{s: s, issueKey: ic.key, project: ic.p.Key}
	s.planTransition(ctx, ic, enc, plan, want, cur.Fields.Status.Name, editInput{local: req.Local.Root})
	if err := plan.errs[0]; err != nil {
		return err
	}
	if plan.tr == nil {
		return nil // already there
	}
	return s.sendTransition(ctx, ic, plan)
}

// checkDelete refuses to delete an issue that has sub-tasks (jira spec §7.5).
func (s *session) checkDelete(ctx context.Context, id string) error {
	var is struct {
		Fields struct {
			Subtasks []issueRef `json:"subtasks"`
		} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(id)+"?fields=subtasks", nil, &is); err != nil {
		return err
	}
	if n := len(is.Fields.Subtasks); n > 0 {
		var keys []string
		for _, st := range is.Fields.Subtasks {
			keys = append(keys, st.Key)
		}
		return fmt.Errorf("delete or re-parent its sub-tasks first (%s)", strings.Join(keys, ", "))
	}
	return nil
}

func (s *session) deleteIssue(ctx context.Context, id string) error {
	if err := s.checkDelete(ctx, id); err != nil {
		return err
	}
	return s.c.Do(ctx, http.MethodDelete, "/rest/api/3/issue/"+url.PathEscape(id), nil, nil)
}
