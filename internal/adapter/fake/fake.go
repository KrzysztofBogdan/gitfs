// Package fake is an in-memory adapter for engine tests.
package fake

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

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
	},
}

type record struct {
	path    string
	root    *xmltree.Node
	version int
	by      string
}

type Remote struct {
	FailVerb     map[string]error // keyed by verb, or verb+" "+target
	LockFailures int              // Apply returns ErrLock this many times
	Calls        []string         // "verb path" per executed action
	Published    []string         // "path channel"
	recs         map[string]*record
	seq          int
}

func (r *Remote) next() string { r.seq++; return strconv.Itoa(r.seq) }

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

func (r *Remote) Delete(id string)     { delete(r.recs, id) }
func (r *Remote) Move(id, path string) { r.recs[id].path = path; r.recs[id].version++ }

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
	return &adapter.Resource{ID: id, Version: strconv.Itoa(rec.version), Path: rec.path,
		By: rec.by, At: "2026-09-23T12:00:00Z", Root: root}
}

type Adapter struct{ Remote *Remote }

func New() *Adapter { return &Adapter{Remote: &Remote{recs: map[string]*record{}}} }

func (*Adapter) Name() string                 { return "fake" }
func (*Adapter) Schemes() []string            { return []string{"fake"} }
func (*Adapter) Schema() *schema.Schema       { return Schema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Tree }
func (*Adapter) DefaultDir(*url.URL) string   { return "fake" }
func (*Adapter) Verbs() []adapter.Verb {
	return []adapter.Verb{{Name: "publish", Class: "publish", Help: "publish the note to a channel", Params: []string{"channel"}}}
}

func (*Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	a.Class = a.Verb
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

func (a *Adapter) Open(context.Context, *url.URL, map[string]string) (adapter.Session, error) {
	return session{a.Remote}, nil
}

type session struct{ r *Remote }

func (s session) Close() error { return nil }

func (s session) List(context.Context, string) (adapter.Listing, error) {
	l := adapter.Listing{Full: true}
	for id, rec := range s.r.recs {
		l.Resources = append(l.Resources, *s.r.resource(id, rec))
	}
	return l, nil
}

func (s session) Fetch(_ context.Context, id string) (*adapter.Resource, error) {
	res, ok := s.r.Get(id)
	if !ok {
		return nil, adapter.ErrNotFound
	}
	return res, nil
}

func (s session) Check(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var out []adapter.Result
	for _, a := range req.Actions {
		out = append(out, adapter.Result{Action: a, Err: s.fault(a)})
	}
	return out
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
			continue
		}
		s.r.Calls = append(s.r.Calls, a.Verb+" "+req.Local.Path)
		switch {
		case a.Verb == "create" && a.Target == "":
			id = s.r.next()
			root := req.Local.Root.Clone()
			for _, c := range root.ChildrenNamed("comment") {
				c.SetAttr("id", "c"+s.r.next())
				c.SetAttr("created", "2026-09-23T12:00:00Z")
			}
			root.DelAttr("id")
			root.DelAttr("version")
			rec = &record{path: req.Local.Path, root: root, version: 0, by: "me"}
			s.r.recs[id] = rec
			res.ID = id
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
