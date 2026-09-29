package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type space struct {
	key, id, dir string // "HF", "98307", "hf"
	loaded       bool   // its pages are in tree
}

type session struct {
	c      *client
	sel    selection
	spaces map[string]*space  // by dir
	byID   map[string]*space  // by space id
	tree   map[string]pageRef // pages of the loaded spaces
	names  map[string]string
	now    func() time.Time
}

func openSession(ctx context.Context, t target) (*session, error) {
	s := &session{c: newClient(t), sel: t.sel, spaces: map[string]*space{}, byID: map[string]*space{},
		tree: map[string]pageRef{}, names: map[string]string{}, now: time.Now}
	if err := s.resolveSpaces(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveSpaces turns the selection into spaces (site clone spec §4.1).
func (s *session) resolveSpaces(ctx context.Context) error {
	path := "/wiki/api/v2/spaces?limit=250"
	if len(s.sel.keys) > 0 {
		esc := make([]string, len(s.sel.keys))
		for i, k := range s.sel.keys {
			esc[i] = url.QueryEscape(k)
		}
		path += "&keys=" + strings.Join(esc, ",") // no status: a named archived space is included
	} else {
		path += "&status=current"
		if s.sel.typ != "all" {
			path += "&type=" + s.sel.typ
		}
	}
	excluded := map[string]bool{}
	for _, k := range s.sel.exclude {
		excluded[k] = true
	}
	err := s.c.paginate(ctx, path, func(raw json.RawMessage) error {
		var a struct{ ID, Key string }
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if !excluded[a.Key] {
			sp := &space{key: a.Key, id: a.ID, dir: strings.ToLower(a.Key)}
			s.spaces[sp.dir], s.byID[sp.id] = sp, sp
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []string
	for _, k := range s.sel.keys {
		if s.spaces[strings.ToLower(k)] == nil && !excluded[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("space %s not found or not visible", strings.Join(missing, ", "))
	}
	return nil
}

func (s *session) sortedSpaces() []*space {
	out := make([]*space, 0, len(s.spaces))
	for _, sp := range s.spaces {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out
}

func (s *session) dirList() string {
	var ds []string
	for _, sp := range s.sortedSpaces() {
		ds = append(ds, sp.dir)
	}
	return strings.Join(ds, ", ")
}

// spaceFor maps a working path's first folder to its space (site clone spec §4.5).
func (s *session) spaceFor(p string) (*space, error) {
	dir, _, ok := strings.Cut(p, "/")
	if !ok {
		return nil, fmt.Errorf("pages must live in a space folder (%s)", s.dirList())
	}
	sp := s.spaces[dir]
	if sp == nil {
		return nil, fmt.Errorf("no space %q in this tree; gfs does not create spaces (spaces: %s)", dir, s.dirList())
	}
	return sp, nil
}

// keyOf is a bridge until Task 5: the space key of a working path, or "".
func (s *session) keyOf(p string) string {
	if sp, err := s.spaceFor(p); err == nil {
		return sp.key
	}
	return ""
}

func (s *session) Close() error { return nil }

// Identity is the account this session acts as (adapter.Identified).
func (s *session) Identity() string { return s.c.t.email }

// load reads sp's page tree once per session (site clone spec §4.2).
func (s *session) load(ctx context.Context, sp *space) error {
	if sp.loaded {
		return nil
	}
	fresh := map[string]pageRef{}
	err := s.c.paginate(ctx, "/wiki/api/v2/spaces/"+sp.id+"/pages?limit=250", func(raw json.RawMessage) error {
		var p apiPage
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		fresh[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
		return nil
	})
	if err != nil {
		return fmt.Errorf("space %s: %w", sp.key, err)
	}
	for id, r := range s.tree {
		if r.Space == sp.id {
			delete(s.tree, id)
		}
	}
	for id, r := range fresh {
		s.tree[id] = r
	}
	sp.loaded = true
	return nil
}

func (s *session) loadAll(ctx context.Context) error {
	for _, sp := range s.sortedSpaces() {
		if err := s.load(ctx, sp); err != nil {
			return err
		}
	}
	return nil
}

// paths maps sp's pages to working paths; a path depends only on its own space.
func (s *session) paths(sp *space) map[string]string {
	var refs []pageRef
	for _, r := range s.tree {
		if r.Space == sp.id {
			refs = append(refs, r)
		}
	}
	return pagePaths(sp.dir, refs)
}

func (s *session) name(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}
	if n, ok := s.names[accountID]; ok {
		return n
	}
	var u struct {
		DisplayName string `json:"displayName"`
	}
	n := accountID
	if s.c.do(ctx, http.MethodGet, "/wiki/rest/api/user?accountId="+url.QueryEscape(accountID), nil, &u) == nil && u.DisplayName != "" {
		n = u.DisplayName
	}
	s.names[accountID] = n
	return n
}

func (s *session) List(ctx context.Context, _ string) (adapter.Listing, error) {
	if err := s.loadAll(ctx); err != nil {
		return adapter.Listing{}, err
	}
	ids := make([]string, 0, len(s.tree))
	for id := range s.tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	l := adapter.Listing{Full: true}
	for _, id := range ids {
		r, err := s.Fetch(ctx, id)
		if errors.Is(err, adapter.ErrNotFound) {
			continue // deleted while listing
		}
		if err != nil {
			return adapter.Listing{}, err
		}
		l.Resources = append(l.Resources, *r)
	}
	return l, nil
}

func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	var p apiPage
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+id+"?body-format=storage", nil, &p); err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			delete(s.tree, id)
		}
		return nil, err
	}
	sp := s.byID[p.SpaceID]
	if sp == nil {
		delete(s.tree, id)
		return nil, fmt.Errorf("%w: page %s is in a space outside this tree", adapter.ErrNotFound, id)
	}
	if err := s.load(ctx, sp); err != nil {
		return nil, err
	}
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
	var labels []string
	err := s.c.paginate(ctx, "/wiki/api/v2/pages/"+id+"/labels?limit=250", func(raw json.RawMessage) error {
		var l struct{ Name, Prefix string }
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		if l.Prefix == "global" {
			labels = append(labels, l.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var comments []apiComment
	err = s.c.paginate(ctx, "/wiki/api/v2/pages/"+id+"/footer-comments?body-format=storage&limit=250", func(raw json.RawMessage) error {
		var c apiComment
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		comments = append(comments, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	atts, err := s.listAttachments(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := pageNode(p, labels, comments, func(a string) string { return s.name(ctx, a) })
	if err != nil {
		return nil, err
	}
	root.Children = append(root.Children, attachmentNodes(atts, func(a string) string { return s.name(ctx, a) })...)
	return &adapter.Resource{ID: p.ID, Version: strconv.Itoa(p.Version.Number), Path: s.paths(sp)[p.ID],
		By: s.name(ctx, p.Version.AuthorID), At: p.Version.CreatedAt, Root: root}, nil
}

func storageBody(v string) map[string]any {
	return map[string]any{"representation": "storage", "value": v}
}

// titleTaken reports whether another page in the space already has title.
func (s *session) titleTaken(title, except string) bool {
	for _, r := range s.tree {
		if r.Title == title && r.ID != except {
			return true
		}
	}
	return false
}

func (s *session) parentFor(p string, idByPath func(string) (string, bool)) (string, error) {
	if _, err := s.spaceFor(p); err != nil {
		return "", err
	}
	parent := parentPath(p)
	if parent == "" {
		return "", nil
	}
	id, ok := idByPath(parent)
	if !ok {
		return "", fmt.Errorf("parent page %s is not on the remote yet; commit it first", parent)
	}
	return id, nil
}

// pagePut computes the title and parent for the combined page update.
func (s *session) pagePut(req adapter.ApplyRequest, idx []int) (title, parent string, err error) {
	title = titleOf(req.Local.Root)
	titleChanged := false
	for _, i := range idx {
		a := req.Actions[i]
		titleChanged = titleChanged || (a.Verb == "update" && a.Group == "title")
	}
	for _, i := range idx {
		a := req.Actions[i]
		if a.Verb != "move" {
			continue
		}
		if parentPath(a.To) == "" {
			return "", "", errors.New("moving a page to the space root is not supported; move it under a page")
		}
		if parent, err = s.parentFor(a.To, req.IDByPath); err != nil {
			return "", "", err
		}
		if !titleChanged && sanitize(title) != baseName(a.To) {
			title = baseName(a.To)
		}
	}
	return title, parent, nil
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	if err := s.loadAll(ctx); err != nil {
		return []adapter.Result{{Action: req.Actions[0], Err: err}}
	}
	var out []adapter.Result
	for i, a := range req.Actions {
		res := adapter.Result{Action: a}
		switch {
		case a.IsAttachment():
			res.Err = checkAttachment(req, a)
		case a.Target != "":
		case a.Verb == "create":
			title := titleOf(req.Local.Root)
			if title == "" {
				title = baseName(req.Local.Path)
			}
			if _, err := s.parentFor(req.Local.Path, req.IDByPath); err != nil {
				res.Err = err
			} else if s.titleTaken(title, "") {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, s.keyOf(req.Local.Path))
			}
		case a.Verb == "move" || (a.Verb == "update" && a.Group == "title"):
			title, _, err := s.pagePut(req, []int{i})
			if err != nil {
				res.Err = err
			} else if s.titleTaken(title, req.Local.ID) {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, s.keyOf(req.Local.Path))
			}
		case a.Verb == "update" || a.Verb == "delete":
		default:
			res.Err = fmt.Errorf("confluence has no action %q", a.Verb)
		}
		out = append(out, res)
	}
	return out
}

func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	if err := s.loadAll(ctx); err != nil {
		return []adapter.Result{{Action: req.Actions[0], Err: err, Code: codeOf(err)}}
	}
	out := make([]adapter.Result, len(req.Actions))
	var pageIdx, labelIdx, commentIdx, attIdx []int
	for i, a := range req.Actions {
		out[i].Action = a
		switch {
		case a.IsAttachment():
			attIdx = append(attIdx, i)
		case a.Target != "":
			commentIdx = append(commentIdx, i)
		case a.Verb == "create":
			return s.create(ctx, req)
		case a.Verb == "delete":
			err := s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/pages/"+req.Base.ID, nil, nil)
			out[i].Err, out[i].Code = err, codeOf(err)
			delete(s.tree, req.Base.ID)
		case a.Verb == "update" && a.Group == "labels":
			labelIdx = append(labelIdx, i)
		case a.Verb == "move" || (a.Verb == "update" && (a.Group == "title" || a.Group == "body")):
			pageIdx = append(pageIdx, i)
		default:
			out[i].Err = fmt.Errorf("confluence has no action %q", a.Verb)
		}
	}
	id := req.Base.ID
	if len(pageIdx) > 0 {
		lock, _ := strconv.Atoi(req.Lock)
		title, parent, err := s.pagePut(req, pageIdx)
		detail := fmt.Sprintf("v%d -> v%d", lock, lock+1)
		if err == nil {
			body := map[string]any{"id": id, "status": "current", "title": title,
				"body":    storageBody(storageOf(req.Local.Root.Child("body"))),
				"version": map[string]any{"number": lock + 1, "message": "gfs"}}
			if parent != "" {
				body["parentId"] = parent
			}
			err = s.c.do(ctx, http.MethodPut, "/wiki/api/v2/pages/"+id, body, nil)
			if errors.Is(err, adapter.ErrLock) {
				return []adapter.Result{{Action: req.Actions[0], Err: err, Code: codeOf(err)}}
			}
			if err == nil {
				ref := s.tree[id]
				ref.Title = title
				if parent != "" {
					ref.Parent = parent
				}
				s.tree[id] = ref
			}
		}
		for _, i := range pageIdx {
			out[i].Err, out[i].Code = err, codeOf(err)
			if err == nil {
				out[i].Detail = detail
			}
		}
	}
	for _, i := range labelIdx {
		var baseLabels []string
		if req.Base != nil {
			baseLabels = labelsOf(req.Base.Root)
		}
		err := s.syncLabels(ctx, id, baseLabels, labelsOf(req.Local.Root))
		out[i].Err, out[i].Code = err, codeOf(err)
	}
	for _, i := range commentIdx {
		err := s.comment(ctx, id, req, req.Actions[i])
		out[i].Err, out[i].Code = err, codeOf(err)
	}
	for _, i := range attIdx {
		out[i].ID, out[i].Version, out[i].Err = s.attachment(ctx, id, req, req.Actions[i])
		out[i].Code = codeOf(out[i].Err)
	}
	return out
}

func (s *session) create(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	fail := func(err error) []adapter.Result {
		return []adapter.Result{{Action: act, Err: err, Code: codeOf(err)}}
	}
	parent, err := s.parentFor(req.Local.Path, req.IDByPath)
	if err != nil {
		return fail(err)
	}
	title := titleOf(req.Local.Root)
	if title == "" {
		title = baseName(req.Local.Path)
	}
	sp, _ := s.spaceFor(req.Local.Path)
	body := map[string]any{"spaceId": sp.id, "status": "current", "title": title,
		"body": storageBody(storageOf(req.Local.Root.Child("body")))}
	if parent != "" {
		body["parentId"] = parent
	}
	var p apiPage
	if err := s.c.do(ctx, http.MethodPost, "/wiki/api/v2/pages", body, &p); err != nil {
		return fail(err)
	}
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID, Space: sp.id, Version: p.Version.Number}
	out := []adapter.Result{{Action: act, ID: p.ID, Detail: "id=" + p.ID}}
	if ls := labelsOf(req.Local.Root); len(ls) > 0 {
		if err := s.syncLabels(ctx, p.ID, nil, ls); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "update", Group: "labels"}, Err: err, Code: codeOf(err)})
		}
	}
	for n, c := range req.Local.Root.ChildrenNamed("comment") {
		if _, has := c.Attr("id"); has {
			continue
		}
		if err := s.postComment(ctx, p.ID, c); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "create", Target: fmt.Sprintf("comment[%d]", n+1)}, Err: err, Code: codeOf(err)})
		}
	}
	for _, a := range req.Actions {
		if !a.IsAttachment() {
			continue
		}
		attID, ver, err := s.attachment(ctx, p.ID, req, a)
		out = append(out, adapter.Result{Action: a, ID: attID, Version: ver, Err: err, Code: codeOf(err)})
	}
	return out
}

