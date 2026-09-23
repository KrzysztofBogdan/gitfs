// Package cftest is an in-memory Confluence Cloud (REST v2 subset) for tests.
package cftest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"sync"
)

type Page struct {
	ID, Title, ParentID, SpaceID, Storage, AuthorID, CreatedAt, UpdatedAt string
	Version                                                               int
	Labels                                                                []string
}

type Comment struct {
	ID, PageID, Storage, AuthorID, CreatedAt string
	Version                                  int
}

type Attachment struct {
	ID, PageID, Title, MediaType, AuthorID, CreatedAt string
	Data                                              []byte
	Version                                           int
}

type Server struct {
	*httptest.Server
	PageLimit int
	Requests  []string
	Fail      map[string]int // "METHOD /path" -> status returned instead of handling the request

	mu          sync.Mutex
	spaces      map[string]string // key -> id
	pages       map[string]*Page
	comments    map[string]*Comment
	attachments map[string]*Attachment
	users       map[string]string
	seq         int
	now         string
}

func New() *Server {
	s := &Server{PageLimit: 250, spaces: map[string]string{}, pages: map[string]*Page{},
		comments: map[string]*Comment{}, attachments: map[string]*Attachment{}, users: map[string]string{"me": "Me", "bob": "bob"},
		seq: 1000, now: "2026-09-23T12:00:00.000Z"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wiki/api/v2/spaces", s.getSpaces)
	mux.HandleFunc("GET /wiki/api/v2/spaces/{id}/pages", s.listPages)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}", s.getPage)
	mux.HandleFunc("POST /wiki/api/v2/pages", s.createPage)
	mux.HandleFunc("PUT /wiki/api/v2/pages/{id}", s.updatePage)
	mux.HandleFunc("DELETE /wiki/api/v2/pages/{id}", s.deletePage)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/labels", s.getLabels)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/footer-comments", s.getComments)
	mux.HandleFunc("POST /wiki/api/v2/footer-comments", s.createComment)
	mux.HandleFunc("PUT /wiki/api/v2/footer-comments/{id}", s.updateComment)
	mux.HandleFunc("DELETE /wiki/api/v2/footer-comments/{id}", s.deleteComment)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/label", s.addLabel)
	mux.HandleFunc("DELETE /wiki/rest/api/content/{id}/label", s.removeLabel)
	mux.HandleFunc("GET /wiki/rest/api/user", s.getUser)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/attachments", s.listAttachments)
	mux.HandleFunc("GET /wiki/api/v2/attachments/{id}", s.getAttachment)
	mux.HandleFunc("DELETE /wiki/api/v2/attachments/{id}", s.deleteAttachment)
	mux.HandleFunc("GET /wiki/download/attachments/{page}/{name}", s.downloadRedirect)
	mux.HandleFunc("GET /media/{id}", s.media)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/child/attachment", s.createAttachment)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/child/attachment/{att}/data", s.updateAttachment)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
		if u, p, ok := r.BasicAuth(); !ok || u == "" || p == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if code := s.Fail[r.Method+" "+r.URL.Path]; code != 0 {
			fail(w, code, "injected failure")
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return s
}

func (s *Server) next() string { s.seq++; return strconv.Itoa(s.seq) }

// ---- test helpers (lock-free callers: tests call them between requests) ----

func (s *Server) AddSpace(key, id string) { s.mu.Lock(); s.spaces[key] = id; s.mu.Unlock() }

func (s *Server) AddPage(p Page) *Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = s.next()
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.AuthorID == "" {
		p.AuthorID = "me"
	}
	if p.CreatedAt == "" {
		p.CreatedAt = "2026-01-01T10:00:00.000Z"
	}
	if p.UpdatedAt == "" {
		p.UpdatedAt = p.CreatedAt
	}
	s.pages[p.ID] = &p
	return &p
}

func (s *Server) AddComment(c Comment) *Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = s.next()
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.AuthorID == "" {
		c.AuthorID = "bob"
	}
	if c.CreatedAt == "" {
		c.CreatedAt = "2026-02-01T10:00:00.000Z"
	}
	s.comments[c.ID] = &c
	return &c
}

