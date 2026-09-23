package creds

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGlobalHostDefaults(t *testing.T) {
	d := Dirs{Config: filepath.Join(t.TempDir(), "gfs")}
	g, err := LoadGlobal(d)
	if err != nil || g.HostEmail("acme.atlassian.net") != "" {
		t.Fatalf("missing file must read as empty: %v", err)
	}
	g.SetHostEmail("other.atlassian.net", "me@x.com")
	g.SetHostEmail("ACME.atlassian.net", "me@x.com")
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d.Config, "config"))
	want := "[host \"acme.atlassian.net\"]\nemail = me@x.com\n\n[host \"other.atlassian.net\"]\nemail = me@x.com\n"
	if string(b) != want {
		t.Fatalf("config file:\n%s", b)
	}
	g, _ = LoadGlobal(d)
	if got := g.HostEmail("acme.atlassian.net"); got != "me@x.com" {
		t.Fatalf("host email %q", got)
	}
	if got := g.HostsFor("ME@x.com"); !slices.Equal(got, []string{"acme.atlassian.net", "other.atlassian.net"}) {
		t.Fatalf("hosts %v", got)
	}
	if g.Path() != filepath.Join(d.Config, "config") {
		t.Fatal(g.Path())
	}
}

func TestGlobalBadFile(t *testing.T) {
	d := Dirs{Config: t.TempDir()}
	os.WriteFile(filepath.Join(d.Config, "config"), []byte("no equals sign\n"), 0o644)
	if _, err := LoadGlobal(d); err == nil || !strings.Contains(err.Error(), filepath.Join(d.Config, "config")) {
		t.Fatalf("got %v", err)
	}
}
