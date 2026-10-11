// Package fake is an in-memory adapter for engine tests.
package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var Schema = &schema.Schema{
	Root: "note", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "tags", Kind: schema.List, Item: "tag", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type"}}, BodyTypes: []string{"text/plain"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
		{Name: "attachment", Kind: schema.Attachment, ID: "id", SortKey: "created", NameAttr: "name", VersionAttr: "version",
			Ops: []string{"create", "update", "delete"},
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "name", ReadOnly: true}, {Name: "size", ReadOnly: true},
				{Name: "version", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

type attachment struct {
	id, name, created string
	data              []byte
	version           int
}

type record struct {
	path    string
	root    *xmltree.Node // never holds attachment elements; resource() adds them
	version int
	by      string
	atts    []*attachment
}

type Remote struct {
	FailVerb     map[string]error // keyed by verb, or verb+" "+target; "download" fails Download, "list" List
	LockFailures int              // Apply returns ErrLock this many times
	Calls        []string         // "verb path" per executed action ("verb file" for attachments)
	Published    []string         // "path channel"
	Downloads    int              // successful Download calls
	Cursors      []string         // cursor passed to each List call
	Partial      bool             // List returns Full: false
	FullDirs     []string         // returned as Listing.FullDirs
	WithFiles    bool             // listed resources carry their attachment bytes (Resource.Files)
	CacheDir     string           // last UseCache argument
	Advice       map[string]adapter.Advice
	CheckDetail  map[string]string        // "verb group" -> Result.Detail from Check
	CheckClass   map[string]string        // "verb group" -> Result.Class from Check
	CheckWarn    map[string]string        // "verb group" -> Result.Warning from Check
	Preview      map[string]bool          // verb -> Describe sets Action.Preview
	Checks       int                      // Check calls
	Warn         map[string]string        // Action.Detail -> Action.Warning set by Describe
	NeedSecret   bool                     // Apply reads a secret for comment creates
	Secrets      []string                 // secrets Apply read
	VanishOnFail bool                     // a failed sub update also removes that sub (a delete whose create failed)
	Stubs        bool                     // List returns stubs (no Root); the engine fetches them
	Missing      map[string]bool          // listed, but Fetch reports not found (deleted after listing)
	FetchLimit   int                      // Fetch fails after this many calls (0: never)
	Parent       func(path string) string // create: the working path whose id becomes the new record's parent ("" none)
	Parents      map[string]string        // record id -> parent id, set on create
	CreatePath   map[string]string        // create: working path -> remote path (a title-derived rename)
	FailCreate   map[string]error         // create of this working path fails
	fetches      int
	recs         map[string]*record
	seq          int
}

// next returns a fresh id, skipping ids already taken by Put.
func (r *Remote) next() string {
	for {
		r.seq++
		id := strconv.Itoa(r.seq)
		if _, taken := r.recs[id]; !taken {
			return id
		}
	}
}

func parse(x string) *xmltree.Node {
	n, err := xmltree.ParseString(x)
	if err != nil {
		panic(err)
	}
	return n
}

// Put stores a resource at version 1 (xml is the <note> root, without id/version).
func (r *Remote) Put(id, path, x string) {
	r.recs[id] = &record{path: path, root: parse(x), version: 1, by: "alice"}
}

func (r *Remote) Edit(id string, f func(root *xmltree.Node)) {
	rec := r.recs[id]
	f(rec.root)
	rec.version++
	rec.by = "bob"
}

// EditSilently changes a resource without a new version, as services do for
// sub-resources that do not version the parent.
func (r *Remote) EditSilently(id string, f func(root *xmltree.Node)) { f(r.recs[id].root) }

func (r *Remote) Delete(id string)     { delete(r.recs, id) }
func (r *Remote) Move(id, path string) { r.recs[id].path = path; r.recs[id].version++ }

// PutAttachment adds an attachment to resource resID at version 1.
func (r *Remote) PutAttachment(resID, attID, name string, data []byte) {
	rec := r.recs[resID]
	rec.atts = append(rec.atts, &attachment{id: attID, name: name, created: "2026-01-01T00:00:00Z", data: data, version: 1})
}

// EditAttachment replaces an attachment's bytes on the remote and bumps its version.
func (r *Remote) EditAttachment(resID, attID string, data []byte) {
	a := findAtt(r.recs[resID], attID)
	a.data, a.version = data, a.version+1
}

func (r *Remote) RenameAttachment(resID, attID, name string) {
	findAtt(r.recs[resID], attID).name = name
}

func (r *Remote) DeleteAttachment(resID, attID string) {
	rec := r.recs[resID]
	rec.atts = slices.DeleteFunc(rec.atts, func(a *attachment) bool { return a.id == attID })
}

// Attachment returns an attachment's bytes and version.
func (r *Remote) Attachment(resID, attID string) ([]byte, int, bool) {
	a := findAtt(r.recs[resID], attID)
	if a == nil {
		return nil, 0, false
	}
	return a.data, a.version, true
}

// AttachmentNamed returns the id of resID's attachment called name.
func (r *Remote) AttachmentNamed(resID, name string) (string, bool) {
	if rec := r.recs[resID]; rec != nil {
		for _, a := range rec.atts {
			if a.name == name {
				return a.id, true
			}
		}
	}
	return "", false
}

func findAtt(rec *record, id string) *attachment {
	if rec == nil {
		return nil
	}
	for _, a := range rec.atts {
		if a.id == id {
			return a
		}
	}
	return nil
}

func (r *Remote) Get(id string) (*adapter.Resource, bool) {
	rec, ok := r.recs[id]
	if !ok {
		return nil, false
	}
	return r.resource(id, rec), true
}

func (r *Remote) resource(id string, rec *record) *adapter.Resource {
	root := rec.root.Clone()
	root.SetAttr("id", id)
	root.SetAttr("version", strconv.Itoa(rec.version))
	for _, a := range rec.atts {
		root.Children = append(root.Children, &xmltree.Node{Kind: xmltree.Element, Name: "attachment", Attrs: []xmltree.Attr{
			{Name: "id", Value: a.id}, {Name: "name", Value: a.name}, {Name: "size", Value: strconv.Itoa(len(a.data))},
			{Name: "version", Value: strconv.Itoa(a.version)}, {Name: "created", Value: a.created}}})
	}
	res := &adapter.Resource{ID: id, Version: strconv.Itoa(rec.version), Path: rec.path,
		By: rec.by, At: "2026-09-23T12:00:00Z", Root: root}
	if r.WithFiles {
		res.Files = map[string][]byte{}
		for _, a := range rec.atts {
			res.Files[a.id] = a.data
		}
	}
	return res
}

type Adapter struct {
	Remote *Remote
	Deps   func(local *adapter.Resource) []string // DependsOn; nil: none
	sch    *schema.Schema
}

func (a *Adapter) DependsOn(local *adapter.Resource) []string {
	if a.Deps == nil {
		return nil
	}
	return a.Deps(local)
}

func New() *Adapter { return &Adapter{Remote: &Remote{recs: map[string]*record{}}, sch: Schema} }

// ReadOnlyAttachments makes this adapter's attachment kind allow no operation.
func (a *Adapter) ReadOnlyAttachments() {
	s := *Schema
	s.Elems = slices.Clone(Schema.Elems)
	for i := range s.Elems {
		if s.Elems[i].Kind == schema.Attachment {
			s.Elems[i].Ops = nil
		}
	}
	a.sch = &s
}

// Schema is the adapter's schema: the package Schema unless ReadOnlyAttachments changed it.
func (a *Adapter) Schema() *schema.Schema {
	if a.sch == nil {
		return Schema
	}
	return a.sch
}

func (*Adapter) Name() string                 { return "fake" }
func (*Adapter) Schemes() []string            { return []string{"fake"} }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Tree }
func (*Adapter) DefaultDir(*url.URL) string   { return "fake" }
func (*Adapter) Verbs() []adapter.Verb {
	return []adapter.Verb{{Name: "publish", Class: "publish", Help: "publish the note to a channel", Params: []string{"channel"}}}
}

func (ad *Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	defer func() { a.Warning = ad.Remote.Warn[a.Detail] }()
	a.Class = a.Verb
	a.Preview = ad.Remote.Preview[a.Verb]
	switch {
	case a.Target != "":
		a.Detail = a.Verb + " " + a.Target
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	case a.Verb == "move":
		a.Detail = "move"
	default:
		a.Detail = a.Verb + " note"
	}
}

// DescribeBase names what a sub delete removes, from the remote's copy.
func (ad *Adapter) DescribeBase(a *adapter.Action, local, base *adapter.Resource) {
	ad.Describe(a, local)
	if m := targetRe.FindStringSubmatch(a.Target); a.Verb == "delete" && m != nil && base != nil {
		name, id := m[1], m[2]
		for _, c := range base.Root.ChildrenNamed(name) {
			if v, _ := c.Attr("id"); v == id {
				a.Detail += " (" + c.TextContent() + ")"
			}
		}
		a.Warning = ad.Remote.Warn[a.Verb+" "+a.Target]
	}
}

func (a *Adapter) Open(context.Context, *url.URL, map[string]string) (adapter.Session, error) {
	return session{a.Remote}, nil
}

type session struct{ r *Remote }

func (s session) Close() error { return nil }

func (s session) List(_ context.Context, cursor string) (adapter.Listing, error) {
	s.r.Cursors = append(s.r.Cursors, cursor)
	if err := s.r.FailVerb["list"]; err != nil {
		return adapter.Listing{}, err
	}
	l := adapter.Listing{Full: !s.r.Partial, FullDirs: s.r.FullDirs, Cursor: "c1"}
	for id, rec := range s.r.recs {
		res := *s.r.resource(id, rec)
		if s.r.Stubs {
			res.Root = nil
		}
		l.Resources = append(l.Resources, res)
	}
	return l, nil
}

func (s session) Fetch(_ context.Context, id string) (*adapter.Resource, error) {
	if s.r.fetches++; s.r.FetchLimit > 0 && s.r.fetches > s.r.FetchLimit {
		return nil, errors.New("fetch failed")
	}
	res, ok := s.r.Get(id)
	if !ok || s.r.Missing[id] {
		return nil, adapter.ErrNotFound
	}
	return res, nil
}

func (s session) Download(_ context.Context, resID, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	if err := s.r.FailVerb["download"]; err != nil {
		return adapter.AttachmentInfo{}, err
	}
	a := findAtt(s.r.recs[resID], attID)
	if a == nil {
		return adapter.AttachmentInfo{}, adapter.ErrNotFound
	}
	n, err := w.Write(a.data)
	if err == nil {
		s.r.Downloads++
	}
	return adapter.AttachmentInfo{Version: strconv.Itoa(a.version), Size: int64(n)}, err
}

func (s session) Check(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	s.r.Checks++
	var out []adapter.Result
	for _, a := range req.Actions {
		k := a.Verb + " " + a.Group
		res := adapter.Result{Action: a, Err: s.fault(a), Detail: s.r.CheckDetail[k], Class: s.r.CheckClass[k], Warning: s.r.CheckWarn[k]}
		if a.Verb == "create" && a.Target == "" && res.Err == nil {
			if pid, err := s.parentID(req); err != nil {
				res.Err = err
			} else if pid == adapter.Pending {
				res.Detail = "under a new parent"
			}
		}
		out = append(out, res)
	}
	return out
}

// parentID resolves the parent Remote.Parent names for a new file ("" none).
func (s session) parentID(req adapter.ApplyRequest) (string, error) {
	if s.r.Parent == nil {
		return "", nil
	}
	pp := s.r.Parent(req.Local.Path)
	if pp == "" {
		return "", nil
	}
	if id, ok := req.IDByPath(pp); ok {
		return id, nil
	}
	return "", fmt.Errorf("parent %s not found", pp)
}

func (s session) fault(a adapter.Action) error {
	if err := s.r.FailVerb[a.Verb+" "+a.Target]; err != nil {
		return err
	}
	return s.r.FailVerb[a.Verb]
}

var targetRe = regexp.MustCompile(`^(\w+)\[(?:id=([^\]]+)|(\d+))\]$`)

func (s session) Apply(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var id string
	var rec *record
	if req.Base != nil {
		id = req.Base.ID
		rec = s.r.recs[id]
		if rec == nil {
			return []adapter.Result{{Action: req.Actions[0], Err: adapter.ErrNotFound, Code: "404"}}
		}
		if s.r.LockFailures > 0 || (req.Lock != "" && req.Lock != strconv.Itoa(rec.version)) {
			if s.r.LockFailures > 0 {
				s.r.LockFailures--
			}
			return []adapter.Result{{Action: req.Actions[0], Err: fmt.Errorf("fake: %w", adapter.ErrLock), Code: "409"}}
		}
	}
	var out []adapter.Result
	changed := false
	for _, a := range req.Actions {
		res := adapter.Result{Action: a}
		if err := s.fault(a); err != nil {
			res.Err, res.Code = err, "500"
			out = append(out, res)
			if s.r.VanishOnFail && a.Verb == "update" && a.Target != "" && rec != nil {
				out[len(out)-1].Partial = true
				m := targetRe.FindStringSubmatch(a.Target)
				name, sid := m[1], m[2]
				rec.root.Children = slices.DeleteFunc(rec.root.Children, func(c *xmltree.Node) bool {
					v, _ := c.Attr("id")
					return c.Kind == xmltree.Element && c.Name == name && v == sid
				})
				rec.version++
			}
			continue
		}
		if s.r.NeedSecret && a.Verb == "create" && strings.HasPrefix(a.Target, "comment[") {
			if req.Secret == nil {
				res.Err = errors.New("needs a password: run in a terminal")
				out = append(out, res)
				continue
			}
			pw, err := req.Secret("Password: ")
			if err != nil {
				res.Err = err
				out = append(out, res)
				continue
			}
			s.r.Secrets = append(s.r.Secrets, pw)
		}
		if a.IsAttachment() {
			s.r.Calls = append(s.r.Calls, a.Verb+" "+a.File)
			out = append(out, s.applyAttachment(rec, req, a))
			continue // attachment changes do not bump the resource version
		}
		s.r.Calls = append(s.r.Calls, a.Verb+" "+req.Local.Path)
		switch {
		case a.Verb == "create" && a.Target == "":
			if err := s.r.FailCreate[req.Local.Path]; err != nil {
				res.Err, res.Code = err, "500"
				out = append(out, res)
				continue
			}
			pid, err := s.parentID(req)
			if err != nil {
				res.Err = err
				out = append(out, res)
				continue
			}
			id = s.r.next()
			root := req.Local.Root.Clone()
			for _, c := range root.ChildrenNamed("comment") {
				c.SetAttr("id", "c"+s.r.next())
				c.SetAttr("created", "2026-09-23T12:00:00Z")
			}
			root.DelAttr("id")
			root.DelAttr("version")
			root.Children = slices.DeleteFunc(root.Children, func(c *xmltree.Node) bool {
				return c.Kind == xmltree.Element && c.Name == "attachment"
			})
			p := req.Local.Path
			if np, ok := s.r.CreatePath[p]; ok {
				p = np
			}
			rec = &record{path: p, root: root, version: 0, by: "me"}
			s.r.recs[id] = rec
			res.ID = id
			if pid != "" {
				if s.r.Parents == nil {
					s.r.Parents = map[string]string{}
				}
				s.r.Parents[id] = pid
			}
		case a.Verb == "delete" && a.Target == "":
			delete(s.r.recs, id)
			out = append(out, res)
			return out
		case a.Verb == "move":
			rec.path = a.To
		case a.Verb == "update" && a.Target == "":
			replaceGroup(rec.root, req.Local.Root, a.Group)
		case a.Target != "":
			s.applySub(rec.root, req.Local.Root, a)
		case a.Verb == "publish":
			s.r.Published = append(s.r.Published, req.Local.Path+" "+a.Params["channel"])
		}
		changed = true
		out = append(out, res)
	}
	if changed && rec != nil {
		rec.version++
		rec.by = "me"
	}
	return out
}

func (s session) applyAttachment(rec *record, req adapter.ApplyRequest, a adapter.Action) adapter.Result {
	res := adapter.Result{Action: a}
	if rec == nil {
		res.Err, res.Code = adapter.ErrNotFound, "404"
		return res
	}
	read := func() ([]byte, error) {
		rc, err := req.Open(a.File)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	id, _, _ := adapter.ParseAttachmentTarget(a.Target)
	switch a.Verb {
	case "create":
		data, err := read()
		if err != nil {
			res.Err = err
			return res
		}
		n := &attachment{id: "a" + s.r.next(), name: path.Base(a.File), created: "2026-09-23T12:00:00Z", data: data, version: 1}
		rec.atts = append(rec.atts, n)
		res.ID, res.Version = n.id, "1"
	case "update":
		x := findAtt(rec, id)
		if x == nil {
			res.Err, res.Code = adapter.ErrNotFound, "404"
			return res
		}
		data, err := read()
		if err != nil {
			res.Err = err
			return res
		}
		x.data, x.version = data, x.version+1
		res.ID, res.Version = x.id, strconv.Itoa(x.version)
	case "delete":
		if findAtt(rec, id) == nil {
			res.Err, res.Code = adapter.ErrNotFound, "404"
			return res
		}
		rec.atts = slices.DeleteFunc(rec.atts, func(x *attachment) bool { return x.id == id })
	default:
		res.Err = fmt.Errorf("fake: no attachment action %q", a.Verb)
	}
	return res
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}

func (s session) applySub(dst, src *xmltree.Node, a adapter.Action) {
	m := targetRe.FindStringSubmatch(a.Target)
	if m == nil {
		return
	}
	name, id, nth := m[1], m[2], m[3]
	switch a.Verb {
	case "create":
		n, _ := strconv.Atoi(nth)
		i := 0
		for _, c := range src.ChildrenNamed(name) {
			if _, has := c.Attr("id"); has {
				continue
			}
			if i++; i == n {
				nc := c.Clone()
				nc.SetAttr("id", "c"+s.r.next())
				nc.SetAttr("created", "2026-09-23T12:00:00Z")
				dst.Children = append(dst.Children, nc)
			}
		}
	case "update":
		if old, nw := validate.FindSub(dst, name, "id", id), validate.FindSub(src, name, "id", id); old != nil && nw != nil {
			*old = *nw.Clone()
		}
	case "delete":
		old := validate.FindSub(dst, name, "id", id)
		var kept []*xmltree.Node
		for _, c := range dst.Children {
			if c != old {
				kept = append(kept, c)
			}
		}
		dst.Children = kept
	}
}

func (s session) UseCache(dir string) { s.r.CacheDir = dir }

func (s session) Available(_ context.Context, id string, _ *adapter.Resource) (adapter.Advice, error) {
	return s.r.Advice[id], nil
}

// Placed is the fake adapter with attachments outside X.files/ (ksef spec
// §2.1): resources live under _m/, and attachment name of _m/<dir>/<stem>.xml
// is <dir>/<stem>.<name>.
type Placed struct{ *Adapter }

func NewPlaced() *Placed { return &Placed{New()} }

func (*Placed) ResourceDir() string { return "_m" }

func (*Placed) AttachmentPath(resPath, name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(resPath, "_m/"), ".xml") + "." + name
}

func (*Placed) ResourceOf(p string) (string, bool) {
	if strings.HasPrefix(p, "_m/") {
		return "", false
	}
	dir, file := path.Split(p)
	stem, _, ok := strings.Cut(file, ".")
	if !ok {
		return "", false
	}
	return "_m/" + dir + stem + ".xml", true
}

// Frozen is Placed with read-only resources (ksef spec §8).
type Frozen struct{ *Placed }

func NewFrozen() *Frozen { return &Frozen{NewPlaced()} }

func (*Frozen) ReadOnly() string { return "notes are read-only" }