func (s *Server) EditPage(id string, f func(*Page)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pages[id]
	f(p)
	p.Version++
	p.AuthorID = "bob"
	p.UpdatedAt = s.now
}

func (s *Server) Page(id string) (*Page, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pages[id]
	return p, ok
}

func (s *Server) DeletePage(id string) { s.mu.Lock(); delete(s.pages, id); s.mu.Unlock() }

func (s *Server) Comments(pageID string) []*Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commentsOf(pageID)
}

func (s *Server) commentsOf(pageID string) []*Comment {
	var out []*Comment
	for _, c := range s.comments {
		if c.PageID == pageID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

func storage(v string) map[string]any {
	return map[string]any{"storage": map[string]any{"representation": "storage", "value": v}}
}

func (p *Page) json(withBody bool) map[string]any {
	m := map[string]any{"id": p.ID, "status": "current", "title": p.Title, "spaceId": p.SpaceID,
		"authorId": p.AuthorID, "createdAt": p.CreatedAt,
		"version": map[string]any{"number": p.Version, "authorId": p.AuthorID, "createdAt": p.UpdatedAt}}
	if p.ParentID != "" {
		m["parentId"] = p.ParentID
	} else {
		m["parentId"] = nil
	}
	if withBody {
		m["body"] = storage(p.Storage)
	}
	return m
}

func (c *Comment) json() map[string]any {
	return map[string]any{"id": c.ID, "status": "current", "pageId": c.PageID, "body": storage(c.Storage),
		"version": map[string]any{"number": c.Version, "authorId": c.AuthorID, "createdAt": c.CreatedAt}}
}

// paged writes one page of items; the cursor is a plain offset.
func (s *Server) paged(w http.ResponseWriter, r *http.Request, items []any) {
	limit := s.PageLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l < limit {
		limit = l
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(off+limit, len(items))
	resp := map[string]any{"results": items[min(off, len(items)):end], "_links": map[string]any{}}
	if end < len(items) {
		q := r.URL.Query()
		q.Set("cursor", strconv.Itoa(end))
		resp["_links"] = map[string]any{"next": r.URL.Path + "?" + q.Encode()}
	}
	writeJSON(w, 200, resp)
}

type bodyIn struct {
	Representation string `json:"representation"`
	Value          string `json:"value"`
}

type pageIn struct {
	ID, Status, Title, SpaceID, ParentID string
	Body                                 bodyIn
	Version                              struct{ Number int }
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func (s *Server) titleTaken(spaceID, title, except string) bool {
	for _, p := range s.pages {
		if p.SpaceID == spaceID && p.Title == title && p.ID != except {
			return true
		}
	}
	return false
}

// ---- handlers ----

func (s *Server) getSpaces(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("keys")
	var out []any
	if id, ok := s.spaces[key]; ok {
		home := ""
		for _, p := range s.pages {
			if p.SpaceID == id && p.ParentID == "" && (home == "" || p.ID < home) {
				home = p.ID
			}
		}
		out = append(out, map[string]any{"id": id, "key": key, "homepageId": home})
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (s *Server) sortedPages(spaceID string) []*Page {
	var ps []*Page
	for _, p := range s.pages {
		if p.SpaceID == spaceID {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	return ps
}

func (s *Server) listPages(w http.ResponseWriter, r *http.Request) {
	var items []any
	for _, p := range s.sortedPages(r.PathValue("id")) {
		items = append(items, p.json(false))
	}
	s.paged(w, r, items)
}

func (s *Server) getPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	writeJSON(w, 200, p.json(r.URL.Query().Get("body-format") == "storage"))
}

func (s *Server) createPage(w http.ResponseWriter, r *http.Request) {
	var in pageIn
	if err := decode(r, &in); err != nil || in.Title == "" || in.SpaceID == "" {
		fail(w, 400, "title and spaceId are required")
		return
	}
	if s.titleTaken(in.SpaceID, in.Title, "") {
		fail(w, 400, "A page with this title already exists")
		return
	}
	if in.ParentID != "" {
		if _, ok := s.pages[in.ParentID]; !ok {
			fail(w, 400, "parent not found")
			return
		}
	}
	p := &Page{ID: s.next(), Title: in.Title, ParentID: in.ParentID, SpaceID: in.SpaceID, Storage: in.Body.Value,
		AuthorID: "me", CreatedAt: s.now, UpdatedAt: s.now, Version: 1}
	s.pages[p.ID] = p
	writeJSON(w, 200, p.json(true))
}

func (s *Server) updatePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var in pageIn
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Version.Number != p.Version+1 {
		fail(w, 409, fmt.Sprintf("Version must be incremented when updating a page. Current Version: [%d]. Provided version: [%d]", p.Version, in.Version.Number))
		return
	}
	if s.titleTaken(p.SpaceID, in.Title, p.ID) {
		fail(w, 400, "A page with this title already exists")
		return
	}
	if in.ParentID != "" {
		if _, ok := s.pages[in.ParentID]; !ok {
			fail(w, 400, "parent not found")
			return
		}
		p.ParentID = in.ParentID
	}
	p.Title, p.Storage = in.Title, in.Body.Value
	p.Version++
	p.AuthorID, p.UpdatedAt = "me", s.now
	writeJSON(w, 200, p.json(true))
}

func (s *Server) deletePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	delete(s.pages, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) getLabels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for i, l := range p.Labels {
		items = append(items, map[string]any{"id": strconv.Itoa(i + 1), "name": l, "prefix": "global"})
	}
	s.paged(w, r, items)
}

func (s *Server) getComments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for _, c := range s.commentsOf(r.PathValue("id")) {
		items = append(items, c.json())
	}
	s.paged(w, r, items)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PageID string `json:"pageId"`
		Body   bodyIn
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, ok := s.pages[in.PageID]; !ok {
		fail(w, 404, "page not found")
		return
	}
	c := &Comment{ID: s.next(), PageID: in.PageID, Storage: in.Body.Value, AuthorID: "me", CreatedAt: s.now, Version: 1}
	s.comments[c.ID] = c
	writeJSON(w, 200, c.json())
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	c, ok := s.comments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "comment not found")
		return
	}
	var in struct {
		Body    bodyIn
		Version struct{ Number int }
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Version.Number != c.Version+1 {
		fail(w, 409, "version conflict")
		return
	}
	c.Storage, c.Version = in.Body.Value, c.Version+1
	writeJSON(w, 200, c.json())
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.comments[r.PathValue("id")]; !ok {
		fail(w, 404, "comment not found")
		return
	}
	delete(s.comments, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) addLabel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var in []struct{ Prefix, Name string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	for _, l := range in {
		dup := false
		for _, have := range p.Labels {
			dup = dup || have == l.Name
		}
		if !dup {
			p.Labels = append(p.Labels, l.Name)
		}
	}
	writeJSON(w, 200, map[string]any{"results": []any{}})
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	name := r.URL.Query().Get("name")
	var kept []string
	for _, l := range p.Labels {
		if l != name {
			kept = append(kept, l)
		}
	}
	p.Labels = kept
	w.WriteHeader(204)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("accountId")
	name, ok := s.users[id]
	if !ok {
		fail(w, 404, "user not found")
		return
	}
	writeJSON(w, 200, map[string]any{"accountId": id, "displayName": name, "publicName": name})
}

func (s *Server) AddAttachment(a Attachment) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = "att" + s.next()
	}
	if a.Version == 0 {
		a.Version = 1
	}
	if a.AuthorID == "" {
		a.AuthorID = "me"
	}
	if a.CreatedAt == "" {
		a.CreatedAt = "2026-03-01T10:00:00.000Z"
	}
	if a.MediaType == "" {
		a.MediaType = "application/octet-stream"
	}
	s.attachments[a.ID] = &a
	return &a
}

// EditAttachment replaces an attachment's bytes as another user would.
func (s *Server) EditAttachment(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attachments[id]
	a.Data, a.Version, a.AuthorID, a.CreatedAt = data, a.Version+1, "bob", s.now
}

func (s *Server) Attachment(id string) (*Attachment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attachments[id]
	return a, ok
}

func (s *Server) Attachments(pageID string) []*Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachmentsOf(pageID)
}

func (s *Server) attachmentsOf(pageID string) []*Attachment {
	var out []*Attachment
	for _, a := range s.attachments {
		if a.PageID == pageID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *Attachment) json() map[string]any {
	return map[string]any{"id": a.ID, "status": "current", "title": a.Title, "pageId": a.PageID,
		"mediaType": a.MediaType, "fileSize": len(a.Data),
		"downloadLink": fmt.Sprintf("/download/attachments/%s/%s?version=%d&api=v2", a.PageID, url.PathEscape(a.Title), a.Version),
		"version":      map[string]any{"number": a.Version, "authorId": a.AuthorID, "createdAt": a.CreatedAt}}
}

// v1json is the shape of the REST v1 upload responses.
func (a *Attachment) v1json() map[string]any {
	return map[string]any{"id": a.ID, "type": "attachment", "title": a.Title, "version": map[string]any{"number": a.Version}}
}

func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for _, a := range s.attachmentsOf(r.PathValue("id")) {
		items = append(items, a.json())
	}
	s.paged(w, r, items)
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "attachment not found")
		return
	}
	writeJSON(w, 200, a.json())
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.attachments[r.PathValue("id")]; !ok {
		fail(w, 404, "attachment not found")
		return
	}
	delete(s.attachments, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) downloadRedirect(w http.ResponseWriter, r *http.Request) {
	for _, a := range s.attachments {
		if a.PageID == r.PathValue("page") && a.Title == r.PathValue("name") {
			http.Redirect(w, r, "/media/"+a.ID, http.StatusFound)
			return
		}
	}
	fail(w, 404, "attachment not found")
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "attachment not found")
		return
	}
	w.Header().Set("Content-Type", a.MediaType)
	w.Write(a.Data)
}

