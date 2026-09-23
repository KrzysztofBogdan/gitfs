package textdiff

import (
	"fmt"
	"strings"
)

type op struct {
	kind   byte // ' ', '-', '+'
	line   string
	ai, bi int // position in a / b before this op
}

func script(a, b []string) []op {
	m := Matches(a, b)
	var ops []op
	j := 0
	for i := range a {
		if m[i] < 0 {
			ops = append(ops, op{'-', a[i], i, j})
			continue
		}
		for ; j < m[i]; j++ {
			ops = append(ops, op{'+', b[j], i, j})
		}
		ops = append(ops, op{' ', a[i], i, j})
		j++
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j], len(a), j})
	}
	return ops
}

func rng(start, n int) string {
	s := start + 1
	if n == 0 {
		s = start
	}
	if n == 1 {
		return fmt.Sprintf("%d", s)
	}
	return fmt.Sprintf("%d,%d", s, n)
}

const context = 3

func Unified(aName, bName string, a, b []string) string {
	ops := script(a, b)
	var sb strings.Builder
	i := 0
	for i < len(ops) {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i == len(ops) {
			break
		}
		start := max(0, i-context)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*context {
				end = min(end+context, len(ops))
				break
			}
			end = run
		}
		if sb.Len() == 0 {
			fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
		}
		na, nb := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				na++
			}
			if o.kind != '-' {
				nb++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", rng(ops[start].ai, na), rng(ops[start].bi, nb))
		for _, o := range ops[start:end] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.line)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String()
}