func (s *session) syncLabels(ctx context.Context, id string, have, want []string) error {
	hs, ws := map[string]bool{}, map[string]bool{}
	for _, l := range have {
		hs[l] = true
	}
	var add []map[string]string
	for _, l := range want {
		ws[l] = true
		if !hs[l] {
			add = append(add, map[string]string{"prefix": "global", "name": l})
		}
	}
	if len(add) > 0 {
		if err := s.c.do(ctx, http.MethodPost, "/wiki/rest/api/content/"+id+"/label", add, nil); err != nil {
			return err
		}
	}
	for _, l := range have {
		if !ws[l] {
			if err := s.c.do(ctx, http.MethodDelete, "/wiki/rest/api/content/"+id+"/label?name="+url.QueryEscape(l), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) postComment(ctx context.Context, pageID string, c *xmltree.Node) error {
	return s.c.do(ctx, http.MethodPost, "/wiki/api/v2/footer-comments",
		map[string]any{"pageId": pageID, "body": storageBody(storageOf(c))}, nil)
}

func (s *session) comment(ctx context.Context, pageID string, req adapter.ApplyRequest, a adapter.Action) error {
	name, id, nth, ok := changes.ParseTarget(a.Target)
	if !ok || name != "comment" {
		return fmt.Errorf("confluence has no sub-resource %q", a.Target)
	}
	switch a.Verb {
	case "create":
		i := 0
		for _, c := range req.Local.Root.ChildrenNamed("comment") {
			if _, has := c.Attr("id"); has {
				continue
			}
			if i++; i == nth {
				return s.postComment(ctx, pageID, c)
			}
		}
		return fmt.Errorf("%s not found in file", a.Target)
	case "update":
		local := validate.FindSub(req.Local.Root, "comment", "id", id)
		base := validate.FindSub(req.Base.Root, "comment", "id", id)
		if local == nil || base == nil {
			return fmt.Errorf("%s not found", a.Target)
		}
		v, _ := base.Attr("version")
		n, _ := strconv.Atoi(v)
		return s.c.do(ctx, http.MethodPut, "/wiki/api/v2/footer-comments/"+id,
			map[string]any{"version": map[string]any{"number": n + 1}, "body": storageBody(storageOf(local))}, nil)
	case "delete":
		return s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/footer-comments/"+id, nil, nil)
	}
	return fmt.Errorf("confluence has no action %q on %s", a.Verb, a.Target)
}
