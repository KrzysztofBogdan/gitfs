// Package jtest is an in-memory Jira Cloud (REST v3 subset) for tests.
package jtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TimeFormat is how Jira writes timestamps.
const TimeFormat = "2006-01-02T15:04:05.000-0700"

type Project struct {
	ID, Key string
	Type    string // "software" (default), "service_desk", "business"
}

type IssueType struct {
	ID, Name string
	Subtask  bool
}

// Field is one field of a create screen, as createmeta reports it.
type Field struct {
	ID, Name, Type, Items, Custom string
	Required                      bool
	Options                       []Option // select-type fields
}

type Option struct{ ID, Value string }

type User struct {
	Account, Name string
	Email         string // "" = hidden by the user's profile settings
	Customer      bool
}

type Comment struct {
	ID, Author       string          // author account
	Body             json.RawMessage // ADF doc
	Created, Updated time.Time
	Public           *bool // JSM only
}

type Worklog struct {
	ID, Author, Started, Spent string
	Comment                    json.RawMessage
	Created, Updated           time.Time
}

type Attachment struct {
	ID, Filename, Mime, Author string
	Data                       []byte
	Created                    time.Time
}

type LinkType struct{ ID, Name, Inward, Outward string }

// Link reads "From <type.outward> To".
type Link struct{ ID, Type, From, To string }

type Status struct{ ID, Name, Category string } // category: new, indeterminate, done

// Transition leads to status To (a name); no From means from any status.
type Transition struct {
	ID, Name, To string
	From         []string
	Screen       []ScreenField
}

type ScreenField struct {
	ID       string
	Required bool
}

type Workflow struct {
	Name        string
	Statuses    []Status
	Transitions []Transition
}

type Issue struct {
	ID, Key, Project, Type      string // project key, issue type id
	Summary, Status             string
	Assignee, Reporter, Creator string         // account ids
	Fields                      map[string]any // every other field, as Jira JSON
	Created, Updated            time.Time
	Comments                    []*Comment
	Worklogs                    []*Worklog
	Attachments                 []*Attachment
}

type Server struct {
	*httptest.Server
	Clock        func() time.Time // nil: time.Now
	Admin        bool             // the account has Administer Jira
	CommentLimit int              // comments inline in search results (default 20)
	WorklogLimit int              // worklogs inline in search results (default 20)
	RateLimit    int              // the next RateLimit requests get 429 with Retry-After: 0
	Requests     []string         // "METHOD /path" of every request
	Fail         map[string]int   // "METHOD /path" -> status returned instead of handling
	EditHidden   []string         // field ids on the create screen but not the edit screen

	mu        sync.Mutex
	projects  map[string]*Project // by key
	types     map[string][]IssueType
	fields    map[string][]Field // by "KEY/typeID"
	users     map[string]*User
	issues    map[string]*Issue // by id
	linkTypes []LinkType
	links     []*Link
	workflows map[string]*Workflow // by "KEY/typeID"
	seq       int
	counters  map[string]int // next issue number per project
}

