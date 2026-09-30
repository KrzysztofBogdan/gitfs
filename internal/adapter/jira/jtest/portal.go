package jtest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Portal is an in-memory JSM customer API (/rest/servicedeskapi) as a
// customer sees it: no Jira platform API at all.
type Portal struct {
	*httptest.Server
	Clock    func() time.Time
	Requests []string // "METHOD /path"

	mu       sync.Mutex
	desks    []Desk
	types    map[string][]RequestType  // by desk id
	fields   map[string][]RequestField // by "desk/type"
	users    map[string]*User
	requests map[string]*Request // by id
	temps    map[string]*Attachment
	seq      int
}

type Desk struct{ ID, Key, Name string }

type RequestType struct{ ID, Name string }

type RequestField struct {
	ID, Name string
	Required bool
}

type CustomerComment struct {
	ID, Author, Body string
	Public           bool
	Created          time.Time
}

type Approval struct {
	ID, Name  string
	Decision  string // "pending", "approved", "declined"
	CanAnswer bool
}

// CustomerTransition is a transition a customer may take; To is the status it leads to.
type CustomerTransition struct{ ID, Name, To, Category string }

type Request struct {
	ID, Key, Desk, Type  string
	Summary, Description string
	Status, Category     string // category: NEW, INDETERMINATE, DONE
	StatusDate           time.Time
	History              int // length of the status history
	Reporter             string
	Participants         []string
	Fields               map[string]any // other request field values
	Created              time.Time
	Comments             []*CustomerComment
	Attachments          []*Attachment
	Approvals            []*Approval
	Transitions          []CustomerTransition
	Mine                 bool // raised by the caller (ownership=owned)
}

func NewPortal() *Portal {
	p := &Portal{types: map[string][]RequestType{}, fields: map[string][]RequestField{}, users: map[string]*User{
		"me": {Account: "me", Name: "Me", Email: "me@x.com"}}, requests: map[string]*Request{}, temps: map[string]*Attachment{}, seq: 20000}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk", p.getDesks)
	mux.HandleFunc("GET /rest/servicedeskapi/request", p.listRequests)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}", p.getRequest)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/comment", p.getComments)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/attachment", p.getAttachments)
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/approval", p.getApprovals)
	mux.HandleFunc("GET /secure/attachment/{aid}/{name}", p.content)
	for _, register := range portalRegistrars {
		register(p, mux)
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.Requests = append(p.Requests, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/rest/api/") {
			jiraError(w, 404, "customers cannot use the Jira platform API", nil)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return p
}

// portalRegistrars add the write endpoints (portal_write.go).
var portalRegistrars []func(*Portal, *http.ServeMux)

func (p *Portal) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func (p *Portal) nextID() string {
	p.seq++
	return strconv.Itoa(p.seq)
}

func (p *Portal) AddDesk(d Desk, types ...RequestType) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.desks = append(p.desks, d)
	p.types[d.ID] = types
}

func (p *Portal) SetFields(desk, typeID string, fs ...RequestField) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fields[desk+"/"+typeID] = fs
}

func (p *Portal) AddUser(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[u.Account] = &u
}

// AddRequest stores r; empty ID, Key, times and status are filled in.
func (p *Portal) AddRequest(r Request) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addRequest(r)
}

func (p *Portal) addRequest(r Request) *Request {
	if r.ID == "" {
		r.ID = p.nextID()
	}
	if r.Key == "" {
		for _, d := range p.desks {
			if d.ID == r.Desk {
				r.Key = fmt.Sprintf("%s-%s", d.Key, r.ID)
			}
		}
	}
	if r.Created.IsZero() {
		r.Created = p.now()
	}
	if r.StatusDate.IsZero() {
		r.StatusDate = r.Created
	}
	if r.Status == "" {
		r.Status, r.Category = "Waiting for support", "INDETERMINATE"
	}
	if r.History == 0 {
		r.History = 1
	}
	if r.Fields == nil {
		r.Fields = map[string]any{}
	}
	p.requests[r.ID] = &r
	return &r
}

// Edit changes a request as the service desk would.
func (p *Portal) Edit(id string, f func(*Request)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p.requests[id])
}

// SetStatus moves a request as an agent would: a new status history entry.
func (p *Portal) SetStatus(id, status, category string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.requests[id]
	r.Status, r.Category, r.StatusDate = status, category, p.now()
	r.History++
}

// AddComment adds a comment as an agent would; the request's status does not change.
func (p *Portal) AddComment(id string, c CustomerComment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.ID == "" {
		c.ID = p.nextID()
	}
	if c.Created.IsZero() {
		c.Created = p.now()
	}
	p.requests[id].Comments = append(p.requests[id].Comments, &c)
}

func (p *Portal) Request(id string) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.requests[id]; r != nil {
		c := *r
		return &c
	}
	return nil
}

// Count is how many requests were "METHOD /path" with path starting with prefix.
func (p *Portal) Count(method, prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.Requests {
		if strings.HasPrefix(r, method+" "+prefix) {
			n++
		}
	}
	return n
}

