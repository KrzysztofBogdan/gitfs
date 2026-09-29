package engine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type PullOpts struct {
	Force  bool
	Full   bool // list everything, ignoring the cursor
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
		case c.ID != "" && !c.Quiet:
			localByID[c.ID] = c
		}
	}
	cursor := e.Index.Cursor
	if o.Full {
		cursor = "" // label-only changes, deleted comments and attachments show up only this way
	}
	e.report(adapter.Progress{Phase: "list"})
	l, err := e.Session.List(ctx, cursor)
	if err != nil {
		e.report(adapter.Progress{Phase: "done"})
		return r, err
	}
	fetching := true
	defer func() {
		if fetching {
			e.report(adapter.Progress{Phase: "done"})
		}
	}()
	vanished := map[string]bool{} // listed, then not found: deleted on the remote meanwhile
	printed := false
	say := func(format string, a ...any) {
		printed = true
		fmt.Fprintf(e.Out, format+"\n", a...)
	}
	// A skipped page the listing sent in full (changed without a new version)
	// would come back as a stub next time: keep the old cursor so the next
	// listing flags it again. A path-limited pull skips pages it never looks at.
	keepCursor := o.Filter != nil
	listed := map[string]bool{}
	for i, item := range l.Resources {
		e.report(adapter.Progress{Phase: "fetch", Done: i, Total: len(l.Resources), Item: item.Path})
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
			if errors.Is(err, adapter.ErrNotFound) {
				continue
			}
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
		if item.Root == nil && item.Version == entry.Version && item.Path == entry.Path && !o.Full {
			continue
		}
		res, err := e.Resolved(ctx, item)
		if errors.Is(err, adapter.ErrNotFound) {
			vanished[item.ID] = true
			continue
		}
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
		case deletedLocally[item.ID] && o.Force:
			if err := e.Store(res, entry.Path); err != nil {
				return r, err
			}
			r.Updated++
			say("  ~  %s   (forced, restored)", res.Path)
		case deletedLocally[item.ID]:
			keepCursor = keepCursor || item.Root != nil
			r.Conflicts++
			say("  C  %s   deleted locally, changed on remote; gfs resolve --ours keeps the deletion, --theirs restores it", entry.Path)
		case changed && local.Status == 'C':
			if !o.Force {
				keepCursor = keepCursor || item.Root != nil
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
	fetching = false
	e.report(adapter.Progress{Phase: "done"})
	gone := vanished
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
	if err := e.pullAttachments(ctx, o, say, &r); err != nil {
		return r, err
	}
	if !keepCursor {
		e.Index.Cursor = l.Cursor
	}
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

// pullAttachments refreshes fetched attachments against the updated bases
// (attachments spec 4.5). Attachments never fetched are never downloaded.
func (e *Env) pullAttachments(ctx context.Context, o PullOpts, say func(string, ...any), r *PullReport) error {
	el := e.attachmentElem()
	if el == nil {
		return nil
	}
	roots := map[string]*xmltree.Node{}
	rootOf := func(en workdir.Entry) (*xmltree.Node, error) {
		if root, ok := roots[en.ID]; ok {
			return root, nil
		}
		root, err := e.baseRoot(en)
		if err != nil {
			return nil, err
		}
		roots[en.ID] = root
		return root, nil
	}
	selected := func(l workdir.AttEntry) bool {
		if o.Filter == nil || o.Filter(l.Path) {
			return true
		}
		res, _ := attach.ResourceOf(l.Path)
		return o.Filter(res)
	}
	for _, l := range e.Atts.All() {
		if !selected(l) || !e.Tree.Exists(l.Path) {
			continue // evicted, or moved with its sidecar: placeAttachments below
		}
		var rel *xmltree.Node
		if en, ok := e.Index.ByID(l.ResID); ok {
			root, err := rootOf(en)
			if err != nil {
				return err
			}
			rel = attach.Elements(root, el)[l.AttID]
		}
		changed, err := attach.Changed(e.Tree, l.Path, l)
		if err != nil {
			return err
		}
		if rel == nil {
			if changed && !o.Force {
				r.Conflicts++
				say("  C  %s   deleted on remote; kept because it changed locally", l.Path)
				continue
			}
			if err := e.forgetAttachment(l.ResID, l.AttID, l.Path); err != nil {
				return err
			}
			e.removeConflictCopies(l.Path)
			r.Deleted++
			say("  -  %s   (deleted on remote)", l.Path)
			continue
		}
		v := attach.Version(rel, el)
		if v == l.Version || v == "-" {
			continue
		}
		if changed && !o.Force {
			cp := attach.ConflictCopy(l.Path, v)
			if !e.Tree.Exists(cp) {
				if _, err := e.download(ctx, l.ResID, l.AttID, cp); err != nil {
					return err
				}
			}
			r.Conflicts++
			say("  C  %s   changed locally and on remote v%s; remote copy: %s", l.Path, v, path.Base(cp))
			continue
		}
		line, err := e.download(ctx, l.ResID, l.AttID, l.Path)
		if err != nil {
			return err
		}
		e.Atts.Put(line)
		e.removeConflictCopies(l.Path)
		r.Updated++
		say("  ~  %s", l.Path)
	}
	ids := map[string]bool{}
	for _, l := range e.Atts.All() {
		ids[l.ResID] = true
	}
	var order []string
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		en, ok := e.Index.ByID(id)
		if !ok {
			continue
		}
		root, err := rootOf(en)
		if err != nil {
			return err
		}
		moves, err := e.placeAttachments(id, en.Path, root)
		if err != nil {
			return err
		}
		for _, m := range moves {
			r.Moved++
			say("  ~  %s -> %s", m[0], m[1])
		}
	}
	return e.saveAtts()
}
