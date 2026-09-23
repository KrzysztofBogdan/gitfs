package engine

import (
	"context"
	"fmt"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type PullOpts struct {
	Force  bool
	Filter func(string) bool
}

type PullReport struct{ Added, Updated, Merged, Moved, Deleted, Conflicts int }

func (r PullReport) ExitCode() int {
	if r.Conflicts > 0 {
		return 1
	}
	return 0
}

func Pull(ctx context.Context, e *Env, o PullOpts) (PullReport, error) {
	var r PullReport
	s := e.Adapter.Schema()
	cs, err := changes.Compute(e.Tree, e.Index, e.Adapter, nil)
	if err != nil {
		return r, err
	}
	localByID := map[string]changes.FileChange{}
	deletedLocally := map[string]bool{}
	for _, c := range cs {
		switch {
		case c.Status == 'D':
			deletedLocally[c.ID] = true
		case c.ID != "":
			localByID[c.ID] = c
		}
	}
	l, err := e.Session.List(ctx, e.Index.Cursor)
	if err != nil {
		return r, err
	}
	printed := false
	say := func(format string, a ...any) {
		printed = true
		fmt.Fprintf(e.Out, format+"\n", a...)
	}
	listed := map[string]bool{}
	for _, item := range l.Resources {
		listed[item.ID] = true
		entry, known := e.Index.ByID(item.ID)
		if o.Filter != nil && !o.Filter(item.Path) && !(known && o.Filter(entry.Path)) {
			continue
		}
		if !known {
			if e.Tree.Exists(item.Path) {
				say("  !  %s   exists locally and is not tracked; skipped", item.Path)
				continue
			}
			res, err := e.Resolved(ctx, item)
			if err != nil {
				return r, err
			}
			if err := e.Store(res, ""); err != nil {
				return r, err
			}
			r.Added++
			say("  +  %s", res.Path)
			continue
		}
		if item.Root == nil && item.Version == entry.Version && item.Path == entry.Path {
			continue
		}
		res, err := e.Resolved(ctx, item)
		if err != nil {
			return r, err
		}
		bdata, err := e.Tree.ReadBase(entry.Path)
		if err != nil {
			return r, err
		}
		base, err := envelope.Parse(bdata)
		if err != nil {
			return r, fmt.Errorf("base %s: %w", entry.Path, err)
		}
		if res.Path == entry.Path && changes.CanonContent(res.Root, s) == changes.CanonContent(base.Content, s) {
			if res.Version != entry.Version {
				entry.Version = res.Version
				e.Index.Put(entry)
			}
			continue
		}
		local, changed := localByID[item.ID]
		switch {
		case deletedLocally[item.ID]:
			r.Conflicts++
			say("  C  %s   deleted locally, changed on remote", entry.Path)
		case changed && local.Status == 'C':
			if !o.Force {
				r.Conflicts++
				say("  C  %s   unresolved conflict; resolve first", local.Path)
				continue
			}
			if err := e.Store(res, local.Path); err != nil {
				return r, err
			}
			r.Updated++
			say("  ~  %s   (forced)", res.Path)
		case !changed:
			if err := e.Store(res, entry.Path); err != nil {
				return r, err
			}
			if res.Path != entry.Path {
				r.Moved++
				say("  ~  %s -> %s   (moved on remote)", entry.Path, res.Path)
			} else {
				r.Updated++
				say("  ~  %s", res.Path)
			}
		default:
			if local.Local == nil { // unparseable local file: cannot merge
				r.Conflicts++
				say("  C  %s   local file does not parse: %v", local.Path, local.Err)
				continue
			}
			target := res.Path
			if local.Status == 'R' {
				target = local.Path
			}
			m, err := merge.Merge(base.Content, local.Local.Content, res.Root, s, "remote v"+res.Version)
			if err != nil {
				r.Conflicts++
				say("  C  %s   merge failed: %v", local.Path, err)
				continue
			}
			tb := *res
			tb.Path = target
			if m.Conflicted() {
				doc := &envelope.Doc{Action: local.Local.Action, Params: local.Local.Params, Conflict: &envelope.Conflict{
					RemoteVersion: res.Version, By: res.By, At: res.At, Elements: m.Elements, Hunks: m.Hunks}}
				if err := e.Tree.WriteFile(target, []byte(envelope.Header(doc)+m.Text+"\n"+envelope.Footer)); err != nil {
					return r, err
				}
				r.Conflicts++
				say("  C  %s   conflict with remote v%s", target, res.Version)
			} else {
				doc := *local.Local
				doc.Content = m.Root
				if err := e.Tree.WriteFile(target, envelope.Bytes(&doc, s)); err != nil {
					return r, err
				}
				r.Merged++
				say("  ~  %s   merged", target)
			}
			if target != local.Path && e.Tree.Exists(local.Path) {
				if err := e.Tree.Remove(local.Path); err != nil {
					return r, err
				}
			}
			if err := e.StoreBase(&tb, entry.Path); err != nil {
				return r, err
			}
		}
	}
	gone := map[string]bool{}
	for _, id := range l.Deleted {
		gone[id] = true
	}
	if l.Full {
		for _, en := range e.Index.All() {
			if !listed[en.ID] {
				gone[en.ID] = true
			}
		}
	}
	for id := range gone {
		en, ok := e.Index.ByID(id)
		if !ok || (o.Filter != nil && !o.Filter(en.Path)) {
			continue
		}
		local, changed := localByID[id]
		if !changed || local.Local == nil {
			if err := e.Forget(id, en.Path); err != nil {
				return r, err
			}
			r.Deleted++
			say("  -  %s   (deleted on remote)", en.Path)
			continue
		}
		doc := *local.Local
		doc.Content = local.Local.Content.Clone()
		StripReadOnly(doc.Content, s)
		doc.Conflict = &envelope.Conflict{RemoteVersion: "deleted"}
		if err := e.Tree.WriteFile(local.Path, envelope.Bytes(&doc, s)); err != nil {
			return r, err
		}
		if err := e.Tree.RemoveBase(en.Path); err != nil {
			return r, err
		}
		e.Index.Delete(id)
		r.Conflicts++
		say("  C  %s   deleted on remote; remove <conflict/> and commit to re-create, or delete the file", local.Path)
	}
	e.Index.Cursor = l.Cursor
	if err := e.Tree.SaveIndex(e.Index); err != nil {
		return r, err
	}
	if !printed {
		fmt.Fprintln(e.Out, "Already up to date.")
	}
	return r, nil
}

// StripReadOnly removes service-owned attributes so the content can be re-created.
func StripReadOnly(root *xmltree.Node, s *schema.Schema) {
	for _, a := range s.RootAttrs {
		if a.ReadOnly {
			root.DelAttr(a.Name)
		}
	}
	stripSubs(root, s.Elems)
}

func stripSubs(n *xmltree.Node, elems []schema.Elem) {
	var kept []*xmltree.Node
	for _, c := range n.Children {
		if c.Kind == xmltree.Element {
			if e := schema.Find(elems, c.Name); e != nil && e.Kind == schema.Attachment {
				continue // attachments cannot be re-created from metadata
			}
		}
		kept = append(kept, c)
	}
	n.Children = kept
	for _, c := range n.Elements() {
		e := schema.Find(elems, c.Name)
		if e == nil || e.Kind != schema.Sub {
			continue
		}
		for _, a := range e.Attrs {
			if a.ReadOnly || a.Name == e.ID {
				c.DelAttr(a.Name)
			}
		}
		stripSubs(c, e.Children)
	}
}
