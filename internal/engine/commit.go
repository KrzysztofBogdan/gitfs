package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type CommitOpts struct {
	DryRun  bool
	Allow   map[string]bool
	NoMerge bool
	Filter  func(string) bool
}

type Report struct{ Actions, Failed, Denied, Conflicts int }

func (r Report) ExitCode() int {
	if r.Failed+r.Denied+r.Conflicts > 0 {
		return 1
	}
	return 0
}

func category(fc changes.FileChange) int {
	switch fc.Status {
	case 'A':
		return 0
	case 'R':
		return 2
	case 'D':
		return 3
	}
	return 1
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func oneLine(err error) string { return strings.ReplaceAll(err.Error(), "\n", "; ") }

// actPath is the path an action is reported under: its attachment file, or the resource.
func actPath(fc changes.FileChange, a adapter.Action) string {
	if a.IsAttachment() {
		return a.File
	}
	return fc.Path
}

func (e *Env) line(verb, path, newPath, outcome, detail string) {
	p := path
	if newPath != "" && newPath != path {
		p += " -> " + newPath
	}
	s := fmt.Sprintf("%-7s %s   %s", verb, p, outcome)
	if detail != "" {
		s += "  " + detail
	}
	fmt.Fprintln(e.Out, s)
}

func (e *Env) policy() (*policy.Policy, error) {
	cfg, err := e.Tree.LoadConfig()
	if err != nil {
		return nil, err
	}
	return policy.FromConfig(cfg.Section("policy"))
}

func Commit(ctx context.Context, e *Env, o CommitOpts) (Report, error) {
	pol, err := e.policy()
	if err != nil {
		return Report{}, err
	}
	cs, err := changes.Compute(e.Tree, e.Index, e.Adapter, o.Filter)
	if err != nil {
		return Report{}, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return category(cs[i]) < category(cs[j]) })
	d := &policy.Decider{Policy: pol, Allowed: o.Allow, Prompt: e.Prompt}
	var r Report
	for _, fc := range cs {
		var err error
		if o.DryRun {
			e.dryRunFile(ctx, fc, d, &r)
		} else {
			err = e.commitFile(ctx, fc, d, o, &r)
		}
		if err != nil {
			return r, err
		}
	}
	footer := fmt.Sprintf("%s, %d failed, %d denied", plural(r.Actions, "action"), r.Failed, r.Denied)
	if r.Conflicts > 0 {
		footer += ", " + plural(r.Conflicts, "conflict")
	}
	if o.DryRun {
		footer = "dry run: " + footer
	}
	fmt.Fprintln(e.Out, footer)
	return r, nil
}

// precheck reports attachment conflicts and refusals, then handles C, invalid
// and no-op files. It returns false when the file is done.
func (e *Env) precheck(fc changes.FileChange, r *Report) bool {
	for _, a := range fc.Attachments {
		switch a.Status {
		case 'C':
			r.Conflicts++
			fmt.Fprintf(e.Out, "  C  %s   %s\n", a.Path, a.Note)
		case '!':
			r.Failed++
			e.line("invalid", a.Path, "", "FAIL", a.Note)
			e.Log("invalid", a.Path, "", "FAIL", a.Note)
		}
	}
	if fc.Quiet && fc.Status == 'C' {
		return false // tracked files of a resource that is gone, reported above
	}
	switch {
	case fc.Status == 'C':
		r.Conflicts++
		fmt.Fprintf(e.Out, "  C  %s   unresolved conflict: edit, remove <conflict/>, commit (or gfs resolve)\n", fc.Path)
		return false
	case fc.Err != nil:
		r.Failed++
		e.line("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		e.Log("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		return false
	case len(fc.Actions) == 0:
		if fc.Note != "" {
			fmt.Fprintf(e.Out, "warning: %s: %s\n", fc.Path, fc.Note)
		}
		return false
	}
	return true
}

func (e *Env) dryRunFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, r *Report) {
	if !e.precheck(fc, r) {
		return
	}
	var checks []adapter.Result
	if fc.Local != nil {
		checks = e.Session.Check(ctx, adapter.ApplyRequest{
			Local: &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: fc.Local.Content},
			Base:  baseResource(fc), Actions: fc.Actions, IDByPath: e.IDByPath, Open: e.open,
		})
	}
	for i, a := range fc.Actions {
		level := d.Policy.Level(a.Class)
		mark := ""
		switch {
		case level == policy.Deny:
			mark = "[deny]"
			r.Denied++
		case level == policy.Ask && !d.Allowed[a.Class]:
			mark = "[ask]"
			if d.Prompt == nil {
				r.Denied++
			} else {
				r.Actions++
			}
		default:
			r.Actions++
		}
		if i < len(checks) && checks[i].Err != nil {
			r.Failed++
			e.line(a.Verb, actPath(fc, a), a.To, "FAIL", oneLine(checks[i].Err))
			continue
		}
		e.line(a.Verb, actPath(fc, a), a.To, "would run", strings.TrimSpace(a.Detail+"  "+mark))
	}
}

