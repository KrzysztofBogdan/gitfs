package cli

import (
	"os"
	"strings"
	"testing"
)

func TestDocsCommands(t *testing.T) {
	mustContain(t, mustRun(t, 0, "help", "start"), "# gfs: getting started")
	mustContain(t, mustRun(t, 0, "help", "confluence"), "# gfs and Confluence Cloud")
	mustContain(t, mustRun(t, 0, "help", "confluence-storage"), "# Confluence storage reference", "## panel")
	mustContain(t, mustRun(t, 0, "--help"), "Additional help topics", "gfs confluence-storage")

	schema := mustRun(t, 0, "schema", "confluence")
	want, err := os.ReadFile("../../docs/confluence/storage.rng")
	if err != nil || schema != string(want) {
		t.Fatalf("gfs schema confluence differs from docs/confluence/storage.rng (%v)", err)
	}
	mustRun(t, 2, "schema", "jira")

	mustContain(t, mustRun(t, 0, "example", "confluence"), "panel", "codeBlock")
	if out := mustRun(t, 0, "example", "confluence", "rule/hr"); strings.TrimSpace(out) != "<hr/>" {
		t.Fatalf("fragment: %q", out)
	}
	mustContain(t, mustRun(t, 0, "example", "confluence", "panel"), "<!-- panel/info (same): info panel -->")
	mustContain(t, mustRun(t, 2, "example", "confluence", "nope"), `unknown node "nope"`)
	mustContain(t, mustRun(t, 2, "example", "confluence", "panel/nope"), `unknown variant "nope" of panel`)
}