func New() *Server {
	s := &Server{CommentLimit: 20, WorklogLimit: 20, Fail: map[string]int{},
		projects: map[string]*Project{}, types: map[string][]IssueType{}, fields: map[string][]Field{},
		users: map[string]*User{"me": {Account: "me", Name: "Me", Email: "me@x.com"}}, issues: map[string]*Issue{},
		workflows: map[string]*Workflow{}, seq: 10000, counters: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/myself", s.myself)
	mux.HandleFunc("GET /rest/api/3/project/search", s.projectSearch)
	mux.HandleFunc("GET /rest/api/3/issue/createmeta/{p}/issuetypes", s.createmetaTypes)
	mux.HandleFunc("GET /rest/api/3/issue/createmeta/{p}/issuetypes/{t}", s.createmetaFields)
	mux.HandleFunc("POST /rest/api/3/search/jql", s.search)
	mux.HandleFunc("POST /rest/api/3/search/approximate-count", s.count)
	mux.HandleFunc("GET /rest/api/3/issue/{id}", s.getIssue)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/comment", s.getComments)
	mux.HandleFunc("GET /rest/api/3/issue/{id}/worklog", s.getWorklogs)
	mux.HandleFunc("GET /rest/api/3/user/search", s.userSearch)
	mux.HandleFunc("GET /rest/api/3/user/assignable/search", s.userSearch)
	mux.HandleFunc("GET /rest/api/3/issueLinkType", s.getLinkTypes)
	mux.HandleFunc("GET /rest/api/3/mypermissions", s.myPermissions)
	mux.HandleFunc("GET /rest/api/3/workflowscheme/project", s.workflowScheme)
	mux.HandleFunc("GET /rest/api/3/workflow/search", s.workflowSearch)
	mux.HandleFunc("GET /rest/api/3/project/{key}/statuses", s.projectStatuses)
	mux.HandleFunc("GET /rest/api/3/status", s.allStatuses)
	mux.HandleFunc("GET /rest/api/3/attachment/content/{id}", s.attachmentContent)
	for _, register := range registrars { // write endpoints, one file per kind
		register(s, mux)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		key := r.Method + " " + r.URL.Path
		s.Requests = append(s.Requests, key)
		status, fail := s.Fail[key]
		limited := s.RateLimit > 0
		if limited {
			s.RateLimit--
		}
		s.mu.Unlock()
		switch {
		case limited:
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"errorMessages":["rate limited"]}`, http.StatusTooManyRequests)
		case fail:
			http.Error(w, `{"errorMessages":["injected failure"]}`, status)
		default:
			mux.ServeHTTP(w, r)
		}
	}))
	return s
}

// registrars add handlers to a new server; each write-side file appends its own.
var registrars []func(*Server, *http.ServeMux)

func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Server) nextID() string {
	s.seq++
	return strconv.Itoa(s.seq)
}

// Count is how many requests were "METHOD /path" with path starting with prefix.
func (s *Server) Count(method, prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if strings.HasPrefix(r, method+" "+prefix) {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jiraError(w http.ResponseWriter, status int, msg string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"errorMessages": []string{}, "errors": map[string]string{}}
	if msg != "" {
		body["errorMessages"] = []string{msg}
	}
	if fields != nil {
		body["errors"] = fields
	}
	json.NewEncoder(w).Encode(body)
}

// ---- setup ----

func (s *Server) AddProject(p Project, types ...IssueType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Type == "" {
		p.Type = "software"
	}
	if p.ID == "" {
		p.ID = s.nextID()
	}
	s.projects[p.Key] = &p
	s.types[p.Key] = types
}

// SetFields sets the create-screen fields of an issue type.
func (s *Server) SetFields(project, typeID string, fs ...Field) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fields[project+"/"+typeID] = fs
}

func (s *Server) AddUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.Account] = &u
}

func (s *Server) AddLinkType(lt LinkType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkTypes = append(s.linkTypes, lt)
}

func (s *Server) AddLink(l Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == "" {
		l.ID = s.nextID()
	}
	s.links = append(s.links, &l)
}

// SetWorkflow sets the workflow of an issue type (default: To Do, In Progress, Done, all global).
func (s *Server) SetWorkflow(project, typeID string, w Workflow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[project+"/"+typeID] = &w
}

var defaultWorkflow = Workflow{Name: "Simple Workflow",
	Statuses: []Status{{"1", "To Do", "new"}, {"2", "In Progress", "indeterminate"}, {"3", "Done", "done"}},
	Transitions: []Transition{{ID: "11", Name: "To Do", To: "To Do"}, {ID: "21", Name: "Start", To: "In Progress"},
		{ID: "31", Name: "Done", To: "Done"}}}

func (s *Server) workflow(is *Issue) *Workflow {
	if w := s.workflows[is.Project+"/"+is.Type]; w != nil {
		return w
	}
	return &defaultWorkflow
}

// AddIssue stores is; empty ID, Key, Status and times are filled in.
func (s *Server) AddIssue(is Issue) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addIssue(is)
}

func (s *Server) addIssue(is Issue) *Issue {
	if is.ID == "" {
		is.ID = s.nextID()
	}
	if is.Key == "" {
		s.counters[is.Project]++
		is.Key = fmt.Sprintf("%s-%d", is.Project, s.counters[is.Project])
	}
	if is.Status == "" {
		is.Status = s.workflow(&is).Statuses[0].Name
	}
	if is.Created.IsZero() {
		is.Created = s.now()
	}
	if is.Updated.IsZero() {
		is.Updated = is.Created
	}
	if is.Fields == nil {
		is.Fields = map[string]any{}
	}
	if is.Creator == "" {
		is.Creator = is.Reporter
	}
	s.issues[is.ID] = &is
	return &is
}

// Edit changes an issue as another user would; Updated moves to now.
func (s *Server) Edit(id string, f func(*Issue)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	f(is)
	is.Updated = s.now()
}

func (s *Server) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.issues, id)
}

// Move puts an issue in another project under a new key; the id stays.
func (s *Server) Move(id, project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	s.counters[project]++
	is.Project, is.Key = project, fmt.Sprintf("%s-%d", project, s.counters[project])
	is.Updated = s.now()
}

// AddComment adds a comment as another user would; the issue's Updated moves.
func (s *Server) AddComment(issueID string, c Comment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if c.ID == "" {
		c.ID = s.nextID()
	}
	if c.Created.IsZero() {
		c.Created = s.now()
	}
	if c.Updated.IsZero() {
		c.Updated = c.Created
	}
	is.Comments = append(is.Comments, &c)
	is.Updated = s.now()
}

func (s *Server) AddWorklog(issueID string, wl Worklog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if wl.ID == "" {
		wl.ID = s.nextID()
	}
	if wl.Created.IsZero() {
		wl.Created = s.now()
	}
	if wl.Updated.IsZero() {
		wl.Updated = wl.Created
	}
	is.Worklogs = append(is.Worklogs, &wl)
	is.Updated = s.now()
}

func (s *Server) AddAttachment(issueID string, a Attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[issueID]
	if a.ID == "" {
		a.ID = s.nextID()
	}
	if a.Created.IsZero() {
		a.Created = s.now()
	}
	is.Attachments = append(is.Attachments, &a)
	is.Updated = s.now()
}

// Issue returns a copy of the stored issue (nil when gone).
func (s *Server) Issue(id string) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.issues[id]
	if is == nil {
		return nil
	}
	c := *is
	return &c
}

// ---- rendering ----

func (s *Server) userJSON(account string) any {
	u := s.users[account]
	if u == nil {
		if account == "" {
			return nil
		}
		u = &User{Account: account, Name: account}
	}
	m := map[string]any{"accountId": u.Account, "displayName": u.Name, "active": true, "accountType": "atlassian"}
	if u.Customer {
		m["accountType"] = "customer"
	}
	if u.Email != "" {
		m["emailAddress"] = u.Email
	}
	return m
}

func (s *Server) typeOf(is *Issue) IssueType {
	for _, t := range s.types[is.Project] {
		if t.ID == is.Type {
			return t
		}
	}
	return IssueType{ID: is.Type, Name: "Type " + is.Type}
}

func (s *Server) statusJSON(is *Issue) map[string]any {
	cat := "new"
	for _, st := range s.workflow(is).Statuses {
		if st.Name == is.Status {
			cat = st.Category
		}
	}
	return map[string]any{"name": is.Status, "statusCategory": map[string]any{"key": cat}}
}

func (s *Server) commentJSON(is *Issue, c *Comment) map[string]any {
	m := map[string]any{"id": c.ID, "author": s.userJSON(c.Author), "body": c.Body,
		"created": c.Created.Format(TimeFormat), "updated": c.Updated.Format(TimeFormat)}
	if s.projects[is.Project].Type == "service_desk" {
		pub := true
		if c.Public != nil {
			pub = *c.Public
		}
		m["jsdPublic"] = pub
	}
	return m
}

func (s *Server) worklogJSON(wl *Worklog) map[string]any {
	m := map[string]any{"id": wl.ID, "author": s.userJSON(wl.Author), "started": wl.Started, "timeSpent": wl.Spent,
		"created": wl.Created.Format(TimeFormat), "updated": wl.Updated.Format(TimeFormat)}
	if len(wl.Comment) > 0 {
		m["comment"] = wl.Comment
	}
	return m
}

func (s *Server) linksJSON(is *Issue) []any {
	out := []any{}
	for _, l := range s.links {
		var lt LinkType
		for _, t := range s.linkTypes {
			if t.Name == l.Type {
				lt = t
			}
		}
		typ := map[string]any{"id": lt.ID, "name": lt.Name, "inward": lt.Inward, "outward": lt.Outward}
		switch {
		case l.From == is.Key:
			out = append(out, map[string]any{"id": l.ID, "type": typ, "outwardIssue": map[string]any{"key": l.To}})
		case l.To == is.Key:
			out = append(out, map[string]any{"id": l.ID, "type": typ, "inwardIssue": map[string]any{"key": l.From}})
		}
	}
	return out
}

func page(items []any, limit int) map[string]any {
	shown := items
	if limit >= 0 && len(items) > limit {
		shown = items[:limit]
	}
	return map[string]any{"total": len(items), "maxResults": len(shown), "startAt": 0, "items": shown}
}

// issueJSON renders is with the given fields ("*all" or nil for every field).
func (s *Server) issueJSON(is *Issue, want []string, inlineLimit bool) map[string]any {
	p := s.projects[is.Project]
	t := s.typeOf(is)
	f := map[string]any{
		"summary":   is.Summary,
		"issuetype": map[string]any{"id": t.ID, "name": t.Name, "subtask": t.Subtask},
		"status":    s.statusJSON(is),
		"project":   map[string]any{"id": p.ID, "key": p.Key, "projectTypeKey": p.Type},
		"created":   is.Created.Format(TimeFormat), "updated": is.Updated.Format(TimeFormat),
		"assignee": s.userJSON(is.Assignee), "reporter": s.userJSON(is.Reporter), "creator": s.userJSON(is.Creator),
		"issuelinks": s.linksJSON(is),
	}
	climit, wlimit := -1, -1
	if inlineLimit {
		climit, wlimit = s.CommentLimit, s.WorklogLimit
	}
	var cs, ws, as []any
	for _, c := range is.Comments {
		cs = append(cs, s.commentJSON(is, c))
	}
	for _, wl := range is.Worklogs {
		ws = append(ws, s.worklogJSON(wl))
	}
	for _, a := range is.Attachments {
		as = append(as, map[string]any{"id": a.ID, "filename": a.Filename, "mimeType": a.Mime, "size": len(a.Data),
			"created": a.Created.Format(TimeFormat), "author": s.userJSON(a.Author)})
	}
	cp := page(cs, climit)
	cp["comments"] = cp["items"]
	delete(cp, "items")
	wp := page(ws, wlimit)
	wp["worklogs"] = wp["items"]
	delete(wp, "items")
	f["comment"], f["worklog"], f["attachment"] = cp, wp, as
	for k, v := range is.Fields {
		f[k] = v
	}
	all := len(want) == 0
	keep := map[string]bool{}
	for _, w := range want {
		if w == "*all" {
			all = true
		}
		keep[w] = true
	}
	if !all {
		for k := range f {
			if !keep[k] {
				delete(f, k)
			}
		}
	}
	return map[string]any{"id": is.ID, "key": is.Key, "fields": f}
}

// ---- read handlers ----

func (s *Server) myself(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.userJSON("me"))
}

func startMax(r *http.Request, def int) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	max, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if err != nil || max <= 0 {
		max = def
	}
	return start, max
}

func window[T any](items []T, start, max int) []T {
	if start > len(items) {
		return nil
	}
	end := min(start+max, len(items))
	return items[start:end]
}

func (s *Server) projectSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.projects))
	for k := range s.projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	start, max := startMax(r, 50)
	var vals []any
	for _, k := range window(keys, start, max) {
		p := s.projects[k]
		vals = append(vals, map[string]any{"id": p.ID, "key": p.Key, "projectTypeKey": p.Type})
	}
	writeJSON(w, map[string]any{"values": vals, "total": len(keys), "isLast": start+max >= len(keys)})
}

func (s *Server) createmetaTypes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.types[r.PathValue("p")]
	if !ok {
		jiraError(w, 404, "no project "+r.PathValue("p"), nil)
		return
	}
	start, max := startMax(r, 50)
	var vals []any
	for _, t := range window(ts, start, max) {
		vals = append(vals, map[string]any{"id": t.ID, "name": t.Name, "subtask": t.Subtask})
	}
	writeJSON(w, map[string]any{"issueTypes": vals, "total": len(ts), "startAt": start, "maxResults": max})
}

func fieldJSON(f Field) map[string]any {
	sch := map[string]any{"type": f.Type}
	if f.Items != "" {
		sch["items"] = f.Items
	}
	if f.Custom != "" {
		sch["custom"] = f.Custom
	}
	return map[string]any{"fieldId": f.ID, "key": f.ID, "name": f.Name, "required": f.Required, "schema": sch}
}

func (s *Server) createmetaFields(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs := s.fields[r.PathValue("p")+"/"+r.PathValue("t")]
	start, max := startMax(r, 50)
	var vals []any
	for _, f := range window(fs, start, max) {
		vals = append(vals, fieldJSON(f))
	}
	writeJSON(w, map[string]any{"fields": vals, "total": len(fs), "startAt": start, "maxResults": max})
}

// query is the parsed subset of JQL that gfs sends.
type query struct {
	projects []string
	ids      []string
	since    *time.Time
	asc      bool
}

var (
	projectEq = regexp.MustCompile(`^project\s*=\s*"?([A-Z][A-Z0-9_]*)"?$`)
	projectIn = regexp.MustCompile(`^project\s+in\s*\((.*)\)$`)
	idIn      = regexp.MustCompile(`^id\s+in\s*\((.*)\)$`)
	updatedGE = regexp.MustCompile(`^updated\s*>=\s*"([^"]+)"$`)
	relative  = regexp.MustCompile(`^-(\d+)([mhdw])$`)
	orderBy   = regexp.MustCompile(`(?i)\s+ORDER BY updated (ASC|DESC)$`)
)