func baseResource(fc changes.FileChange) *adapter.Resource {
	if fc.Base == nil {
		return nil
	}
	return &adapter.Resource{ID: fc.ID, Version: fc.Entry.Version, Path: fc.Entry.Path, Root: fc.Base.Content}
}

func (e *Env) commitFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, o CommitOpts, r *Report) error {
	if !e.precheck(fc, r) {
		return nil
	}
	for _, a := range fc.Actions {
		p := actPath(fc, a)
		if run, reason := d.Decide(a.Class, fmt.Sprintf("%s %s ?", a.Verb, p)); !run {
			r.Denied++
			e.line(a.Verb, p, "", "denied", reason)
			e.Log(a.Verb, p, "", "denied", reason)
			return nil
		}
	}
	s := e.Adapter.Schema()
	acts := fc.Actions
	for attempt := 0; ; attempt++ {
		var remote *adapter.Resource
		content := (*xmltree.Node)(nil)
		if fc.Local != nil {
			content = fc.Local.Content
		}
		merged := false
		if fc.Base != nil {
			var err error
			remote, err = e.Session.Fetch(ctx, fc.ID)
			if errors.Is(err, adapter.ErrNotFound) {
				if fc.Status == 'D' {
					r.Actions++
					e.line("delete", fc.Path, "", "ok", "already deleted on remote")
					e.Log("delete", fc.Path, "", "ok", "already deleted on remote")
					return e.Forget(fc.ID, fc.Path)
				}
				return e.failAll(fc, acts, errors.New("deleted on remote; run gfs pull"), r)
			}
			if err != nil {
				return e.failAll(fc, acts, err, r)
			}
			if changes.CanonContent(remote.Root, s) != changes.CanonContent(fc.Base.Content, s) {
				if fc.Status == 'D' {
					r.Conflicts++
					fmt.Fprintf(e.Out, "  C  %s   changed on remote since base; run gfs pull\n", fc.Path)
					return nil
				}
				if o.NoMerge {
					return e.refuse(fc, remote, r)
				}
				res, err := merge.Merge(fc.Base.Content, fc.Local.Content, remote.Root, s, "remote v"+remote.Version)
				if err != nil {
					return e.failAll(fc, acts, fmt.Errorf("merge: %w", err), r)
				}
				if res.Conflicted() {
					return e.writeConflict(fc, remote, res, r)
				}
				content, merged = res.Root, true
			}
			if fc.Status != 'D' {
				if acts, err = e.checkAttachments(ctx, fc, remote, acts, r); err != nil {
					return err
				}
				if len(acts) == 0 {
					if fc.Quiet {
						return e.fastForward(fc, remote, content)
					}
					return nil
				}
			}
		}
		local := &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: content}
		if fc.Status == 'D' {
			local = remote
		}
		req := adapter.ApplyRequest{Local: local, Base: remote, Actions: acts, IDByPath: e.IDByPath,
			Open: e.open, Files: e.sidecarFiles(fc)}
		if remote != nil {
			req.Lock = remote.Version
		}
		results := e.Session.Apply(ctx, req)
		if len(results) > 0 && errors.Is(results[0].Err, adapter.ErrLock) && attempt == 0 {
			continue
		}
		return e.finish(ctx, fc, content, results, merged, r)
	}
}

func (e *Env) errorsFor(failed []adapter.Result) []envelope.Error {
	var out []envelope.Error
	for _, f := range failed {
		out = append(out, envelope.Error{Action: f.Action.Verb, Target: f.Action.Target, Code: f.Code,
			At: e.Now().Format(time.RFC3339), Msg: oneLine(f.Err)})
	}
	return out
}

func (e *Env) failAll(fc changes.FileChange, acts []adapter.Action, err error, r *Report) error {
	var results []adapter.Result
	for _, a := range acts {
		results = append(results, adapter.Result{Action: a, Err: err})
	}
	return e.finish(context.Background(), fc, nil, results, false, r)
}

func (e *Env) refuse(fc changes.FileChange, remote *adapter.Resource, r *Report) error {
	doc := *fc.Local
	doc.Conflict = &envelope.Conflict{RemoteVersion: remote.Version, By: remote.By, At: remote.At}
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   remote moved to v%s (--no-merge)\n", fc.Path, remote.Version)
	e.Log("merge", fc.Path, "", "FAIL", "remote moved to v"+remote.Version+" (--no-merge)")
	return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, e.Adapter.Schema()))
}

func (e *Env) writeConflict(fc changes.FileChange, remote *adapter.Resource, res merge.Result, r *Report) error {
	doc := &envelope.Doc{Action: fc.Local.Action, Params: fc.Local.Params, Conflict: &envelope.Conflict{
		RemoteVersion: remote.Version, By: remote.By, At: remote.At, Elements: res.Elements, Hunks: res.Hunks}}
	if err := e.Tree.WriteFile(fc.Path, []byte(envelope.Header(doc)+res.Text+"\n"+envelope.Footer)); err != nil {
		return err
	}
	base := *remote
	base.Path = fc.Path
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   conflict with remote v%s by %s %s, %s, %s\n", fc.Path, remote.Version, remote.By, remote.At,
		plural(res.Elements, "element"), plural(res.Hunks, "hunk"))
	e.Log("merge", fc.Path, "", "FAIL", "conflict with remote v"+remote.Version)
	return e.StoreBase(&base, fc.Entry.Path)
}

