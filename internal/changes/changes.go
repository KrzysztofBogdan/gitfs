// Package changes computes gfs status: what changed and which remote actions follow.
package changes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type FileChange struct {
	Status        byte
	Path, OldPath string
	ID            string
	Entry         workdir.Entry
	Local, Base   *envelope.Doc
	Actions       []adapter.Action // resource actions, then attachment actions
	Note          string
	Err           error
	Attachments   []AttChange // attachment status of this resource (attachments spec 3.4)
	Quiet         bool        // the resource file itself is unchanged; only attachments changed
	Warnings      []string    // adapter lint findings new in this version of the file
}

func Compute(t *workdir.Tree, ix *workdir.Index, ad adapter.Adapter, filter func(string) bool) ([]FileChange, error) {
	s := ad.Schema()
	files, err := t.Scan()
	if err != nil {
		return nil, err
	}
	atts, err := t.LoadAttachments()
	if err != nil {
		return nil, err
	}
	ac := &attCtx{t: t, atts: atts, ad: ad, e: s.Attachment(), claimed: map[string]bool{}}
	var out []FileChange
	seen := map[string]string{} // id -> path
	for _, p := range files {
		data, err := t.ReadFile(p)
		if err != nil {
			return nil, err
		}
		fc := FileChange{Path: p}
		byPath, inIndex := ix.ByPath(p)
		if envelope.HasMarkers(data) || envelope.HasConflictElement(data) {
			ac.claim(p, byPath.ID)
			fc.Status, fc.ID, fc.Entry = 'C', byPath.ID, byPath
			if inIndex {
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			ac.claim(p, byPath.ID)
			fc.Status, fc.Err = 'A', err
			if inIndex {
				fc.Status, fc.ID, fc.Entry = 'M', byPath.ID, byPath
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		fc.Local = doc
		id, _ := doc.Content.Attr(s.ID)
		entry, known := ix.ByID(id)
		if id != "" && known {
			if other, dup := seen[id]; dup {
				ac.claim(p, "")
				fc.Status, fc.Err = 'A', fmt.Errorf("duplicate identity %s=%q, also in %s", s.ID, id, other)
				out = append(out, fc)
				continue
			}
			seen[id] = p
			fc.ID, fc.Entry = id, entry
			bdata, err := t.ReadBase(entry.Path)
			if err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
			if fc.Base, err = envelope.Parse(bdata); err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
		}
		moved := fc.Base != nil && entry.Path != p
		var baseContent *xmltree.Node
		basePath := ""
		if fc.Base != nil {
			baseContent, basePath = fc.Base.Content, entry.Path
		}
		if fc.Attachments, err = ac.changes(fc.ID, p, doc.Content, baseContent, doc.Action); err != nil {
			return nil, err
		}
		if fc.Base != nil && !moved && doc.Action == "" && len(doc.Errors) == 0 &&
			CanonContent(doc.Content, s) == CanonContent(fc.Base.Content, s) {
			if len(fc.Attachments) == 0 {
				continue // unchanged
			}
			fc.Status, fc.Quiet, fc.Actions = 'M', true, attachmentActions(fc.Attachments)
			out = append(out, fc)
			continue
		}
		switch {
		case len(doc.Errors) > 0:
			fc.Status = '!'
		case fc.Base == nil:
			fc.Status = 'A'
		case moved:
			fc.Status = 'R'
		default:
			fc.Status = 'M'
		}
		if moved {
			fc.OldPath = entry.Path
			if !adapter.IsMove(ad.PathModel(), entry.Path, p) {
				fc.Note = "rename ignored: name is derived"
			}
		}
		fc.Err = validate.Resource(doc.Content, baseContent, s)
		fc.Warnings = newWarnings(ad, doc.Content, baseContent)
		fc.Actions = append(ResolveActions(ad, baseContent, doc, basePath, p), attachmentActions(fc.Attachments)...)
		out = append(out, fc)
	}
	for _, e := range ix.All() {
		if _, ok := seen[e.ID]; ok {
			continue
		}
		fc := FileChange{Status: 'D', Path: e.Path, ID: e.ID, Entry: e, Actions: []adapter.Action{{Verb: "delete"}}}
		if bdata, err := t.ReadBase(e.Path); err == nil {
			fc.Base, _ = envelope.Parse(bdata)
		}
		ad.Describe(&fc.Actions[0], nil)
		out = append(out, fc)
	}
	orphans, err := ac.orphans(seen)
	if err != nil {
		return nil, err
	}
	out = append(out, orphans...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if filter == nil {
		return out, nil
	}
	var kept []FileChange
	for _, c := range out {
		if filter(c.Path) || (c.OldPath != "" && filter(c.OldPath)) {
			kept = append(kept, c)
			continue
		}
		if c.Status != 'M' {
			continue // attachments alone can be selected only from a modified resource
		}
		var acs []AttChange
		for _, a := range c.Attachments {
			if filter(a.Path) {
				acs = append(acs, a)
			}
		}
		if len(acs) == 0 {
			continue
		}
		c.Attachments, c.Quiet, c.Actions = acs, true, attachmentActions(acs)
		kept = append(kept, c)
	}
	return kept, nil
}

func PathFilter(t *workdir.Tree, args []string) (func(string) bool, error) {
	if len(args) == 0 {
		return nil, nil
	}
	var rels []string
	for _, a := range args {
		r, err := t.Rel(a)
		if err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return func(p string) bool {
		for _, r := range rels {
			if r == "" || p == r || strings.HasPrefix(p, r+"/") {
				return true
			}
		}
		return false
	}, nil
}

// newWarnings lints the local content and keeps what the base did not already have.
func newWarnings(ad adapter.Adapter, local, base *xmltree.Node) []string {
	l, ok := ad.(adapter.Linter)
	if !ok || local == nil {
		return nil
	}
	old := map[string]bool{}
	if base != nil {
		for _, w := range l.Lint(base) {
			old[w] = true
		}
	}
	var out []string
	for _, w := range l.Lint(local) {
		if !old[w] {
			out = append(out, w)
		}
	}
	return out
}
