package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// issueCtx is what applying changes to one issue file needs to know.
type issueCtx struct {
	id, key string
	p       *projectMeta
	t       *typeMeta // nil when the type has no create screen
	set     map[string]fieldMeta
}

func (s *session) dirList() string {
	var ds []string
	for _, p := range s.sorted() {
		ds = append(ds, p.dir())
	}
	return strings.Join(ds, ", ")
}

// projectFor maps a working path's folder to its project.
func (s *session) projectFor(path string) (*projectMeta, error) {
	dir, _, ok := strings.Cut(path, "/")
	if !ok {
		return nil, fmt.Errorf("issues must live in a project folder (%s)", s.dirList())
	}
	for _, p := range s.projects {
		if p.dir() == dir {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project %q in this tree; gfs does not create projects (projects: %s)", dir, s.dirList())
}

// issueCtx resolves the project, type and field set of res, a file whose
// root carries (for an existing issue) id and key.
func (s *session) issueCtx(ctx context.Context, res *adapter.Resource) (*issueCtx, error) {
	p, err := s.projectFor(res.Path)
	if err != nil {
		return nil, err
	}
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, err
		}
	}
	ic := &issueCtx{id: res.ID, p: p}
	ic.key, _ = res.Root.Attr("key")
	typeName := textOf(child(res.Root, "type"))
	if ic.t = p.typeNamed(typeName); ic.t != nil {
		ic.set = fieldSet(ic.t)
	} else {
		ic.set = projectFieldSet(p)
	}
	return ic, nil
}

// editPlan is the field changes of one file, before anything is sent.
type editPlan struct {
	put      map[string]any   // field id -> value, for PUT /issue
	covers   map[int][]string // action index -> field ids it changes
	errs     map[int]error    // action index -> why it cannot run
	status   int              // index of the <status> action, -1 if none
	editable map[string]bool  // the issue's edit screen (editmeta); nil when not fetched
	from     string           // status before the change
	tr       *apiTransition   // the chosen transition, nil if none
	trFields map[string]any   // field id -> value sent with the transition
}

// editInput is the two versions of the file being applied.
type editInput struct{ local, base *xmltree.Node }

func fieldsByID(root *xmltree.Node) map[string]*xmltree.Node {
	out := map[string]*xmltree.Node{}
	if root == nil {
		return out
	}
	for _, f := range root.ChildrenNamed("field") {
		if id, _ := f.Attr("id"); id != "" {
			out[id] = f
		}
	}
	return out
}