func unquote(list string) []string {
	var out []string
	for _, v := range strings.Split(list, ",") {
		if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) parseJQL(jql string) (query, error) {
	var q query
	if m := orderBy.FindStringSubmatch(jql); m != nil {
		q.asc = strings.EqualFold(m[1], "ASC")
		jql = jql[:len(jql)-len(m[0])]
	}
	for _, c := range strings.Split(jql, " AND ") {
		c = strings.TrimSpace(c)
		switch {
		case projectEq.MatchString(c):
			q.projects = []string{projectEq.FindStringSubmatch(c)[1]}
		case projectIn.MatchString(c):
			q.projects = unquote(projectIn.FindStringSubmatch(c)[1])
		case idIn.MatchString(c):
			q.ids = unquote(idIn.FindStringSubmatch(c)[1])
		case updatedGE.MatchString(c):
			v := updatedGE.FindStringSubmatch(c)[1]
			var t time.Time
			if m := relative.FindStringSubmatch(v); m != nil {
				n, _ := strconv.Atoi(m[1])
				unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
				t = s.now().Add(-time.Duration(n) * unit)
			} else if d, err := time.Parse("2006-01-02", v); err == nil {
				t = d
			} else {
				return q, fmt.Errorf("bad date %q", v)
			}
			q.since = &t
		default:
			return q, fmt.Errorf("unsupported JQL clause %q", c)
		}
	}
	return q, nil
}

func (s *Server) matching(q query) []*Issue {
	var out []*Issue
	for _, is := range s.issues {
		if len(q.projects) > 0 && !contains(q.projects, is.Project) {
			continue
		}
		if len(q.ids) > 0 && !contains(q.ids, is.ID) {
			continue
		}
		if q.since != nil && is.Updated.Before(*q.since) {
			continue
		}
		out = append(out, is)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.Before(out[j].Updated) == q.asc
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JQL           string   `json:"jql"`
		Fields        []string `json:"fields"`
		MaxResults    int      `json:"maxResults"`
		NextPageToken string   `json:"nextPageToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.parseJQL(req.JQL)
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	max := req.MaxResults
	if max <= 0 || max > 100 {
		max = 50
	}
	start, _ := strconv.Atoi(req.NextPageToken)
	all := s.matching(q)
	var out []any
	for _, is := range window(all, start, max) {
		out = append(out, s.issueJSON(is, req.Fields, true))
	}
	resp := map[string]any{"issues": out, "isLast": start+max >= len(all)}
	if start+max < len(all) {
		resp["nextPageToken"] = strconv.Itoa(start + max)
	}
	writeJSON(w, resp)
}

func (s *Server) count(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JQL string `json:"jql"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.parseJQL(req.JQL)
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	writeJSON(w, map[string]any{"count": len(s.matching(q))})
}

// lookup finds an issue by id or key.
func (s *Server) lookup(idOrKey string) *Issue {
	if is := s.issues[idOrKey]; is != nil {
		return is
	}
	for _, is := range s.issues {
		if is.Key == idOrKey {
			return is
		}
	}
	return nil
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "Issue does not exist or you do not have permission to see it.", nil)
		return
	}
	var fields []string
	if f := r.URL.Query().Get("fields"); f != "" {
		fields = strings.Split(f, ",")
	}
	writeJSON(w, s.issueJSON(is, fields, false))
}

