package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

func TestActionsForPaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "fake://x", "wt")
	t.Chdir(dir + "/wt")
	fk.Remote.Advice = map[string]adapter.Advice{"1": {
		State: "status: In Progress",
		Note:  `local <status> Done -> "Finish"`,
		Items: []adapter.Available{
			{Verb: "transition", Name: "Finish", To: "Done"},
			{Verb: "transition", Name: "Resolve this issue", To: "Closed", Fields: []adapter.AvailableField{
				{Element: "resolution", Required: true, Allowed: []string{"Done", "Won't Do"}},
				{Element: "link"},
			}},
		},
	}}
	t.Cleanup(func() { fk.Remote.Advice = nil })
	got := mustRun(t, 0, "actions", "a/one.xml")
	want := "a/one.xml    status: In Progress\n" +
		"  transition  Finish                    -> Done\n" +
		"  transition  Resolve this issue        -> Closed\n" +
		"                requires <resolution>: Done | Won't Do\n" +
		"                optional <link>\n" +
		"  note: local <status> Done -> \"Finish\"\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	writeFile(t, "a/new.xml", "<note><title>N</title></note>")
	mustContain(t, mustRun(t, 0, "actions", "a/new.xml"), "a/new.xml    not on the remote yet")
	mustContain(t, mustRun(t, 2, "actions", "a/missing.xml"), "a/missing.xml")
	if out := mustRun(t, 0, "actions"); !strings.Contains(out, "create  allow") {
		t.Fatalf("verb list changed:\n%s", out)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
