package textdiff

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func L(s string) []string { return Lines(s) }

func TestLines(t *testing.T) {
	if got := Lines("a\nb\n"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatal(got)
	}
	if got := Lines(""); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestMatches(t *testing.T) {
	m := Matches(L("a\nb\nc\nd"), L("a\nx\nc\nd\ne"))
	if !slices.Equal(m, []int{0, -1, 2, 3}) {
		t.Fatal(m)
	}
}

func TestMerge3(t *testing.T) {
	cases := []struct {
		name, base, local, remote string
		want                      string
		conflicts                 int
	}{
		{"local only", "a\nb\nc", "a\nB\nc", "a\nb\nc", "a\nB\nc", 0},
		{"remote only", "a\nb\nc", "a\nb\nc", "a\nb\nC", "a\nb\nC", 0},
		{"same change", "a\nb\nc", "a\nX\nc", "a\nX\nc", "a\nX\nc", 0},
		{"separate changes", "a\nb\nc\nd\ne", "a\nB\nc\nd\ne", "a\nb\nc\nd\nE", "a\nB\nc\nd\nE", 0},
		{"local insert remote delete elsewhere", "a\nb\nc\nd", "a\nnew\nb\nc\nd", "a\nb\nc", "a\nnew\nb\nc", 0},
		{"overlap", "a\nb\nc", "a\nL\nc", "a\nR\nc",
			"a\n<<<<<<< local\nL\n||||||| base\nb\n=======\nR\n>>>>>>> remote v8\nc", 1},
		{"both insert same spot", "a\nc", "a\nL\nc", "a\nR\nc",
			"a\n<<<<<<< local\nL\n||||||| base\n=======\nR\n>>>>>>> remote v8\nc", 1},
		{"adjacent changes conflict", "a\nb\nc", "a\nB\nc", "a\nb\nC",
			"a\n<<<<<<< local\nB\nc\n||||||| base\nb\nc\n=======\nb\nC\n>>>>>>> remote v8", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := Render(Merge3(L(c.base), L(c.local), L(c.remote)), "remote v8")
			if strings.Join(got, "\n") != c.want || n != c.conflicts {
				t.Fatalf("got (%d conflicts)\n%s\nwant (%d)\n%s", n, strings.Join(got, "\n"), c.conflicts, c.want)
			}
		})
	}
}

func TestUnified(t *testing.T) {
	var a, b []string
	for i := 1; i <= 20; i++ {
		a = append(a, fmt.Sprint(i))
		b = append(b, fmt.Sprint(i))
	}
	b[4] = "five"
	b = append(b, "21")
	want := `--- a/x.xml
+++ b/x.xml
@@ -2,7 +2,7 @@
 2
 3
 4
-5
+five
 6
 7
 8
@@ -18,3 +18,4 @@
 18
 19
 20
+21
`
	if got := Unified("a/x.xml", "b/x.xml", a, b); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if Unified("a", "b", a, a) != "" {
		t.Fatal("equal inputs must give empty diff")
	}
	got := Unified("a", "b", nil, L("x"))
	if got != "--- a\n+++ b\n@@ -0,0 +1 @@\n+x\n" {
		t.Fatalf("new file diff:\n%s", got)
	}
}
