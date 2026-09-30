package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// CustomerAdapter is customer mode: jira+customer://<site> (jira spec §9).
// It is registered, with Open, in customer_apply.go.
type CustomerAdapter struct{}

var (
	_ adapter.Normalizer = (*CustomerAdapter)(nil)
	_ adapter.Cacher     = (*custSession)(nil)
	_ adapter.Reporter   = (*custSession)(nil)
	_ adapter.Identified = (*custSession)(nil)
)

func (*CustomerAdapter) Name() string                 { return "jira-customer" }
func (*CustomerAdapter) Schemes() []string            { return []string{"jira+customer"} }
func (*CustomerAdapter) Schema() *schema.Schema       { return requestSchema }
func (*CustomerAdapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*CustomerAdapter) Verbs() []adapter.Verb        { return nil }
func (*CustomerAdapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

func (*CustomerAdapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

// Describe says what a customer-mode action will do (jira spec §9.2).
func (*CustomerAdapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	var root *xmltree.Node
	p := ""
	if local != nil {
		root, p = local.Root, local.Path
	}
	name, id, _, _ := changes.ParseTarget(a.Target)
	switch {
	case a.IsAttachment() && a.Verb == "create":
		a.Detail = "attach " + path.Base(a.File)
	case a.IsAttachment():
		a.Detail = a.Verb + " attachment (customers can only add attachments)"
	case name == "comment" && a.Verb == "create":
		a.Class, a.Detail = "reply", "add reply (emails the service desk)"
	case name == "approval" && a.Verb == "update":
		a.Class, a.Detail = "approve", "answer approval "+id
	case a.Target != "":
		a.Detail = a.Verb + " " + a.Target + " (not allowed for customers)"
	case a.Verb == "create":
		a.Detail = fmt.Sprintf("raise %q on %s", textOf(child(root, "requestType")), strings.ToUpper(topDir(p)))
	case a.Verb == "update" && a.Group == "status":
		a.Class, a.Detail = "transition", "transition: "+textOf(child(root, "status"))
	case a.Verb == "update" && a.Group == "participant":
		a.Class, a.Detail = "delete", "update participants (may remove people)" // Describe cannot see the base
	default:
		a.Detail = a.Verb + " " + a.Group + " (not allowed for customers)"
	}
}

type desk struct{ ID, Key, Name string }

func (d *desk) dir() string { return strings.ToLower(d.Key) }

// custSession is the requests of one customer on one site (jira spec §9).
type custSession struct {
	c        *atlassian.Client
	t        target
	desks    map[string]*desk // by id
	reg      *registry
	cacheDir string
	report   func(adapter.Progress)
}

func openCustomer(ctx context.Context, t target) (*custSession, error) {
	s := &custSession{c: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Jira"), t: t,
		desks: map[string]*desk{}, reg: newRegistry(), report: func(adapter.Progress) {}}
	s.c.Header.Set("X-ExperimentalApi", "opt-in")
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	if err := s.resolveDesks(ctx); err != nil {
		return nil, authHint(err, t)
	}
	return s, nil
}

// servicePages calls each for every item of a paged /rest/servicedeskapi list.
func (s *custSession) servicePages(ctx context.Context, path string, each func(json.RawMessage) error) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for start := 0; ; {
		var resp struct {
			Values     []json.RawMessage `json:"values"`
			IsLastPage bool              `json:"isLastPage"`
		}
		if err := s.c.Do(ctx, http.MethodGet, fmt.Sprintf("%s%sstart=%d&limit=50", path, sep, start), nil, &resp); err != nil {
			return err
		}
		for _, v := range resp.Values {
			if err := each(v); err != nil {
				return err
			}
		}
		start += len(resp.Values)
		if resp.IsLastPage || len(resp.Values) == 0 {
			return nil
		}
	}
}

func (s *custSession) resolveDesks(ctx context.Context) error {
	want := map[string]bool{}
	for _, d := range s.t.csel.desks {
		want[d] = true
	}
	err := s.servicePages(ctx, "/rest/servicedeskapi/servicedesk", func(raw json.RawMessage) error {
		var d struct {
			ID          string `json:"id"`
			ProjectKey  string `json:"projectKey"`
			ProjectName string `json:"projectName"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if len(want) == 0 || want[d.ID] {
			s.desks[d.ID] = &desk{ID: d.ID, Key: d.ProjectKey, Name: d.ProjectName}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for _, d := range s.t.csel.desks {
		if s.desks[d] == nil {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("service desk %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *custSession) sorted() []*desk {
	out := make([]*desk, 0, len(s.desks))
	for _, d := range s.desks {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (s *custSession) Identity() string                     { return s.t.email }
func (s *custSession) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *custSession) cachePath() string                    { return filepath.Join(s.cacheDir, "people.json") }
func (s *custSession) UseCache(dir string) {
	s.cacheDir = filepath.Join(dir, "jira")
	s.reg.load(s.cachePath())
}
func (s *custSession) Close() error {
	if s.cacheDir != "" && s.reg.dirty {
		return s.reg.save(s.cachePath())
	}
	return nil
}

type isoTime struct {
	ISO8601 string `json:"iso8601"`
}

type apiRequest struct {
	IssueID            string  `json:"issueId"`
	IssueKey           string  `json:"issueKey"`
	RequestTypeID      string  `json:"requestTypeId"`
	ServiceDeskID      string  `json:"serviceDeskId"`
	CreatedDate        isoTime `json:"createdDate"`
	Reporter           apiUser `json:"reporter"`
	RequestFieldValues []struct {
		FieldID string          `json:"fieldId"`
		Label   string          `json:"label"`
		Value   json.RawMessage `json:"value"`
	} `json:"requestFieldValues"`
	CurrentStatus struct {
		Status         string  `json:"status"`
		StatusCategory string  `json:"statusCategory"`
		StatusDate     isoTime `json:"statusDate"`
	} `json:"currentStatus"`
	RequestType struct {
		Name string `json:"name"`
	} `json:"requestType"`
	Participant struct {
		Values []apiUser `json:"values"`
	} `json:"participant"`
	Status struct {
		Values []json.RawMessage `json:"values"`
	} `json:"status"`
}

func (r apiRequest) field(id string) json.RawMessage {
	for _, f := range r.RequestFieldValues {
		if f.FieldID == id {
			return f.Value
		}
	}
	return nil
}

func (r apiRequest) summary() string {
	var s string
	json.Unmarshal(r.field("summary"), &s)
	return s
}

// stamp is the listing version of a request: it moves when the status does
// (jira spec §9.1). New comments on a closed request do not move it.
func (r apiRequest) stamp() string {
	return r.CurrentStatus.StatusDate.ISO8601 + "/" + strconv.Itoa(len(r.Status.Values))
}

const requestExpand = "participant,status,requestType"

// List reports every selected request. Open requests and requests whose
// status moved come in full; closed ones as stubs, which pull fetches only
// when their stamp changed. An empty cursor (clone, pull --full) fetches all.
func (s *custSession) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	full := cursor == ""
	if full {
		s.reg.reset()
	}
	own := map[string]string{"owned": "OWNED_REQUESTS", "all": "ALL_REQUESTS"}[s.t.csel.ownership]
	status := map[string]string{"open": "OPEN_REQUESTS", "all": "ALL_REQUESTS"}[s.t.csel.status]
	l := adapter.Listing{Full: true, Cursor: "requests"}
	desks := s.sorted()
	for i, d := range desks {
		n := 0
		path := fmt.Sprintf("/rest/servicedeskapi/request?serviceDeskId=%s&requestOwnership=%s&requestStatus=%s&expand=%s",
			url.QueryEscape(d.ID), own, status, requestExpand)
		err := s.servicePages(ctx, path, func(raw json.RawMessage) error {
			var r apiRequest
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			n++
			if !full && r.CurrentStatus.StatusCategory == "DONE" {
				l.Resources = append(l.Resources, adapter.Resource{ID: r.IssueID, Version: r.stamp(), Path: requestPath(d, r)})
				return nil
			}
			res, err := s.resource(ctx, r, d)
			if err != nil {
				return err
			}
			l.Resources = append(l.Resources, *res)
			return nil
		})
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("service desk %s: %w", d.Key, err)
		}
		s.report(adapter.Progress{Phase: "pages", Done: i + 1, Total: len(desks), Item: fmt.Sprintf("%s (%s)", d.Key, plural(n, "request"))})
	}
	l.Resources = append(l.Resources, s.reg.resource())
	return l, nil
}

func requestPath(d *desk, r apiRequest) string { return issuePath(d.dir(), r.IssueKey, r.summary()) }

type apiCustComment struct {
	ID      string  `json:"id"`
	Body    string  `json:"body"`
	Public  bool    `json:"public"`
	Author  apiUser `json:"author"`
	Created isoTime `json:"created"`
}

type apiCustAttachment struct {
	Filename string  `json:"filename"`
	Author   apiUser `json:"author"`
	Created  isoTime `json:"created"`
	Size     int64   `json:"size"`
	MimeType string  `json:"mimeType"`
	Links    struct {
		JiraRest string `json:"jiraRest"`
		Content  string `json:"content"`
	} `json:"_links"`
}

// id is the attachment id, the last segment of its jiraRest link.
func (a apiCustAttachment) id() string { return path.Base(a.Links.JiraRest) }

type apiApproval struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	FinalDecision string `json:"finalDecision"`
	CanAnswer     bool   `json:"canAnswerApproval"`
}

func (s *custSession) attachments(ctx context.Context, id string) ([]apiCustAttachment, error) {
	var out []apiCustAttachment
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/attachment", func(raw json.RawMessage) error {
		var a apiCustAttachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (s *custSession) approvals(ctx context.Context, id string) ([]apiApproval, error) {
	var out []apiApproval
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"/approval", func(raw json.RawMessage) error {
		var a apiApproval
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

// resource fetches a request's comments, attachments and approvals and
// renders it as <request> (jira spec §5.10).
func (s *custSession) resource(ctx context.Context, r apiRequest, d *desk) (*adapter.Resource, error) {
	var comments []apiCustComment
	err := s.servicePages(ctx, "/rest/servicedeskapi/request/"+url.PathEscape(r.IssueID)+"/comment", func(raw json.RawMessage) error {
		var c apiCustComment
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		comments = append(comments, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s comments: %w", r.IssueKey, err)
	}
	atts, err := s.attachments(ctx, r.IssueID)
	if err != nil {
		return nil, fmt.Errorf("%s attachments: %w", r.IssueKey, err)
	}
	apps, err := s.approvals(ctx, r.IssueID)
	if err != nil {
		return nil, fmt.Errorf("%s approvals: %w", r.IssueKey, err)
	}
	root := s.requestNode(r, comments, atts, apps)
	return &adapter.Resource{ID: r.IssueID, Version: r.stamp(), Path: requestPath(d, r), At: r.CurrentStatus.StatusDate.ISO8601, Root: root}, nil
}

func (s *custSession) requestNode(r apiRequest, comments []apiCustComment, atts []apiCustAttachment, apps []apiApproval) *xmltree.Node {
	root := el("request", "id", r.IssueID, "key", r.IssueKey, "desk", r.ServiceDeskID, "type", r.RequestTypeID,
		"created", r.CreatedDate.ISO8601, "status-date", r.CurrentStatus.StatusDate.ISO8601)
	add := func(n *xmltree.Node) { root.Children = append(root.Children, n) }
	add(textEl("summary", r.summary()))
	add(textEl("requestType", r.RequestType.Name))
	add(textEl2("status", r.CurrentStatus.Status, "category", strings.ToLower(r.CurrentStatus.StatusCategory)))
	s.reg.see(r.Reporter)
	add(userNode("reporter", r.Reporter))
	for _, u := range r.Participant.Values {
		s.reg.see(u)
		add(userNode("participant", u))
	}
	for _, f := range r.RequestFieldValues {
		switch f.FieldID {
		case "summary", "description", "attachment":
			continue
		}
		if isEmptyJSON(f.Value) {
			continue
		}
		var str string
		var num json.Number
		switch {
		case json.Unmarshal(f.Value, &str) == nil:
			add(textEl2("field", str, "id", f.FieldID, "name", f.Label))
		case json.Unmarshal(f.Value, &num) == nil:
			add(textEl2("field", num.String(), "id", f.FieldID, "name", f.Label))
		default:
			add(rawField(fieldMeta{ID: f.FieldID, Name: f.Label}, f.Value))
		}
	}
	var desc string
	json.Unmarshal(r.field("description"), &desc)
	if strings.TrimSpace(desc) != "" {
		add(textEl2("description", desc, "type", wikiType))
	}
	for _, a := range apps {
		add(el("approval", "id", a.ID, "name", a.Name, "status", a.FinalDecision))
	}
	for _, a := range atts {
		s.reg.see(a.Author)
		add(el("attachment", "id", a.id(), "name", a.Filename, "size", strconv.FormatInt(a.Size, 10), "mime", a.MimeType,
			"created", a.Created.ISO8601, "author", a.Author.DisplayName))
	}
	for _, c := range comments {
		s.reg.see(c.Author)
		add(textEl2("comment", c.Body, "id", c.ID, "account", c.Author.AccountID, "author", c.Author.DisplayName, "created", c.Created.ISO8601))
	}
	return root
}

func (s *custSession) getRequest(ctx context.Context, id string) (apiRequest, *desk, error) {
	var r apiRequest
	if err := s.c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/request/"+url.PathEscape(id)+"?expand="+requestExpand, nil, &r); err != nil {
		return r, nil, err
	}
	d := s.desks[r.ServiceDeskID]
	if d == nil {
		return r, nil, fmt.Errorf("%w: %s is on a service desk outside this tree", adapter.ErrNotFound, r.IssueKey)
	}
	return r, d, nil
}

func (s *custSession) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if id == peopleID {
		r := s.reg.resource()
		return &r, nil
	}
	r, d, err := s.getRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resource(ctx, r, d)
}

// Download streams an attachment through its content link.
func (s *custSession) Download(ctx context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	atts, err := s.attachments(ctx, resID)
	if err != nil {
		return adapter.AttachmentInfo{}, err
	}
	for _, a := range atts {
		if a.id() == attID {
			link := strings.TrimPrefix(a.Links.Content, s.t.base)
			n, err := s.c.Download(ctx, link, w)
			return adapter.AttachmentInfo{Version: "-", Size: n}, err
		}
	}
	return adapter.AttachmentInfo{}, fmt.Errorf("%w: attachment %s", adapter.ErrNotFound, attID)
}
