// Package engine implements clone, commit, pull and resolve (spec 3, 6, 7).
package engine

import (
	"context"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
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
	Now     func() time.Time
	// Progress, when set, hears the steps of clone and pull, for display.
	Progress func(adapter.Progress)
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
	return e.StoreBase(res, oldPath)
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
	return e.Tree.SaveIndex(e.Index)
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
	return e.Tree.SaveIndex(e.Index)
}

func (e *Env) Log(verb, path, newPath, outcome, detail string) {
	if newPath == path {
		newPath = ""
	}
	_ = e.Tree.AppendLog(workdir.LogEntry{At: e.Now(), Verb: verb, Path: path, NewPath: newPath, Outcome: outcome, Detail: detail})
}

func (e *Env) IDByPath(path string) (string, bool) {
	en, ok := e.Index.ByPath(path)
	return en.ID, ok
}

func (e *Env) Resolved(ctx context.Context, r adapter.Resource) (*adapter.Resource, error) {
	if r.Root != nil {
		return &r, nil
	}
	return e.Session.Fetch(ctx, r.ID)
}