func (s *Server) getComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	start, max := startMax(r, 50)
	var out []any
	for _, c := range window(is.Comments, start, max) {
		out = append(out, s.commentJSON(is, c))
	}
	writeJSON(w, map[string]any{"comments": out, "total": len(is.Comments), "startAt": start, "maxResults": max})
}

func (s *Server) getWorklogs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := s.lookup(r.PathValue("id"))
	if is == nil {
		jiraError(w, 404, "no issue", nil)
		return
	}
	start, max := startMax(r, 5000)
	var out []any
	for _, wl := range window(is.Worklogs, start, max) {
		out = append(out, s.worklogJSON(wl))
	}
	writeJSON(w, map[string]any{"worklogs": out, "total": len(is.Worklogs), "startAt": start, "maxResults": max})
}

func (s *Server) userSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(r.URL.Query().Get("query"))
	var accounts []string
	for a, u := range s.users {
		if q != "" && (strings.Contains(strings.ToLower(u.Name), q) || (u.Email != "" && strings.ToLower(u.Email) == q)) {
			accounts = append(accounts, a)
		}
	}
	sort.Strings(accounts)
	out := []any{}
	for _, a := range accounts {
		out = append(out, s.userJSON(a))
	}
	writeJSON(w, out)
}

func (s *Server) getLinkTypes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []any
	for _, lt := range s.linkTypes {
		out = append(out, map[string]any{"id": lt.ID, "name": lt.Name, "inward": lt.Inward, "outward": lt.Outward})
	}
	writeJSON(w, map[string]any{"issueLinkTypes": out})
}