func iso(t time.Time) map[string]any {
	return map[string]any{"iso8601": t.Format("2006-01-02T15:04:05-0700")}
}

func (p *Portal) user(account string) any {
	u := p.users[account]
	if u == nil {
		u = &User{Account: account, Name: account}
	}
	m := map[string]any{"accountId": u.Account, "displayName": u.Name, "active": true}
	if u.Email != "" {
		m["emailAddress"] = u.Email
	}
	return m
}

func (p *Portal) typeName(r *Request) string {
	for _, t := range p.types[r.Desk] {
		if t.ID == r.Type {
			return t.Name
		}
	}
	return ""
}

func (p *Portal) requestJSON(r *Request) map[string]any {
	vals := []any{
		map[string]any{"fieldId": "summary", "label": "Summary", "value": r.Summary},
		map[string]any{"fieldId": "description", "label": "Description", "value": r.Description},
	}
	ids := make([]string, 0, len(r.Fields))
	for id := range r.Fields {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		label := id
		for _, f := range p.fields[r.Desk+"/"+r.Type] {
			if f.ID == id {
				label = f.Name
			}
		}
		vals = append(vals, map[string]any{"fieldId": id, "label": label, "value": r.Fields[id]})
	}
	var parts []any
	for _, a := range r.Participants {
		parts = append(parts, p.user(a))
	}
	history := make([]any, r.History)
	for i := range history {
		history[i] = map[string]any{"status": r.Status}
	}
	return map[string]any{"issueId": r.ID, "issueKey": r.Key, "requestTypeId": r.Type, "serviceDeskId": r.Desk,
		"createdDate": iso(r.Created), "reporter": p.user(r.Reporter), "requestFieldValues": vals,
		"currentStatus": map[string]any{"status": r.Status, "statusCategory": r.Category, "statusDate": iso(r.StatusDate)},
		"requestType":   map[string]any{"id": r.Type, "name": p.typeName(r)},
		"participant":   map[string]any{"values": parts}, "status": map[string]any{"values": history}}
}

func startLimit(r *http.Request, def int) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = def
	}
	return start, limit
}

func pageOf(items []any, start, limit int) map[string]any {
	w := window(items, start, limit)
	if w == nil {
		w = []any{}
	}
	return map[string]any{"values": w, "start": start, "limit": limit, "size": len(w), "isLastPage": start+limit >= len(items)}
}

func (p *Portal) getDesks(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, d := range p.desks {
		out = append(out, map[string]any{"id": d.ID, "projectKey": d.Key, "projectName": d.Name})
	}
	start, limit := startLimit(r, 50)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	var rs []*Request
	for _, x := range p.requests {
		switch {
		case q.Get("serviceDeskId") != "" && x.Desk != q.Get("serviceDeskId"):
		case q.Get("requestOwnership") == "OWNED_REQUESTS" && !x.Mine:
		case q.Get("requestStatus") == "OPEN_REQUESTS" && x.Category == "DONE":
		default:
			rs = append(rs, x)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Created.After(rs[j].Created) })
	var out []any
	for _, x := range rs {
		out = append(out, p.requestJSON(x))
	}
	start, limit := startLimit(r, 50)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) find(w http.ResponseWriter, id string) *Request {
	if x := p.requests[id]; x != nil {
		return x
	}
	for _, x := range p.requests {
		if x.Key == id {
			return x
		}
	}
	jiraError(w, 404, "request not found", nil)
	return nil
}

func (p *Portal) getRequest(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if x := p.find(w, r.PathValue("id")); x != nil {
		writeJSON(w, p.requestJSON(x))
	}
}

func (p *Portal) getComments(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, c := range x.Comments {
		if c.Public {
			out = append(out, map[string]any{"id": c.ID, "body": c.Body, "public": true, "author": p.user(c.Author), "created": iso(c.Created)})
		}
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) attachmentJSON(a *Attachment) map[string]any {
	return map[string]any{"filename": a.Filename, "author": p.user(a.Author), "created": iso(a.Created), "size": len(a.Data),
		"mimeType": a.Mime, "_links": map[string]any{"jiraRest": p.URL + "/rest/api/2/attachment/" + a.ID,
			"content": p.URL + "/secure/attachment/" + a.ID + "/" + a.Filename}}
}

func (p *Portal) getAttachments(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, a := range x.Attachments {
		out = append(out, p.attachmentJSON(a))
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) getApprovals(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, a := range x.Approvals {
		out = append(out, map[string]any{"id": a.ID, "name": a.Name, "finalDecision": a.Decision, "canAnswerApproval": a.CanAnswer})
	}
	start, limit := startLimit(r, 100)
	writeJSON(w, pageOf(out, start, limit))
}

func (p *Portal) content(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.requests {
		for _, a := range x.Attachments {
			if a.ID == r.PathValue("aid") {
				io.WriteString(w, string(a.Data))
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}
