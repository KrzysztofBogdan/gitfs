package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsName(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gfs") {
		t.Fatalf("help does not mention gfs:\n%s", out.String())
	}
}

func TestExitCodeFromError(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&ExitError{Code: 1}, 1},
		{&ExitError{Code: 2}, 2},
		{errString("boom"), 2},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
