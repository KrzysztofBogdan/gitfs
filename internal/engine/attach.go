package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func (e *Env) attachmentElem() *schema.Elem { return e.Adapter.Schema().Attachment() }

func (e *Env) saveAtts() error { return e.Tree.SaveAttachments(e.Atts) }

func (e *Env) open(rel string) (io.ReadCloser, error) { return e.Tree.Open(rel) }

// download streams one attachment's current bytes into rel, atomically, and
// returns its tracking line. The caller decides whether to store the line.
func (e *Env) download(ctx context.Context, resID, attID, rel string) (workdir.AttEntry, error) {
	var info adapter.AttachmentInfo
	h := sha256.New()
	err := e.Tree.WriteStream(rel, func(w io.Writer) error {
		var err error
		info, err = e.Session.Download(ctx, resID, attID, io.MultiWriter(w, h))
		return err
	})
	if err != nil {
		return workdir.AttEntry{}, err
	}
	st, err := e.Tree.Stat(rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	return workdir.AttEntry{ResID: resID, AttID: attID, Version: info.Version, SHA: hex.EncodeToString(h.Sum(nil)),
		Size: st.Size(), MTime: attach.LineMTime(st), Path: rel}, nil
}

// baseRoot returns the base content of an indexed resource.
func (e *Env) baseRoot(en workdir.Entry) (*xmltree.Node, error) {
	data, err := e.Tree.ReadBase(en.Path)
	if err != nil {
		return nil, err
	}
	doc, err := envelope.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("base %s: %w", en.Path, err)
	}
	return doc.Content, nil
}

// placeAttachments moves the tracked files of resource id to the paths derived
// from root at resPath (resource moved, attachment renamed, a de-duplication
// number shifted), follows sidecars the user moved along with the resource, and
// drops the lines of evicted files (attachments spec 3.2, 3.5). A move whose
// target is taken waits for a later pass; passes repeat while moves happen.
func (e *Env) placeAttachments(id, resPath string, root *xmltree.Node) ([][2]string, error) {
	el := e.attachmentElem()
	if el == nil {
		return nil, nil
	}
	derived := attach.Derive(root, el, resPath)
	dir := attach.SidecarDir(resPath)
	var moves [][2]string
	for pass, moved := 0, true; moved && pass < 8; pass++ {
		moved = false
		for _, l := range e.Atts.ForResource(id) {
			cur := l.Path
			if !e.Tree.Exists(cur) {
				alt := dir + "/" + path.Base(cur)
				if _, tracked := e.Atts.ByPath(alt); alt == cur || tracked || !e.Tree.Exists(alt) {
					e.Atts.Delete(l.ResID, l.AttID) // evicted
					continue
				}
				cur = alt
			}
			if want, ok := derived[l.AttID]; ok && want != cur && !e.Tree.Exists(want) {
				if err := e.Tree.Rename(cur, want); err != nil {
					return moves, err
				}
				moves = append(moves, [2]string{cur, want})
				cur, moved = want, true
			}
			if cur != l.Path {
				l.Path = cur
				e.Atts.Put(l)
			}
		}
	}
	return moves, e.saveAtts()
}

// forgetAttachment removes one tracked attachment's file, if present, and its line.
func (e *Env) forgetAttachment(resID, attID, rel string) error {
	if e.Tree.Exists(rel) {
		if err := e.Tree.Remove(rel); err != nil {
			return err
		}
	}
	e.Atts.Delete(resID, attID)
	return e.saveAtts()
}

// dropAttachments removes the unchanged tracked files of a resource that is
// gone; changed ones keep their line, so status shows them as C (spec 5.4).
func (e *Env) dropAttachments(id string) error {
	for _, l := range e.Atts.ForResource(id) {
		if e.Tree.Exists(l.Path) {
			changed, err := attach.Changed(e.Tree, l.Path, l)
			if err != nil {
				return err
			}
			if changed {
				continue
			}
			if err := e.Tree.Remove(l.Path); err != nil {
				return err
			}
		}
		e.Atts.Delete(l.ResID, l.AttID)
	}
	return e.saveAtts()
}

// removeConflictCopies deletes the remote copies written next to p.
func (e *Env) removeConflictCopies(p string) {
	files, _, _ := e.Tree.ListSidecar(path.Dir(p))
	for _, f := range files {
		if orig, ok := attach.ConflictOriginal(path.Base(f)); ok && orig == path.Base(p) {
			_ = e.Tree.Remove(f)
		}
	}
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func sortedIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
