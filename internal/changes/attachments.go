package changes

import (
	"fmt"
	"path"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// AttChange is the status of one attachment of a resource (attachments spec 3.4).
type AttChange struct {
	Status byte            // 'A', 'M', 'D', 'C' or '!'
	Path   string          // sidecar file path (the derived path when not fetched)
	AttID  string          // "" for a new file
	Action *adapter.Action // what commit sends; nil for C and !
	Note   string          // reason for C and !, or how an explicit verb uses the file
}

type attCtx struct {
	t       *workdir.Tree
	atts    *workdir.Attachments
	ad      adapter.Adapter
	e       *schema.Elem    // nil: the adapter has no attachments
	claimed map[string]bool // sidecar dirs accounted for by some resource
}

func (c *attCtx) claim(resPath, resID string) {
	c.claimed[attach.SidecarDir(resPath)] = true
	if resID != "" {
		for _, l := range c.atts.ForResource(resID) {
			c.claimed[path.Dir(l.Path)] = true
		}
	}
}

// changes computes the attachment changes of the resource at resPath.
// resID is "" for a new resource; explicit is the envelope action, if any.
func (c *attCtx) changes(resID, resPath string, local, base *xmltree.Node, explicit string) ([]AttChange, error) {
	if c.e == nil {
		return nil, nil
	}
	c.claim(resPath, resID)
	dir := attach.SidecarDir(resPath)
	files, dirs, err := c.t.ListSidecar(dir)
	if err != nil {
		return nil, err
	}
	var out []AttChange
	for _, d := range dirs {
		out = append(out, AttChange{Status: '!', Path: d, Note: "subdirectories are not supported in " + dir + "/"})
	}
	localEls, baseEls := attach.Elements(local, c.e), attach.Elements(base, c.e)
	var lines []workdir.AttEntry
	if resID != "" {
		lines = c.atts.ForResource(resID)
	}
	taken := map[string]bool{} // sidecar files explained by a tracking line
	var missing []workdir.AttEntry
	for _, l := range lines {
		cur := c.current(l, dir)
		taken[cur] = true
		exists := c.t.Exists(cur)
		el := localEls[l.AttID]
		if el == nil {
			if baseEls[l.AttID] != nil {
				out = append(out, c.change('D', cur, l.AttID, "delete", resPath, local))
				continue
			}
			if exists {
				changed, err := attach.Changed(c.t, cur, l)
				if err != nil {
					return nil, err
				}
				if changed {
					out = append(out, AttChange{Status: 'C', Path: cur, AttID: l.AttID, Note: "deleted on remote; kept because it changed locally"})
				}
			}
			continue
		}
		if !exists {
			missing = append(missing, l) // evicted: silent
			continue
		}
		changed, err := attach.Changed(c.t, cur, l)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if v := attach.Version(el, c.e); v != l.Version {
			out = append(out, AttChange{Status: 'C', Path: cur, AttID: l.AttID,
				Note: fmt.Sprintf("changed locally and on remote v%s; remote copy: %s", v, path.Base(attach.ConflictCopy(cur, v)))})
			continue
		}
		out = append(out, c.change('M', cur, l.AttID, "update", resPath, local))
	}
	baseDerived := attach.Derive(base, c.e, resPath)
	for _, id := range sortedKeys(baseEls) {
		if localEls[id] != nil {
			continue
		}
		if _, fetched := c.atts.Get(resID, id); fetched {
			continue
		}
		ch := c.change('D', baseDerived[id], id, "delete", resPath, local)
		if ch.Action != nil {
			ch.Action.Detail += " (not fetched)"
		}
		out = append(out, ch)
	}
	unfetched := map[string]bool{}
	for id, p := range attach.Derive(local, c.e, resPath) {
		if _, ok := c.atts.Get(resID, id); !ok {
			unfetched[p] = true
		}
	}
	for _, f := range files {
		if taken[f] {
			continue
		}
		if orig, ok := attach.ConflictOriginal(path.Base(f)); ok && taken[dir+"/"+orig] {
			continue
		}
		switch {
		case unfetched[f]:
			out = append(out, AttChange{Status: '!', Path: f, Note: "file exists, not fetched by gfs; move it away or delete it"})
		case c.renamed(f, missing):
			out = append(out, AttChange{Status: '!', Path: f, Note: "rename of attachments is not supported; rename it on the remote, or delete it and add it again"})
		default:
			out = append(out, c.change('A', f, "", "create", resPath, local))
		}
	}
	if explicit != "" {
		for i := range out {
			if out[i].Action != nil {
				out[i].Action, out[i].Note = nil, "included in "+explicit
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// current is where a tracked file is now: its recorded path, or the same name
// in the resource's sidecar when the sidecar moved along with the resource.
func (c *attCtx) current(l workdir.AttEntry, dir string) string {
	if c.t.Exists(l.Path) {
		return l.Path
	}
	alt := dir + "/" + path.Base(l.Path)
	if _, tracked := c.atts.ByPath(alt); alt != l.Path && !tracked && c.t.Exists(alt) {
		return alt
	}
	return l.Path
}

// renamed reports whether f has the content of a tracked file that is missing
// from the same folder (a local rename, attachments spec 3.5).
func (c *attCtx) renamed(f string, missing []workdir.AttEntry) bool {
	st, err := c.t.Stat(f)
	if err != nil {
		return false
	}
	for _, l := range missing {
		if path.Dir(l.Path) != path.Dir(f) || st.Size() != l.Size {
			continue
		}
		if sha, _, _, err := attach.Hash(c.t, f); err == nil && sha == l.SHA {
			return true
		}
	}
	return false
}

// change builds an attachment change with its action, or a refusal when the
// adapter does not allow verb or the file is too large.
func (c *attCtx) change(status byte, p, attID, verb, resPath string, local *xmltree.Node) AttChange {
	ch := AttChange{Status: status, Path: p, AttID: attID}
	if !c.e.Allows(verb) {
		ch.Status = '!'
		if len(c.e.Ops) == 0 {
			ch.Note = "attachments of this resource are read-only"
		} else {
			ch.Note = verb + " of attachments is not supported"
		}
		return ch
	}
	if verb != "delete" && c.e.MaxSize > 0 {
		if st, err := c.t.Stat(p); err == nil && st.Size() > c.e.MaxSize {
			ch.Status = '!'
			ch.Note = fmt.Sprintf("%d bytes is over the upload limit of %d", st.Size(), c.e.MaxSize)
			return ch
		}
	}
	a := adapter.Action{Verb: verb, Target: adapter.AttachmentTarget(c.e.Name, attID), File: p}
	if verb == "create" {
		a.Target = adapter.NewAttachmentTarget(c.e.Name, path.Base(p))
	}
	c.ad.Describe(&a, &adapter.Resource{Path: resPath, Root: local})
	ch.Action = &a
	return ch
}

// orphans reports tracked files whose resource is gone and changed locally
// (kept as C), and sidecar folders that no resource accounts for.
func (c *attCtx) orphans(seen map[string]string) ([]FileChange, error) {
	if c.e == nil {
		return nil, nil
	}
	gone := map[string][]AttChange{}
	var goneKeys []string
	for _, l := range c.atts.All() {
		if _, ok := seen[l.ResID]; ok {
			continue
		}
		c.claimed[path.Dir(l.Path)] = true
		if !c.t.Exists(l.Path) {
			continue
		}
		changed, err := attach.Changed(c.t, l.Path, l)
		if err != nil {
			return nil, err
		}
		if changed {
			res, _ := attach.ResourceOf(l.Path)
			if _, ok := gone[res]; !ok {
				goneKeys = append(goneKeys, res)
			}
			gone[res] = append(gone[res], AttChange{Status: 'C', Path: l.Path, AttID: l.AttID,
				Note: "its resource file is gone; kept because it changed locally"})
		}
	}
	var out []FileChange
	for _, res := range goneKeys {
		out = append(out, FileChange{Status: 'C', Path: res, Quiet: true, Attachments: gone[res]})
	}
	dirs, err := c.t.Sidecars()
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if c.claimed[d] {
			continue
		}
		res, _ := attach.ResourceOf(d + "/x")
		out = append(out, FileChange{Status: '!', Path: d, Err: fmt.Errorf("attachment folder without its resource file %s", res)})
	}
	return out, nil
}

// attachmentActions orders attachment actions: creates and updates, then deletes.
func attachmentActions(acs []AttChange) []adapter.Action {
	var first, last []adapter.Action
	for _, a := range acs {
		switch {
		case a.Action == nil:
		case a.Action.Verb == "delete":
			last = append(last, *a.Action)
		default:
			first = append(first, *a.Action)
		}
	}
	return append(first, last...)
}

func sortedKeys(m map[string]*xmltree.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
