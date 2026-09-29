package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Clone creates a working tree at dir from the remote; progress may be nil.
func Clone(ctx context.Context, ad adapter.Adapter, sess adapter.Session, rawURL, dir string, out io.Writer, progress func(adapter.Progress)) (*Env, error) {
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", rawURL)
	if id, ok := sess.(adapter.Identified); ok && id.Identity() != "" {
		cfg.Set("remote", "email", id.Identity())
	}
	t, err := workdir.Init(dir, cfg)
	if err != nil {
		return nil, err
	}
	ix, err := t.LoadIndex()
	if err != nil {
		return nil, err
	}
	env := &Env{Tree: t, Index: ix, Atts: workdir.NewAttachments(), Adapter: ad, Session: sess, Out: out, Now: time.Now, Progress: progress}
	env.report(adapter.Progress{Phase: "list"})
	l, err := sess.List(ctx, "")
	if err != nil {
		env.report(adapter.Progress{Phase: "done"})
		return nil, err
	}
	n := 0
	for i, r := range l.Resources {
		env.report(adapter.Progress{Phase: "fetch", Done: i, Total: len(l.Resources), Item: r.Path})
		res, err := env.Resolved(ctx, r)
		if errors.Is(err, adapter.ErrNotFound) {
			continue // deleted after listing
		}
		if err != nil {
			env.report(adapter.Progress{Phase: "done"})
			return nil, fmt.Errorf("fetch %s: %w", r.ID, err)
		}
		if err := env.Store(res, ""); err != nil {
			env.report(adapter.Progress{Phase: "done"})
			return nil, err
		}
		n++
	}
	env.report(adapter.Progress{Phase: "done"})
	ix.Cursor = l.Cursor
	if err := t.SaveIndex(ix); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Cloned %d resources from %s into %s\n", n, rawURL, dir)
	return env, nil
}
