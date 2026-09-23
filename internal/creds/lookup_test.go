package creds

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func systemEnv(t *testing.T) Dirs {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return DefaultDirs()
}

func TestDefaultDirs(t *testing.T) {
	d := systemEnv(t)
	if d.Config != filepath.Join(d.Home, "xdg", "gfs") {
		t.Fatalf("%+v", d)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultDirs().Config; got != filepath.Join(d.Home, ".config", "gfs") {
		t.Fatal(got)
	}
}

func TestSystemLookup(t *testing.T) {
	d := systemEnv(t)
	var lk Lookup = System{}
	if got := lk.SoleIdentity(); got != "" {
		t.Fatalf("no identities: %q", got)
	}
	Store{Dirs: d}.Set("me@x.com", "tok")
	if got := lk.SoleIdentity(); got != "me@x.com" {
		t.Fatalf("one identity: %q", got)
	}
	if tok, err := lk.Token("me@x.com"); err != nil || tok != "tok" {
		t.Fatalf("token %q %v", tok, err)
	}
	Store{Dirs: d}.Set("two@x.com", "tok2")
	if got := lk.SoleIdentity(); got != "" {
		t.Fatalf("two identities: %q", got)
	}
	g, _ := LoadGlobal(d)
	g.SetHostEmail("acme.atlassian.net", "two@x.com")
	g.Save()
	if got, err := lk.HostEmail("acme.atlassian.net"); err != nil || got != "two@x.com" {
		t.Fatalf("host email %q %v", got, err)
	}
}
