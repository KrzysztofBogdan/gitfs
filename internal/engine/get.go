package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type GetReport struct {
	Fetched, UpToDate, Refused, Failed int
	Bytes                              int64
}

func (r GetReport) ExitCode() int {
	if r.Refused+r.Failed > 0 {
		return 1
	}
	return 0
}

type getItem struct{ resID, attID, path, version string }

// Get downloads attachment bytes on demand (attachments spec 4.1). A target is
// a resource file, a folder (every resource under it, "" for the whole tree),
// a sidecar folder, or one sidecar file.
func Get(ctx context.Context, e *Env, targets []string) (GetReport, error) {
	var r GetReport
	el := e.attachmentElem()
	if el == nil {
		return r, fmt.Errorf("%s resources have no attachments", e.Adapter.Name())
	}
	items, unmatched, err := e.getItems(targets, el)
	if err != nil {
		return r, err
	}
	for _, t := range unmatched {
		if t == "" {
			t = "."
		}
		r.Refused++
		fmt.Fprintf(e.Out, "  !  %s   not a resource, folder or attachment of this tree\n", t)
	}
	for _, it := range items {
		if err := e.getOne(ctx, it, &r); err != nil {
			return r, err
		}
	}
	fmt.Fprintf(e.Out, "%d fetched, %d up to date, %d refused, %d failed (%s)\n", r.Fetched, r.UpToDate, r.Refused, r.Failed, humanSize(r.Bytes))
	return r, e.saveAtts()
}

func (e *Env) getItems(targets []string, el *schema.Elem) ([]getItem, []string, error) {
	working, err := e.workingPaths()
	if err != nil {
		return nil, nil, err
	}
	matched := map[string]bool{}
	seen := map[string]bool{}
	var items []getItem
	for _, en := range e.Index.All() {
		wp := working[en.ID]
		if wp == "" {
			wp = en.Path
		}
		root, err := e.contentOf(wp, en)
		if err != nil {
			return nil, nil, err
		}
		derived := attach.Derive(root, el, wp)
		els := attach.Elements(root, el)
		sd := attach.SidecarDir(wp)
		whole := func(t string) bool { return t == "" || t == wp || t == sd || strings.HasPrefix(wp, t+"/") }
		for _, t := range targets {
			if whole(t) {
				matched[t] = true
			}
		}
		for _, id := range sortedIDs(derived) {
			p := derived[id]
			tracked := p
			if l, ok := e.Atts.Get(en.ID, id); ok {
				tracked = l.Path
			}
			for _, t := range targets {
				if !whole(t) && t != p && t != tracked {
					continue
				}
				matched[t] = true
				if key := en.ID + "\x00" + id; !seen[key] {
					seen[key] = true
					items = append(items, getItem{resID: en.ID, attID: id, path: p, version: attach.Version(els[id], el)})
				}
			}
		}
	}
	var unmatched []string
	for _, t := range targets {
		if !matched[t] {
			unmatched = append(unmatched, t)
		}
	}
	return items, unmatched, nil
}

// workingPaths maps resource id to its working path (resources may be moved locally).
func (e *Env) workingPaths() (map[string]string, error) {
	files, err := e.Tree.Scan()
	if err != nil {
		return nil, err
	}
	idAttr := e.Adapter.Schema().ID
	out := map[string]string{}
	for _, p := range files {
		data, err := e.Tree.ReadFile(p)
		if err != nil {
			return nil, err
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			continue // conflicted or invalid: the base is used
		}
		if id, ok := doc.Content.Attr(idAttr); ok {
			if _, dup := out[id]; !dup {
				out[id] = p
			}
		}
	}
	return out, nil
}

// contentOf returns the working content of a resource, or its base when the
// working file is missing or does not parse.
func (e *Env) contentOf(wp string, en workdir.Entry) (*xmltree.Node, error) {
	if data, err := e.Tree.ReadFile(wp); err == nil {
		if doc, err := envelope.Parse(data); err == nil {
			return doc.Content, nil
		}
	}
	return e.baseRoot(en)
}

func (e *Env) getOne(ctx context.Context, it getItem, r *GetReport) error {
	if l, ok := e.Atts.Get(it.resID, it.attID); ok && e.Tree.Exists(l.Path) {
		changed, err := attach.Changed(e.Tree, l.Path, l)
		if err != nil {
			return err
		}
		if changed {
			r.Refused++
			fmt.Fprintf(e.Out, "  !  %s   changed locally; commit or resolve first\n", l.Path)
			return nil
		}
		if l.Version == it.version {
			r.UpToDate++
			fmt.Fprintf(e.Out, "  =  %s   up to date\n", l.Path)
			return nil
		}
		it.path = l.Path
	} else if e.Tree.Exists(it.path) {
		r.Refused++
		fmt.Fprintf(e.Out, "  !  %s   file exists, not fetched by gfs; move it away or delete it\n", it.path)
		return nil
	}
	line, err := e.download(ctx, it.resID, it.attID, it.path)
	if err != nil {
		r.Failed++
		fmt.Fprintf(e.Out, "  !  %s   FAIL %s\n", it.path, oneLine(err))
		e.Log("get", it.path, "", "FAIL", oneLine(err))
		return nil
	}
	e.Atts.Put(line)
	r.Fetched++
	r.Bytes += line.Size
	ver := ""
	if line.Version != "-" {
		ver = "   v" + line.Version
	}
	note := ""
	if it.version != "-" && line.Version != it.version {
		note = fmt.Sprintf("   (remote has v%s; run gfs pull)", line.Version)
	}
	fmt.Fprintf(e.Out, "  +  %s   %s%s%s\n", it.path, humanSize(line.Size), ver, note)
	e.Log("get", it.path, "", "ok", strings.TrimSpace(ver))
	return nil
}