// readUpload reads the multipart "file" part of an upload request.
func readUpload(w http.ResponseWriter, r *http.Request) (name, mediaType string, data []byte, ok bool) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" {
		fail(w, 403, "XSRF check failed")
		return "", "", nil, false
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, err.Error())
		return "", "", nil, false
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, err.Error())
			return "", "", nil, false
		}
		if part.FormName() == "file" {
			name, mediaType = part.FileName(), part.Header.Get("Content-Type")
			data, _ = io.ReadAll(part)
		}
	}
	if name == "" {
		fail(w, 400, "a file part is required")
		return "", "", nil, false
	}
	return name, mediaType, data, true
}

func (s *Server) createAttachment(w http.ResponseWriter, r *http.Request) {
	pageID := r.PathValue("id")
	if _, ok := s.pages[pageID]; !ok {
		fail(w, 404, "page not found")
		return
	}
	name, mediaType, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	for _, a := range s.attachmentsOf(pageID) {
		if a.Title == name {
			fail(w, 400, "Cannot add a new attachment with same file name as an existing attachment: "+name)
			return
		}
	}
	a := &Attachment{ID: "att" + s.next(), PageID: pageID, Title: name, MediaType: mediaType, Data: data,
		Version: 1, AuthorID: "me", CreatedAt: s.now}
	s.attachments[a.ID] = a
	writeJSON(w, 200, map[string]any{"results": []any{a.v1json()}})
}

func (s *Server) updateAttachment(w http.ResponseWriter, r *http.Request) {
	a, ok := s.attachments[r.PathValue("att")]
	if !ok || a.PageID != r.PathValue("id") {
		fail(w, 404, "attachment not found")
		return
	}
	_, mediaType, data, ok := readUpload(w, r)
	if !ok {
		return
	}
	a.Data, a.MediaType, a.Version, a.AuthorID, a.CreatedAt = data, mediaType, a.Version+1, "me", s.now
	writeJSON(w, 200, a.v1json())
}
