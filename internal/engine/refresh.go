package engine

import (
	"context"
	"fmt"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

// refresh re-reads the resources fc's actions changed besides fc itself
// (jira move spec §4.2): the sub-tasks a move took along. Files with local
// changes, and files this commit has yet to handle, are left alone.
func (e *Env) refresh(ctx context.Context, fc changes.FileChange, results []adapter.Result, r *Report) error {
	for _, res := range results {
		if res.Err != nil {
			continue
		}
		for _, id := range res.Refresh {
			entry, ok := e.Index.ByID(id)
			if !ok || e.uncommitted[id] {
				continue
			}
			if e.locallyChanged(entry.Path) {
				fmt.Fprintf(e.Out, "warning: %s: changed on the remote by this commit; run gfs pull\n", entry.Path)
				continue
			}
			remote, err := e.Session.Fetch(ctx, id)
			if err != nil {
				r.Failed++
				e.line("refresh", entry.Path, "", "FAIL", oneLine(err))
				e.Log("refresh", entry.Path, "", "FAIL", oneLine(err))
				continue
			}
			if err := e.Store(remote, entry.Path); err != nil {
				return err
			}
			if _, err := e.placeAttachments(id, remote.Path, remote.Root); err != nil {
				return err
			}
			e.line("refresh", entry.Path, remote.Path, "ok", "moved with "+fc.Path)
			e.Log("refresh", entry.Path, remote.Path, "ok", "moved with "+fc.Path)
		}
	}
	return nil
}

// locallyChanged reports whether the working file at p differs from its
// base, or is missing or unreadable.
func (e *Env) locallyChanged(p string) bool {
	s := e.Adapter.Schema()
	ldata, err1 := e.Tree.ReadFile(p)
	bdata, err2 := e.Tree.ReadBase(p)
	if err1 != nil || err2 != nil {
		return true
	}
	local, err1 := envelope.Parse(ldata)
	base, err2 := envelope.Parse(bdata)
	if err1 != nil || err2 != nil || local.Action != "" || len(local.Errors) > 0 || local.Conflict != nil {
		return true
	}
	return changes.CanonContent(local.Content, s) != changes.CanonContent(base.Content, s)
}
