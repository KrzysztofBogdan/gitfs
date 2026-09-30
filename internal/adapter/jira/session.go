package jira

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// session is agent mode: every selected project of a site (jira spec §6, §7).
type session struct {
	c         *atlassian.Client
	t         target
	projects  map[string]*projectMeta // selected, by key
	meta      *metaCache              // every cached project; the selected ones share pointers
	reg       *registry
	wf        *xmltree.Node // .workflows.xml once built or loaded
	wfStale   bool          // metadata changed since wf was built
	wfDirty   bool          // wf changed since load
	metaDirty bool
	cacheDir  string // <tree>/.gfs/cache/jira; "" without a tree
	report    func(adapter.Progress)
	now       func() time.Time
	linkTypes []linkType // issue link types, read on first use (subs.go)
}

func openSession(ctx context.Context, t target) (*session, error) {
	s := &session{c: atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Jira"), t: t,
		projects: map[string]*projectMeta{}, meta: &metaCache{Projects: map[string]*projectMeta{}},
		reg: newRegistry(), report: func(adapter.Progress) {}, now: time.Now}
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	if err := s.resolveProjects(ctx); err != nil {
		return nil, authHint(err, t)
	}
	for k, p := range s.projects {
		s.meta.Projects[k] = p
	}
	return s, nil
}

// authHint says how to fix a token Jira refused (jira spec §10).
func authHint(err error, t target) error {
	if atlassian.Code(err) != "401" {
		return err
	}
	return fmt.Errorf("%w; check the token with gfs auth set %s --host %s", err, t.email, t.host)
}

