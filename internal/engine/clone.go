package engine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func Clone(ctx context.Context, ad adapter.Adapter, sess adapter.Session, rawURL, dir string, out io.Writer) (*Env, error) {
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
	env := &Env{Tree: t, Index: ix, Atts: workdir.NewAttachments(), Adapter: ad, Session: sess, Out: out, Now: time.Now}
	l, err := sess.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, r := range l.Resources {
		res, err := env.Resolved(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", r.ID, err)
		}
		if err := env.Store(res, ""); err != nil {
			return nil, err
		}
	}
	ix.Cursor = l.Cursor
	if err := t.SaveIndex(ix); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Cloned %d resources from %s into %s\n", len(l.Resources), rawURL, dir)
	return env, nil
}
