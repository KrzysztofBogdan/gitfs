package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func init() { adapter.Register(&CustomerAdapter{}) }

var (
	_ adapter.Session = (*custSession)(nil)
	_ adapter.Advisor = (*custSession)(nil)
)

func (*CustomerAdapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return openCustomer(ctx, t)
}

// custTransition is a transition a customer may take; the API does not say
// where it leads.
type custTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *custSession) transitions(ctx context.Context, id string) ([]custTransition, error) {
	var out []custTransition
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/transition", func(raw json.RawMessage) error {
		var t custTransition
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

func transitionNames(ts []custTransition) string {
	var names []string
	for _, t := range ts {
		names = append(names, fmt.Sprintf("%q", t.Name))
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// custStep is one planned request; send is nil when nothing needs sending.
type custStep struct {
	send   func(ctx context.Context) (id string, err error)
	detail string
}

func (s *custSession) post(path string, body any) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return "", s.c.Do(ctx, http.MethodPost, path, body, nil) }
}

func accounts(root *xmltree.Node) map[string]bool {
	out := map[string]bool{}
	if root == nil {
		return out
	}
	for _, p := range root.ChildrenNamed("participant") {
		if acc, _ := p.Attr("account"); acc != "" {
			out[acc] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// plan checks one action on an existing request and prepares its request
// (jira spec §9.2). It writes nothing.
func (s *custSession) plan(ctx context.Context, req adapter.ApplyRequest, a adapter.Action) (custStep, error) {
	id := req.Base.ID
	rp := "/rest/servicedeskapi/request/" + url.PathEscape(id)
	local, base := req.Local.Root, req.Base.Root
	name, sid, nth, _ := changes.ParseTarget(a.Target)
	switch {
	case a.IsAttachment() && a.Verb == "create":
		deskID, _ := base.Attr("desk")
		return custStep{send: func(ctx context.Context) (string, error) { return s.attach(ctx, deskID, id, req, a) }}, nil
	case a.IsAttachment():
		return custStep{}, errors.New("customers can only add attachments")
	case a.Target == "" && a.Verb == "update" && a.Group == "status":
		want := textOf(child(local, "status"))
		ts, err := s.transitions(ctx, id)
		if err != nil {
			return custStep{}, err
		}
		for _, t := range ts {
			if strings.EqualFold(t.Name, want) {
				return custStep{send: s.post(rp+"/transition", map[string]any{"id": t.ID}), detail: "transition " + t.Name}, nil
			}
		}
		return custStep{}, fmt.Errorf("no transition %q for you on this request; available: %s (write a transition name in <status>)", want, transitionNames(ts))
	case a.Target == "" && a.Verb == "update" && a.Group == "participant":
		for _, p := range local.ChildrenNamed("participant") {
			if acc, _ := p.Attr("account"); acc == "" {
				return custStep{}, fmt.Errorf(`<participant>%s</participant>: write account="…" from .people.xml; customers cannot look people up`, textOf(p))
			}
		}
		have, want := accounts(base), accounts(local)
		var add, drop []string
		for acc := range want {
			if !have[acc] {
				add = append(add, acc)
			}
		}
		for acc := range have {
			if !want[acc] {
				drop = append(drop, acc)
			}
		}
		sort.Strings(add)
		sort.Strings(drop)
		return custStep{send: func(ctx context.Context) (string, error) {
			if len(add) > 0 {
				if err := s.c.Do(ctx, http.MethodPost, rp+"/participant", map[string]any{"accountIds": add}, nil); err != nil {
					return "", err
				}
			}
			if len(drop) > 0 {
				return "", s.c.Do(ctx, http.MethodDelete, rp+"/participant", map[string]any{"accountIds": drop}, nil)
			}
			return "", nil
		}}, nil
	case a.Target == "":
		return custStep{}, fmt.Errorf("<%s> is read-only for customers", a.Group)
	case name == "comment" && a.Verb == "create":
		c := nthNew(local, "comment", "id", nth)
		if textOf(c) == "" {
			return custStep{}, errors.New("<comment> is empty")
		}
		return custStep{send: s.post(rp+"/comment", map[string]any{"body": textOf(c), "public": true})}, nil
	case name == "comment":
		return custStep{}, errors.New("customers cannot edit or delete comments")
	case name == "approval" && a.Verb == "update":
		ap, bp := validate.FindSub(local, "approval", "id", sid), validate.FindSub(base, "approval", "id", sid)
		decision, _ := attr(ap, "decision")
		if decision != "approve" && decision != "decline" {
			return custStep{}, fmt.Errorf(`approval %s: write decision="approve" or decision="decline"`, sid)
		}
		if st, _ := attr(bp, "status"); st != "pending" {
			return custStep{}, fmt.Errorf("approval %s is %s, not pending", sid, st)
		}
		return custStep{send: s.post(rp+"/approval/"+url.PathEscape(sid), map[string]any{"decision": decision})}, nil
	}
	return custStep{}, fmt.Errorf("customers cannot %s %s", a.Verb, a.Target)
}

// attach uploads a temporary file to the desk, then attaches it publicly.
func (s *custSession) attach(ctx context.Context, deskID, reqID string, req adapter.ApplyRequest, a adapter.Action) (string, error) {
	if req.Open == nil {
		return "", errors.New("jira: no file reader for uploads")
	}
	f, err := req.Open(a.File)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var tmp struct {
		Temporary []struct {
			ID string `json:"temporaryAttachmentId"`
		} `json:"temporaryAttachments"`
	}
	if err := s.c.Upload(ctx, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(deskID)+"/attachTemporaryFile", path.Base(a.File), f, nil, &tmp); err != nil {
		return "", err
	}
	if len(tmp.Temporary) == 0 {
		return "", errors.New("jira: upload returned no temporary attachment")
	}
	var resp struct {
		Attachments struct {
			Values []apiCustAttachment `json:"values"`
		} `json:"attachments"`
	}
	body := map[string]any{"temporaryAttachmentIds": []string{tmp.Temporary[0].ID}, "public": true}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/servicedeskapi/request/"+url.PathEscape(reqID)+"/attachment", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Attachments.Values) == 0 {
		return "", errors.New("jira: attach returned no attachment")
	}
	return resp.Attachments.Values[0].id(), nil
}

// raisePlan is a new request before it is sent.
type raisePlan struct {
	d      *desk
	typeID string
	values map[string]any
}

// planRaise checks a new file against its request type's form (jira spec §9.2).
func (s *custSession) planRaise(ctx context.Context, req adapter.ApplyRequest) (*raisePlan, error) {
	root := req.Local.Root
	dir := topDir(req.Local.Path)
	var d *desk
	var dirs []string
	for _, x := range s.sorted() {
		dirs = append(dirs, x.dir())
		if x.dir() == dir {
			d = x
		}
	}
	if d == nil || !strings.Contains(req.Local.Path, "/") {
		return nil, fmt.Errorf("new requests go in a service desk folder (%s)", strings.Join(dirs, ", "))
	}
	want := textOf(child(root, "requestType"))
	var typeID string
	var names []string
	err := s.servicePages(ctx, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(d.ID)+"/requesttype", func(raw json.RawMessage) error {
		var t struct{ ID, Name string }
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		names = append(names, t.Name)
		if strings.EqualFold(t.Name, want) {
			typeID = t.ID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if typeID == "" {
		return nil, fmt.Errorf("<requestType> %q is not offered by %s; request types: %s", want, d.Key, strings.Join(names, ", "))
	}
	var form struct {
		Fields []struct {
			FieldID  string `json:"fieldId"`
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"requestTypeFields"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/servicedesk/"+url.PathEscape(d.ID)+"/requesttype/"+url.PathEscape(typeID)+"/field", nil, &form); err != nil {
		return nil, err
	}
	onForm := map[string]bool{}
	for _, f := range form.Fields {
		onForm[f.FieldID] = true
	}
	values := map[string]any{}
	if t := textOf(child(root, "summary")); t != "" {
		values["summary"] = t
	}
	if t := textOf(child(root, "description")); t != "" {
		values["description"] = t
	}
	for id, f := range fieldsByID(root) {
		if !onForm[id] {
			return nil, fmt.Errorf("<field id=%q> is not on the %q form of %s", id, want, d.Key)
		}
		values[id] = textOf(f)
	}
	var missing []string
	for _, f := range form.Fields {
		if _, ok := values[f.FieldID]; f.Required && !ok {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("a new %q request needs %s", want, strings.Join(missing, ", "))
	}
	return &raisePlan{d: d, typeID: typeID, values: values}, nil
}

// raise creates the request of a new file, then its comments and attachments.
func (s *custSession) raise(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	plan, err := s.planRaise(ctx, req)
	if err != nil {
		return []adapter.Result{{Action: act, Err: err}}
	}
	var resp struct {
		IssueID  string `json:"issueId"`
		IssueKey string `json:"issueKey"`
	}
	body := map[string]any{"serviceDeskId": plan.d.ID, "requestTypeId": plan.typeID, "requestFieldValues": plan.values}
	if err := s.c.Do(ctx, http.MethodPost, "/rest/servicedeskapi/request", body, &resp); err != nil {
		return []adapter.Result{{Action: act, Err: err, Code: atlassian.Code(err)}}
	}
	out := []adapter.Result{{Action: act, ID: resp.IssueID, Detail: resp.IssueKey}}
	rp := "/rest/servicedeskapi/request/" + url.PathEscape(resp.IssueID)
	for n := 1; nthNew(req.Local.Root, "comment", "id", n) != nil; n++ {
		c := nthNew(req.Local.Root, "comment", "id", n)
		if err := s.c.Do(ctx, http.MethodPost, rp+"/comment", map[string]any{"body": textOf(c), "public": true}, nil); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "create", Target: fmt.Sprintf("comment[%d]", n), Class: "reply"}, Err: err})
		}
	}
	for _, a := range req.Actions {
		if a.IsAttachment() {
			id, err := s.attach(ctx, plan.d.ID, resp.IssueID, req, a)
			out = append(out, adapter.Result{Action: a, ID: id, Version: "-", Err: err, Code: atlassian.Code(err)})
		}
	}
	return out
}

func (s *custSession) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		_, out[i].Err = s.planRaise(ctx, req)
		return out
	case "delete":
		out[i].Err = errors.New("customers cannot delete requests")
		return out
	}
	for i, a := range req.Actions {
		step, err := s.plan(ctx, req, a)
		out[i].Err, out[i].Detail = err, step.detail
	}
	return out
}

func (s *custSession) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	if err := generated(req); err != nil {
		return failAll(out, err)
	}
	switch i, verb := resourceVerb(req.Actions); verb {
	case "create":
		return s.raise(ctx, req)
	case "delete":
		out[i].Err = errors.New("customers cannot delete requests")
		return out
	}
	for i, a := range req.Actions {
		step, err := s.plan(ctx, req, a)
		if err == nil && step.send != nil {
			out[i].ID, err = step.send(ctx)
		}
		out[i].Err, out[i].Code, out[i].Detail = err, atlassian.Code(err), step.detail
		if err == nil && a.IsAttachment() {
			out[i].Version = "-"
		}
	}
	return out
}

// Available lists the customer transitions and the approvals the user can
// answer (jira spec §9.3).
func (s *custSession) Available(ctx context.Context, id string, local *adapter.Resource) (adapter.Advice, error) {
	if id == peopleID {
		return adapter.Advice{State: "generated by gfs, read-only"}, nil
	}
	r, _, err := s.getRequest(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	ts, err := s.transitions(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	adv := adapter.Advice{State: "status: " + r.CurrentStatus.Status}
	for _, t := range ts {
		adv.Items = append(adv.Items, adapter.Available{Verb: "transition", Name: t.Name, To: "(the service desk decides the status)"})
	}
	apps, err := s.approvals(ctx, id)
	if err != nil {
		return adapter.Advice{}, err
	}
	for _, a := range apps {
		if a.CanAnswer && a.FinalDecision == "pending" {
			adv.Items = append(adv.Items, adapter.Available{Verb: "approve", Name: a.Name,
				To: fmt.Sprintf(`<approval id=%q decision="approve"> or "decline"`, a.ID)})
		}
	}
	if local != nil {
		if want := textOf(child(local.Root, "status")); want != "" && !strings.EqualFold(want, r.CurrentStatus.Status) {
			adv.Note = fmt.Sprintf("no transition %q for you; available: %s", want, transitionNames(ts))
			for _, t := range ts {
				if strings.EqualFold(t.Name, want) {
					adv.Note = fmt.Sprintf("local <status> %s -> %q", want, t.Name)
				}
			}
		}
	}
	return adv, nil
}
