package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

// dependencies maps each file with local content to the paths the adapter
// says it needs created first (parent-child spec §2.1).
func (e *Env) dependencies(cs []changes.FileChange) map[string][]string {
	d, ok := e.Adapter.(adapter.Dependent)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for _, fc := range cs {
		if fc.Local == nil || fc.Err != nil {
			continue
		}
		if ps := d.DependsOn(&adapter.Resource{ID: fc.ID, Path: fc.Path, Root: fc.Local.Content}); len(ps) > 0 {
			out[fc.Path] = ps
		}
	}
	return out
}

// orderCreates puts every new file after the new files it depends on,
// keeping the given order otherwise. New files must lead cs (Commit sorts
// them first). It also returns, per file in a cycle, the cycle.
func orderCreates(cs []changes.FileChange, deps map[string][]string) ([]changes.FileChange, map[string]string) {
	n := 0
	for n < len(cs) && cs[n].Status == 'A' {
		n++
	}
	at := map[string]int{}
	for i, fc := range cs[:n] {
		at[fc.Path] = i
	}
	const visiting, done = 1, 2
	state, cycles := map[string]int{}, map[string]string{}
	var stack []string
	out := make([]changes.FileChange, 0, len(cs))
	var visit func(p string)
	visit = func(p string) {
		switch state[p] {
		case done:
			return
		case visiting:
			loop := stack[slices.Index(stack, p):]
			msg := "parent cycle: " + strings.Join(append(slices.Clone(loop), p), " -> ")
			for _, q := range loop {
				if _, seen := cycles[q]; !seen {
					cycles[q] = msg
				}
			}
			return
		}
		state[p] = visiting
		stack = append(stack, p)
		for _, d := range deps[p] {
			if _, ok := at[d]; ok {
				visit(d)
			}
		}
		stack = stack[:len(stack)-1]
		state[p] = done
		out = append(out, cs[at[p]])
	}
	for _, fc := range cs[:n] {
		visit(fc.Path)
	}
	return append(out, cs[n:]...), cycles
}

// untracked is the first dependency that is neither tracked nor new in this commit.
func (e *Env) untracked(deps []string, adds map[string]bool) string {
	for _, p := range deps {
		if _, ok := e.IDByPath(p); !ok && !adds[p] {
			return p
		}
	}
	return ""
}

// failedDep is the first dependency whose create did not happen in this commit.
func failedDep(deps []string, failed map[string]bool) string {
	for _, p := range deps {
		if failed[p] {
			return p
		}
	}
	return ""
}

// skipBlocked fails fc without sending anything: its parent was not created
// in this commit (parent-child spec §2.3).
func (e *Env) skipBlocked(fc changes.FileChange, parent string, dryRun bool, r *Report) error {
	err := fmt.Errorf("parent %s was not created", parent)
	if dryRun {
		r.Failed++
		e.line(fc.Actions[0].Verb, fc.Path, "", "FAIL", err.Error())
		return nil
	}
	return e.failAll(fc, fc.Actions, err, r)
}