func (e *Env) finish(ctx context.Context, fc changes.FileChange, content *xmltree.Node, results []adapter.Result, merged bool, r *Report) error {
	s := e.Adapter.Schema()
	var failed []adapter.Result
	id := fc.ID
	moveFailed, deleted := false, false
	for _, res := range results {
		r.Actions++
		if res.Err != nil {
			r.Failed++
			failed = append(failed, res)
			moveFailed = moveFailed || res.Action.Verb == "move"
			continue
		}
		if res.ID != "" && !res.Action.IsAttachment() {
			id = res.ID
		}
		if res.Action.Verb == "delete" && res.Action.Target == "" {
			deleted = true
		}
	}
	report := func(newPath string) {
		for _, res := range results {
			a := res.Action
			outcome, detail := "ok", res.Detail
			if merged {
				outcome = "merged"
			}
			if a.IsAttachment() && a.Verb == "create" && detail == "" && res.ID != "" {
				detail = "id=" + res.ID
			}
			if res.Err != nil {
				outcome, detail = "FAIL", oneLine(res.Err)
			}
			np, target := "", a.Target
			if !a.IsAttachment() && (a.Verb == "move" || res.Err == nil) {
				np = newPath
			}
			if a.IsAttachment() {
				target = "" // the path already names the attachment
			}
			p := actPath(fc, a)
			e.line(a.Verb, p, np, outcome, strings.TrimSpace(target+" "+detail))
			e.Log(a.Verb, p, np, outcome, strings.TrimSpace(target+" "+detail))
		}
	}
	if len(failed) == len(results) {
		report("")
		if fc.Local == nil { // deleted file: nothing to annotate
			return nil
		}
		doc := *fc.Local
		doc.Errors, doc.Conflict = e.errorsFor(failed), nil
		return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, s))
	}
	if deleted {
		report("")
		return e.Forget(fc.ID, fc.Entry.Path)
	}
	remote, err := e.Session.Fetch(ctx, id)
	if err != nil {
		report("")
		r.Failed++
		e.line("fetch", fc.Path, "", "FAIL", "write-back: "+oneLine(err))
		return nil
	}
	wb := remote.Root.Clone()
	if fc.Quiet && content != nil {
		wb = e.withRemoteAttachments(content, remote.Root) // keep unselected local edits
	}
	doc := envelope.New(wb)
	path := remote.Path
	if len(failed) > 0 {
		keepLocal(wb, content, failed, s)
		doc.Errors = e.errorsFor(failed)
		if fc.Local.Action != "" {
			doc.Action, doc.Params = fc.Local.Action, fc.Local.Params
		}
		if moveFailed {
			path = fc.Path
		}
	}
	if err := e.Tree.WriteFile(path, envelope.Bytes(doc, s)); err != nil {
		return err
	}
	if path != fc.Path && e.Tree.Exists(fc.Path) {
		if err := e.Tree.Remove(fc.Path); err != nil {
			return err
		}
	}
	report(path)
	if err := e.StoreBase(remote, fc.Entry.Path); err != nil {
		return err
	}
	return e.syncCommitted(id, path, remote.Root, results)
}

// keepLocal puts the local form of failed parts back into the written-back tree,
// so that a retry is a plain re-commit.
func keepLocal(wb, local *xmltree.Node, failed []adapter.Result, s *schema.Schema) {
	if local == nil {
		return
	}
	for _, f := range failed {
		a := f.Action
		if a.IsAttachment() && a.Verb != "delete" {
			continue // the local file and its tracking line stay as they are
		}
		if a.Target == "" {
			if a.Verb == "update" && a.Group != "" {
				replaceGroup(wb, local, a.Group)
			}
			continue
		}
		name, id, nth, ok := changes.ParseTarget(a.Target)
		e := schema.Find(s.Elems, name)
		if !ok || e == nil {
			continue
		}
		switch a.Verb {
		case "create":
			i := 0
			for _, c := range local.ChildrenNamed(name) {
				if _, has := c.Attr(e.ID); !has {
					if i++; i == nth {
						wb.Children = append(wb.Children, c.Clone())
					}
				}
			}
		case "update":
			if old, nw := validate.FindSub(wb, name, e.ID, id), validate.FindSub(local, name, e.ID, id); old != nil && nw != nil {
				*old = *nw.Clone()
			}
		case "delete":
			old := validate.FindSub(wb, name, e.ID, id)
			var kept []*xmltree.Node
			for _, c := range wb.Children {
				if c != old {
					kept = append(kept, c)
				}
			}
			wb.Children = kept
		}
	}
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}
