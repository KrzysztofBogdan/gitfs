package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func Resolve(ctx context.Context, e *Env, paths []string, ours bool) error {
	side := "theirs"
	if ours {
		side = "ours"
	}
	s := e.Adapter.Schema()
	for _, p := range paths {
		if _, inSidecar := attach.ResourceOf(p); inSidecar {
			if err := e.resolveAttachment(ctx, p, ours); err != nil {
				return err
			}
			fmt.Fprintf(e.Out, "resolved %s (%s)\n", p, side)
			continue
		}
		data, err := e.Tree.ReadFile(p)
		if err != nil {
			return err
		}
		markers := envelope.HasMarkers(data)
		if !markers && !envelope.HasConflictElement(data) {
			return fmt.Errorf("%s: not in conflict", p)
		}
		if markers {
			data = []byte(pickSide(string(data), ours))
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			return fmt.Errorf("%s: result does not parse (%v); edit the file by hand", p, err)
		}
		switch {
		case doc.Conflict != nil && doc.Conflict.RemoteVersion == "deleted" && !ours:
			if err := e.Tree.Remove(p); err != nil {
				return err
			}
		case !markers && !ours:
			en, ok := e.Index.ByPath(p)
			if !ok {
				return fmt.Errorf("%s: not tracked", p)
			}
			remote, err := e.Session.Fetch(ctx, en.ID)
			if err != nil {
				return err
			}
			if err := e.Store(remote, p); err != nil {
				return err
			}
		default:
			doc.Conflict = nil
			if err := e.Tree.WriteFile(p, envelope.Bytes(doc, s)); err != nil {
				return err
			}
		}
		fmt.Fprintf(e.Out, "resolved %s (%s)\n", p, side)
	}
	return nil
}

func pickSide(text string, ours bool) string {
	const (
		outside = iota
		local
		base
		remote
	)
	state := outside
	var out []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			state = local
			continue
		case strings.HasPrefix(line, "||||||| ") && state == local:
			state = base
			continue
		case line == "=======" && (state == base || state == local):
			state = remote
			continue
		case strings.HasPrefix(line, ">>>>>>> ") && state == remote:
			state = outside
			continue
		}
		if state == outside || (ours && state == local) || (!ours && state == remote) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// resolveAttachment picks one side of a conflicted attachment (attachments spec 4.6).
func (e *Env) resolveAttachment(ctx context.Context, p string, ours bool) error {
	el := e.attachmentElem()
	l, ok := e.Atts.ByPath(p)
	if el == nil || !ok {
		return fmt.Errorf("%s: not a tracked attachment", p)
	}
	changed, err := attach.Changed(e.Tree, p, l)
	if err != nil {
		return err
	}
	en, known := e.Index.ByID(l.ResID)
	var rel *xmltree.Node
	if known {
		root, err := e.baseRoot(en)
		if err != nil {
			return err
		}
		rel = attach.Elements(root, el)[l.AttID]
	}
	if rel == nil { // deleted on remote
		switch {
		case !changed:
			return fmt.Errorf("%s: not in conflict", p)
		case !ours:
			return e.forgetAttachment(l.ResID, l.AttID, p)
		case !known:
			return fmt.Errorf("%s: its resource is gone from the remote; move the file elsewhere to keep it", p)
		}
		e.Atts.Delete(l.ResID, l.AttID) // the file becomes a new attachment (A)
		return e.saveAtts()
	}
	v := attach.Version(rel, el)
	if v == l.Version || !changed {
		return fmt.Errorf("%s: not in conflict", p)
	}
	cp := attach.ConflictCopy(p, v)
	if !e.Tree.Exists(cp) {
		if _, err := e.download(ctx, l.ResID, l.AttID, cp); err != nil {
			return err
		}
	}
	if ours {
		remote, err := attach.Entry(e.Tree, l.ResID, l.AttID, v, cp)
		if err != nil {
			return err
		}
		remote.Path, remote.MTime = p, 0 // base = remote bytes, so the local file reads as changed
		e.Atts.Put(remote)
		if err := e.Tree.Remove(cp); err != nil {
			return err
		}
		return e.saveAtts()
	}
	if err := e.Tree.Rename(cp, p); err != nil {
		return err
	}
	line, err := attach.Entry(e.Tree, l.ResID, l.AttID, v, p)
	if err != nil {
		return err
	}
	e.Atts.Put(line)
	return e.saveAtts()
}