// resolveProjects turns the selection into projects (jira spec §3.1).
func (s *session) resolveProjects(ctx context.Context) error {
	want := map[string]bool{}
	for _, k := range s.t.sel.keys {
		want[k] = true
	}
	excluded := map[string]bool{}
	for _, k := range s.t.sel.exclude {
		excluded[k] = true
	}
	for start := 0; ; {
		var resp struct {
			Values []struct {
				ID, Key        string
				ProjectTypeKey string `json:"projectTypeKey"`
			} `json:"values"`
			IsLast bool `json:"isLast"`
		}
		if err := s.c.Do(ctx, http.MethodGet, fmt.Sprintf("/rest/api/3/project/search?startAt=%d&maxResults=50", start), nil, &resp); err != nil {
			return err
		}
		for _, v := range resp.Values {
			if excluded[v.Key] || (len(want) > 0 && !want[v.Key]) {
				continue
			}
			s.projects[v.Key] = &projectMeta{Key: v.Key, ID: v.ID, JSM: v.ProjectTypeKey == "service_desk"}
		}
		start += len(resp.Values)
		if resp.IsLast || len(resp.Values) == 0 {
			break
		}
	}
	var missing []string
	for _, k := range s.t.sel.keys {
		if s.projects[k] == nil && !excluded[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("project %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *session) sorted() []*projectMeta {
	out := make([]*projectMeta, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Identity is the account this session acts as (adapter.Identified).
func (s *session) Identity() string { return s.t.email }

// SetProgress reports List's progress to f (adapter.Reporter).
func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }

func (s *session) cachePath(name string) string { return filepath.Join(s.cacheDir, name) }

// UseCache loads metadata, people and workflows kept by earlier runs
// (adapter.Cacher). A cached project whose id changed is ignored.
func (s *session) UseCache(dir string) {
	s.cacheDir = filepath.Join(dir, "jira")
	if m, err := loadMeta(s.cachePath("meta.json")); err == nil {
		for k, p := range s.projects {
			if c := m.Projects[k]; c != nil && c.ID == p.ID && p.Types == nil {
				p.Types = c.Types
			}
			m.Projects[k] = p
		}
		s.meta = m
	}
	s.reg.load(s.cachePath("people.json"))
	if data, err := os.ReadFile(s.cachePath("workflows.xml")); err == nil {
		if n, err := xmltree.ParseString(string(data)); err == nil {
			s.wf = n
		}
	}
}

// Close saves what changed back to the cache.
func (s *session) Close() error {
	if s.cacheDir == "" {
		return nil
	}
	var errs []error
	if s.metaDirty {
		errs = append(errs, s.meta.save(s.cachePath("meta.json")))
	}
	if s.reg.dirty {
		errs = append(errs, s.reg.save(s.cachePath("people.json")))
	}
	if s.wfDirty && s.wf != nil {
		errs = append(errs, writeFileAtomic(s.cachePath("workflows.xml"), []byte(xmltree.Print(s.wf, 0)+"\n")))
	}
	return errors.Join(errs...)
}

// loadTypes reads p's issue types and their create-screen fields (createmeta).
func (s *session) loadTypes(ctx context.Context, p *projectMeta) error {
	types := map[string]*typeMeta{}
	for start := 0; ; {
		var resp struct {
			IssueTypes []struct {
				ID, Name string
				Subtask  bool
			} `json:"issueTypes"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/createmeta/%s/issuetypes?startAt=%d&maxResults=50", url.PathEscape(p.Key), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return fmt.Errorf("issue types of %s: %w", p.Key, err)
		}
		for _, it := range resp.IssueTypes {
			fs, err := s.loadFields(ctx, p.Key, it.ID)
			if err != nil {
				return err
			}
			types[it.ID] = &typeMeta{ID: it.ID, Name: it.Name, Subtask: it.Subtask, Fields: fs}
		}
		start += len(resp.IssueTypes)
		if len(resp.IssueTypes) == 0 || start >= resp.Total {
			break
		}
	}
	p.Types = types
	s.metaDirty, s.wfStale = true, true
	return nil
}

func (s *session) loadFields(ctx context.Context, key, typeID string) (map[string]fieldMeta, error) {
	out := map[string]fieldMeta{}
	for start := 0; ; {
		var resp struct {
			Fields []struct {
				FieldID  string `json:"fieldId"`
				Name     string `json:"name"`
				Required bool   `json:"required"`
				Schema   struct {
					Type, Items, Custom string
				} `json:"schema"`
			} `json:"fields"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/createmeta/%s/issuetypes/%s?startAt=%d&maxResults=200", url.PathEscape(key), url.PathEscape(typeID), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, fmt.Errorf("fields of %s type %s: %w", key, typeID, err)
		}
		for _, f := range resp.Fields {
			out[f.FieldID] = fieldMeta{ID: f.FieldID, Name: f.Name, Type: f.Schema.Type, Items: f.Schema.Items,
				Custom: f.Schema.Custom, Required: f.Required}
		}
		start += len(resp.Fields)
		if len(resp.Fields) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

// setFor is the field set of p's issue type typeID. loaded reports that the
// types were read now, so an issue fetched with the old field list may lack
// some of them.
func (s *session) setFor(ctx context.Context, p *projectMeta, typeID string) (set map[string]fieldMeta, loaded bool, err error) {
	if p.Types == nil {
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, false, err
		}
		loaded = true
	}
	t := p.Types[typeID]
	if t == nil && !loaded { // a type created since the cache was filled
		if err := s.loadTypes(ctx, p); err != nil {
			return nil, false, err
		}
		loaded, t = true, p.Types[typeID]
	}
	if t == nil { // no create screen: the union of the project's types
		return projectFieldSet(p), loaded, nil
	}
	return fieldSet(t), loaded, nil
}

// searchFields is the field list for searching ps: every type's set plus the
// fields always requested.
func (s *session) searchFields(ctx context.Context, ps []*projectMeta) ([]string, error) {
	seen := map[string]bool{}
	for _, f := range alwaysFields {
		seen[f] = true
	}
	for _, p := range ps {
		if p.Types == nil {
			if err := s.loadTypes(ctx, p); err != nil {
				return nil, err
			}
		}
		for _, t := range p.Types {
			for id := range fieldSet(t) {
				seen[id] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

func (s *session) getIssue(ctx context.Context, id string) (apiIssue, error) {
	var is apiIssue
	err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(id)+"?fields=*all", nil, &is)
	return is, err
}

func (s *session) allComments(ctx context.Context, id string) ([]apiComment, error) {
	var out []apiComment
	for start := 0; ; {
		var resp struct {
			Comments []apiComment `json:"comments"`
			Total    int          `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/%s/comment?startAt=%d&maxResults=100", url.PathEscape(id), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Comments...)
		start += len(resp.Comments)
		if len(resp.Comments) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

func (s *session) allWorklogs(ctx context.Context, id string) ([]apiWorklog, error) {
	var out []apiWorklog
	for start := 0; ; {
		var resp struct {
			Worklogs []apiWorklog `json:"worklogs"`
			Total    int          `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/issue/%s/worklog?startAt=%d&maxResults=1000", url.PathEscape(id), start)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Worklogs...)
		start += len(resp.Worklogs)
		if len(resp.Worklogs) == 0 || start >= resp.Total {
			return out, nil
		}
	}
}

// resource turns is into a full resource. complete says is was fetched with
// every field (GET ?fields=*all) rather than a search's field list.
func (s *session) resource(ctx context.Context, is apiIssue, complete bool) (*adapter.Resource, error) {
	key, typeID := is.project()
	p := s.projects[key]
	if p == nil {
		return nil, fmt.Errorf("%w: %s is in project %s, outside this tree", adapter.ErrNotFound, is.Key, key)
	}
	set, loaded, err := s.setFor(ctx, p, typeID)
	if err != nil {
		return nil, err
	}
	if loaded && !complete {
		if is, err = s.getIssue(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	comments, total, err := page[apiComment](is.Fields["comment"], "comments")
	if err != nil {
		return nil, fmt.Errorf("%s comments: %w", is.Key, err)
	}
	if total > len(comments) {
		if comments, err = s.allComments(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	worklogs, total, err := page[apiWorklog](is.Fields["worklog"], "worklogs")
	if err != nil {
		return nil, fmt.Errorf("%s worklogs: %w", is.Key, err)
	}
	if total > len(worklogs) {
		if worklogs, err = s.allWorklogs(ctx, is.ID); err != nil {
			return nil, err
		}
	}
	root, err := decoder{reg: s.reg, jsm: p.JSM}.issue(is, set, comments, worklogs)
	if err != nil {
		return nil, err
	}
	updated := is.str("updated")
	return &adapter.Resource{ID: is.ID, Version: updated, Path: issuePath(p.dir(), is.Key, is.str("summary")), At: updated, Root: root}, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// List reports the selected projects' issues plus .people.xml and
// .workflows.xml (jira spec §6). With a cursor, projects listed before are
// searched for issues updated since; the others are listed in full.
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	start := s.now().UTC()
	prev := parseCursor(cursor)
	l := adapter.Listing{Full: len(prev) == 0}
	if l.Full { // clone or pull --full: rebuild .people.xml, reread field metadata
		s.reg.reset()
		for _, p := range s.projects {
			p.Types = nil
		}
	}
	next := map[string]time.Time{}
	var fresh, known []*projectMeta
	for _, p := range s.sorted() {
		if _, ok := prev[p.Key]; ok {
			known = append(known, p)
		} else {
			fresh = append(fresh, p)
		}
	}
	for i, p := range fresh {
		n, err := s.listProject(ctx, p, &l)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("project %s: %w", p.Key, err)
		}
		s.report(adapter.Progress{Phase: "pages", Done: i + 1, Total: len(fresh), Item: fmt.Sprintf("%s (%s)", p.Key, plural(n, "issue"))})
		if !l.Full {
			l.FullDirs = append(l.FullDirs, p.dir())
		}
		next[p.Key] = start
	}
	for k := range prev {
		if s.projects[k] == nil { // left the selection: all its files go
			l.FullDirs = append(l.FullDirs, strings.ToLower(k))
		}
	}
	sort.Strings(l.FullDirs)
	if len(known) > 0 {
		oldest := start
		for _, p := range known {
			if t := prev[p.Key]; t.Before(oldest) {
				oldest = t
			}
		}
		if err := s.listChanged(ctx, known, windowMinutes(oldest, start), &l); err != nil {
			return adapter.Listing{}, fmt.Errorf("%w (gfs pull --full lists everything without searching)", err)
		}
		for _, p := range known {
			next[p.Key] = start
		}
	}
	wf, err := s.workflows(ctx, len(fresh) > 0)
	if err != nil {
		return adapter.Listing{}, err
	}
	l.Resources = append(l.Resources, s.reg.resource(), wf)
	l.Cursor = formatCursor(next)
	return l, nil
}

// listProject lists every issue of p inside the since/limit window.
func (s *session) listProject(ctx context.Context, p *projectMeta, l *adapter.Listing) (int, error) {
	fields, err := s.searchFields(ctx, []*projectMeta{p})
	if err != nil {
		return 0, err
	}
	jql := fmt.Sprintf("project = %q", p.Key)
	if s.t.sel.since != "" {
		jql += fmt.Sprintf(" AND updated >= %q", sinceDate(s.t.sel.since, s.now()))
	}
	jql += " ORDER BY updated DESC"
	n := 0
	err = s.search(ctx, jql, fields, s.t.sel.limit, func(is apiIssue) error {
		r, err := s.resource(ctx, is, false)
		if errors.Is(err, adapter.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		l.Resources = append(l.Resources, *r)
		n++
		return nil
	})
	return n, err
}

// listChanged lists the issues of ps updated in the last mins minutes.
func (s *session) listChanged(ctx context.Context, ps []*projectMeta, mins int, l *adapter.Listing) error {
	fields, err := s.searchFields(ctx, ps)
	if err != nil {
		return err
	}
	jql := fmt.Sprintf(`project in (%s) AND updated >= "-%dm" ORDER BY updated ASC`, quoteKeys(ps), mins)
	return s.search(ctx, jql, fields, 0, func(is apiIssue) error {
		r, err := s.resource(ctx, is, false)
		if errors.Is(err, adapter.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		l.Resources = append(l.Resources, *r)
		return nil
	})
}

// Fetch returns one resource: an issue by id, or .people.xml / .workflows.xml.
func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	switch id {
	case peopleID:
		r := s.reg.resource()
		return &r, nil
	case workflowsID:
		r, err := s.workflows(ctx, false)
		return &r, err
	}
	is, err := s.getIssue(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resource(ctx, is, true)
}

// Download streams an attachment's bytes; Jira attachments have no versions.
func (s *session) Download(ctx context.Context, _ string, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	n, err := s.c.Download(ctx, "/rest/api/3/attachment/content/"+url.PathEscape(attID), w)
	return adapter.AttachmentInfo{Version: "-", Size: n}, err
}
