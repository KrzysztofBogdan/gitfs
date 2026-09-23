package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

func Resolve(ctx context.Context, e *Env, paths []string, ours bool) error {
	side := "theirs"
	if ours {
		side = "ours"
	}
	s := e.Adapter.Schema()
	for _, p := range paths {
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