func (s *Server) myPermissions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{"permissions": map[string]any{"ADMINISTER": map[string]any{"havePermission": s.Admin}}})
}

func (s *Server) workflowScheme(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Admin {
		jiraError(w, 403, "You do not have permission", nil)
		return
	}
	pid := r.URL.Query().Get("projectId")
	var p *Project
	for _, x := range s.projects {
		if x.ID == pid {
			p = x
		}
	}
	if p == nil {
		writeJSON(w, map[string]any{"values": []any{}})
		return
	}
	mappings := map[string]string{}
	for _, t := range s.types[p.Key] {
		if wf := s.workflows[p.Key+"/"+t.ID]; wf != nil {
			mappings[t.ID] = wf.Name
		}
	}
	writeJSON(w, map[string]any{"values": []any{map[string]any{"projectIds": []string{pid},
		"workflowScheme": map[string]any{"defaultWorkflow": defaultWorkflow.Name, "issueTypeMappings": mappings}}}})
}

func (s *Server) workflowSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Admin {
		jiraError(w, 403, "You do not have permission", nil)
		return
	}
	name := r.URL.Query().Get("workflowName")
	var wf *Workflow
	if name == defaultWorkflow.Name {
		wf = &defaultWorkflow
	}
	for _, x := range s.workflows {
		if x.Name == name {
			wf = x
		}
	}
	if wf == nil {
		writeJSON(w, map[string]any{"values": []any{}, "total": 0, "isLast": true})
		return
	}
	idOf := map[string]string{}
	var sts []any
	for _, st := range wf.Statuses {
		idOf[st.Name] = st.ID
		sts = append(sts, map[string]any{"id": st.ID, "name": st.Name})
	}
	var ts []any
	for _, t := range wf.Transitions {
		from := []string{}
		for _, f := range t.From {
			from = append(from, idOf[f])
		}
		typ := "global"
		if len(t.From) > 0 {
			typ = "directed"
		}
		ts = append(ts, map[string]any{"id": t.ID, "name": t.Name, "from": from, "to": idOf[t.To], "type": typ})
	}
	ts = append(ts, map[string]any{"id": "1", "name": "Create", "from": []string{}, "to": wf.Statuses[0].ID, "type": "initial"})
	writeJSON(w, map[string]any{"values": []any{map[string]any{"id": map[string]any{"name": wf.Name},
		"statuses": sts, "transitions": ts}}, "total": 1, "isLast": true})
}