// changedFields is the ids of <field> elements that differ between base and local.
func changedFields(base, local *xmltree.Node) []string {
	b, l := fieldsByID(base), fieldsByID(local)
	seen := map[string]bool{}
	var out []string
	for id := range b {
		seen[id] = true
	}
	for id := range l {
		seen[id] = true
	}
	for id := range seen {
		switch {
		case b[id] == nil || l[id] == nil:
			out = append(out, id)
		case xmltree.Print(b[id], 0) != xmltree.Print(l[id], 0):
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// planEdit turns the update actions of req into field values. It reads the
// edit screen and resolves people, but writes nothing.
func (s *session) planEdit(ctx context.Context, ic *issueCtx, req adapter.ApplyRequest) (*editPlan, error) {
	plan := &editPlan{put: map[string]any{}, covers: map[int][]string{}, errs: map[int]error{}, status: -1}
	enc := encoder{s: s, issueKey: ic.key, project: ic.p.Key}
	var local, base *xmltree.Node
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
	}
	for i, a := range req.Actions {
		if a.Verb != "update" || a.Target != "" || a.IsAttachment() {
			continue
		}
		switch a.Group {
		case "status":
			plan.status = i
		case "field":
			for _, id := range changedFields(base, local) {
				m, ok := ic.set[id]
				if !ok {
					plan.errs[i] = fmt.Errorf("<field id=%q>: not a field of %s issues in %s", id, textOf(child(local, "type")), ic.p.Key)
					break
				}
				v, err := enc.field(ctx, m, fieldsByID(local)[id], fieldsByID(base)[id])
				if err != nil {
					plan.errs[i] = err
					break
				}
				plan.put[id] = v
				plan.covers[i] = append(plan.covers[i], id)
			}
		default:
			id, ok := elemFields[a.Group]
			if !ok {
				plan.errs[i] = fmt.Errorf("<%s> cannot be changed", a.Group)
				continue
			}
			v, err := enc.system(ctx, a.Group, local, base)
			if err != nil {
				plan.errs[i] = err
				continue
			}
			plan.put[id] = v
			plan.covers[i] = []string{id}
		}
	}
	for i, err := range plan.errs { // a failed action sends none of its fields
		if err != nil {
			for _, id := range plan.covers[i] {
				delete(plan.put, id)
			}
		}
	}
	if plan.status >= 0 {
		plan.from = textOf(child(base, "status"))
		s.planTransition(ctx, ic, enc, plan, textOf(child(local, "status")), plan.from, editInput{local, base})
	}
	if len(plan.put) > 0 {
		if err := s.checkEditable(ctx, ic, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// checkEditable refuses fields that are not on the issue's edit screen.
func (s *session) checkEditable(ctx context.Context, ic *issueCtx, plan *editPlan) error {
	var em struct {
		Fields map[string]struct{} `json:"fields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/editmeta", nil, &em); err != nil {
		return fmt.Errorf("edit screen of %s: %w", ic.key, err)
	}
	plan.editable = map[string]bool{}
	for id := range em.Fields {
		plan.editable[id] = true
	}
	for i, ids := range plan.covers {
		if plan.errs[i] != nil {
			continue
		}
		for _, id := range ids {
			if _, withTransition := plan.trFields[id]; withTransition || plan.editable[id] {
				continue
			}
			switch {
			case id == "resolution" && plan.tr != nil:
				plan.errs[i] = fmt.Errorf("<resolution> is on neither the screen of transition %q nor the edit screen", plan.tr.Name)
			case id == "resolution":
				plan.errs[i] = errors.New("<resolution> can only be set together with a status change on this issue")
			default:
				plan.errs[i] = fmt.Errorf("%s is not on the edit screen of %s", elemName(id), ic.key)
			}
			for _, id := range ids {
				delete(plan.put, id)
			}
			break
		}
	}
	return nil
}

// narrowed is an API error restated for some fields; it still unwraps to
// the API error, so its HTTP status is kept.
type narrowed struct {
	msg string
	err error
}

func (n *narrowed) Error() string { return n.msg }
func (n *narrowed) Unwrap() error { return n.err }

// fieldError is err narrowed to the fields of one action, when Jira named
// the failing fields.
func fieldError(err error, ids []string) error {
	var ae *atlassian.APIError
	if !errors.As(err, &ae) || len(ae.Fields) == 0 {
		return err
	}
	var parts []string
	for _, id := range ids {
		if msg, ok := ae.Fields[id]; ok {
			parts = append(parts, elemName(id)+": "+msg)
		}
	}
	if len(parts) == 0 {
		return &narrowed{"not sent: another field was refused (" + ae.Message() + ")", err}
	}
	return &narrowed{strings.Join(parts, "; "), err}
}

// applyEdit sends plan.put in one PUT, then the transition with its screen
// fields, and records each action's outcome in out (jira spec §7.2, §7.3).
func (s *session) applyEdit(ctx context.Context, ic *issueCtx, plan *editPlan, out []adapter.Result) {
	var errPut, errTr error
	if len(plan.put) > 0 {
		errPut = s.c.Do(ctx, http.MethodPut, "/rest/api/3/issue/"+url.PathEscape(ic.id), map[string]any{"fields": plan.put}, nil)
	}
	if plan.tr != nil && plan.errs[plan.status] == nil {
		if errTr = s.sendTransition(ctx, ic, plan); errTr != nil {
			out[plan.status].Err, out[plan.status].Code = errTr, atlassian.Code(errTr)
		} else {
			out[plan.status].Detail = fmt.Sprintf("%s -> %s (%s)", plan.from, plan.tr.To.Name, plan.tr.Name)
		}
	}
	for i, ids := range plan.covers {
		if plan.errs[i] != nil {
			continue
		}
		for _, id := range ids {
			err := errPut
			if _, withTransition := plan.trFields[id]; withTransition {
				err = errTr
			}
			if err != nil {
				out[i].Err, out[i].Code = fieldError(err, ids), atlassian.Code(err)
				break
			}
		}
	}
	for i, e := range plan.errs {
		out[i].Err = e
	}
}

// sendTransition runs plan's transition with its screen fields.
func (s *session) sendTransition(ctx context.Context, ic *issueCtx, plan *editPlan) error {
	body := map[string]any{"transition": map[string]any{"id": plan.tr.ID}}
	if len(plan.trFields) > 0 {
		body["fields"] = plan.trFields
	}
	err := s.c.Do(ctx, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(ic.id)+"/transitions", body, nil)
	if err == nil {
		return nil
	}
	ids := make([]string, 0, len(plan.trFields))
	for id := range plan.trFields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fieldError(err, ids)
}
