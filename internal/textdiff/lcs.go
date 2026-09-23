// Package textdiff implements the line-based diff, diff3 and unified output.
package textdiff

import "strings"

func Lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Matches returns, for each line of a, the index of the matched line in b
// under a longest common subsequence, or -1.
func Matches(a, b []string) []int {
	m := make([]int, len(a))
	for i := range m {
		m[i] = -1
	}
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		m[pre] = pre
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		m[len(a)-1-suf] = len(b) - 1 - suf
		suf++
	}
	x, y := a[pre:len(a)-suf], b[pre:len(b)-suf]
	n, k := len(x), len(y)
	// t[i][j] = LCS length of x[i:], y[j:]
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, k+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			if x[i] == y[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	for i, j := 0, 0; i < n && j < k; {
		switch {
		case x[i] == y[j]:
			m[pre+i] = pre + j
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			i++
		default:
			j++
		}
	}
	return m
}
