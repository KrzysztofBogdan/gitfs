// Package engine implements clone, commit, pull and resolve (spec 3, 6, 7).
package engine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type Env struct {
	Tree    *workdir.Tree
	Index   *workdir.Index
	Atts    *workdir.Attachments // fetched attachments; saved after every change
	Adapter adapter.Adapter
	Session adapter.Session
	Out     io.Writer
	Prompt  func(string) bool // nil: not a TTY
	// ReadSecret reads a secret without echo for Apply (ApplyRequest.Secret);
	// nil: not a TTY.
	ReadSecret func(prompt string) (string, error)
	Now        func() time.Time
	// Progress, when set, hears the steps of clone and pull, for display.
	Progress func(adapter.Progress)
	// Batch defers index saves to every batchSize changes; clone and pull
	// set it and save once at the end. IndexSaves counts writes, for tests.
	Batch      bool
	IndexSaves int
	pending    int
	// One commit's view of paths (parent-child spec §2.3): where write-back
	// moved files, and in a dry run the new files the commit would create.
	renamed map[string]string
	creates map[string]bool
	// ids of the running commit's files not committed yet (jira move spec §4.2)
	uncommitted map[string]bool
}

func (e *Env) report(p adapter.Progress) {
	if e.Progress != nil {
		e.Progress(p)
	}
}

func (e *Env) Canonical(root *xmltree.Node) []byte {
	return envelope.Bytes(envelope.New(root.Clone()), e.Adapter.Schema())
}

func (e *Env) Store(res *adapter.Resource, oldPath string) error {
	if err := e.Tree.WriteFile(res.Path, e.Canonical(res.Root)); err != nil {
		return err
	}
	if oldPath != "" && oldPath != res.Path && e.Tree.Exists(oldPath) {
		if err := e.Tree.Remove(oldPath); err != nil {
			return err
		}
	}
	if err := e.StoreBase(res, oldPath); err != nil {
		return err
	}
	return e.storeFiles(res)
}

// storeFiles writes the attachment bytes a listing delivered and tracks them
// as if fetched (ksef spec §2.2). A tracked file changed locally is kept.
func (e *Env) storeFiles(res *adapter.Resource) error {
	el := e.attachmentElem()
	if len(res.Files) == 0 || el == nil {
		return nil
	}
	derived := attach.Derive(e.placement(), res.Root, el, res.Path)
	els := attach.Elements(res.Root, el)
	for _, id := range sortedIDs(derived) {
		data, ok := res.Files[id]
		if !ok {
			continue
		}
		p := derived[id]
		l, tracked := e.Atts.Get(res.ID, id)
		if tracked && e.Tree.Exists(l.Path) {
			changed, err := attach.Changed(e.Tree, l.Path, l)
			if err != nil {
				return err
			}
			if changed {
				fmt.Fprintf(e.Out, "  !  %s   changed locally; kept\n", l.Path)
				continue
			}
		} else if !tracked && e.Tree.Exists(p) {
			fmt.Fprintf(e.Out, "  !  %s   exists locally and is not tracked; skipped\n", p)
			continue
		}
		if err := e.Tree.WriteFile(p, data); err != nil {
			return err
		}
		if tracked && l.Path != p && e.Tree.Exists(l.Path) { // moved with its resource
			if err := e.Tree.Remove(l.Path); err != nil {
				return err
			}
		}
		line, err := attach.Entry(e.Tree, res.ID, id, attach.Version(els[id], el), p)
		if err != nil {
			return err
		}
		e.Atts.Put(line)
	}
	if e.Batch {
		return nil // saved with the index
	}
	return e.saveAtts()
}

func (e *Env) StoreBase(res *adapter.Resource, oldPath string) error {
	if oldPath != "" && oldPath != res.Path {
		if err := e.Tree.RemoveBase(oldPath); err != nil {
			return err
		}
	}
	if err := e.Tree.WriteBase(res.Path, e.Canonical(res.Root)); err != nil {
		return err
	}
	e.Index.Put(workdir.Entry{ID: res.ID, Version: res.Version, Path: res.Path})
	return e.saveIndex(false)
}

func (e *Env) Forget(id, path string) error {
	if err := e.dropAttachments(id); err != nil {
		return err
	}
	if e.Tree.Exists(path) {
		if err := e.Tree.Remove(path); err != nil {
			return err
		}
	}
	if err := e.Tree.RemoveBase(path); err != nil {
		return err
	}
	e.Index.Delete(id)
	return e.saveIndex(false)
}

func (e *Env) Log(verb, path, newPath, outcome, detail string) {
	if newPath == path {
		newPath = ""
	}
	_ = e.Tree.AppendLog(workdir.LogEntry{At: e.Now(), Verb: verb, Path: path, NewPath: newPath, Outcome: outcome, Detail: detail})
}

func (e *Env) IDByPath(path string) (string, bool) {
	for range len(e.renamed) {
		next, ok := e.renamed[path]
		if !ok {
			break
		}
		path = next
	}
	if en, ok := e.Index.ByPath(path); ok {
		return en.ID, true
	}
	if e.creates[path] {
		return adapter.Pending, true
	}
	return "", false
}

func (e *Env) Resolved(ctx context.Context, r adapter.Resource) (*adapter.Resource, error) {
	if r.Root != nil {
		return &r, nil
	}
	return e.Session.Fetch(ctx, r.ID)
}

const batchSize = 500

// saveIndex writes the index now, or every batchSize calls while batching.
func (e *Env) saveIndex(force bool) error {
	if e.Batch && !force {
		if e.pending++; e.pending < batchSize {
			return nil
		}
	}
	e.pending = 0
	e.IndexSaves++
	if e.Batch && e.Atts != nil {
		if err := e.saveAtts(); err != nil { // tracking lines written during the batch
			return err
		}
	}
	return e.Tree.SaveIndex(e.Index)
}