func (s *Server) projectStatuses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.PathValue("key")
	var out []any
	for _, t := range s.types[key] {
		wf := s.workflow(&Issue{Project: key, Type: t.ID})
		var sts []any
		for _, st := range wf.Statuses {
			sts = append(sts, map[string]any{"id": st.ID, "name": st.Name, "statusCategory": map[string]any{"key": st.Category}})
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "statuses": sts})
	}
	writeJSON(w, out)
}

func (s *Server) attachmentContent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		for _, a := range is.Attachments {
			if a.ID == r.PathValue("id") {
				w.Header().Set("Content-Type", a.Mime)
				w.Write(a.Data)
				return
			}
		}
	}
	jiraError(w, 404, "no attachment", nil)
}

func (s *Server) allStatuses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []any
	wfs := []*Workflow{&defaultWorkflow}
	for _, wf := range s.workflows {
		wfs = append(wfs, wf)
	}
	for _, wf := range wfs {
		for _, st := range wf.Statuses {
			if !seen[st.ID] {
				seen[st.ID] = true
				out = append(out, map[string]any{"id": st.ID, "name": st.Name, "statusCategory": map[string]any{"key": st.Category}})
			}
		}
	}
	writeJSON(w, out)
}

// ID is the id of the issue with key, or "".
func (s *Server) ID(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.issues {
		if is.Key == key {
			return is.ID
		}
	}
	return ""
}
