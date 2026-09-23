package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// authEnv isolates the keyring, HOME and XDG_CONFIG_HOME, and chdirs to the temp home.
func authEnv(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GFS_CONFLUENCE_EMAIL", "")
	t.Setenv("GFS_CONFLUENCE_TOKEN", "")
	t.Chdir(home)
	return home
}

func gfsIn(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String(), exitCode(err)
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestAuthSetListRm(t *testing.T) {
	authEnv(t)
	out, code := gfsIn(t, "tok1\n", "auth", "set", "Me@X.com")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "stored: atlassian:me@x.com")
	gfsIn(t, "tok2\n", "auth", "set", "me@x.com")
	tok, src, err := creds.Store{Dirs: creds.DefaultDirs()}.Get("me@x.com")
	if err != nil || tok != "tok2" || src != "gfs" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "atlassian:me@x.com", "gfs")
	if strings.Contains(out, "tok2") {
		t.Fatalf("list printed the token:\n%s", out)
	}
	out, code = gfsIn(t, "", "auth", "rm", "me@x.com")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "removed: atlassian:me@x.com")
	out, code = gfsIn(t, "", "auth", "rm", "me@x.com")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "no stored token for me@x.com")
	if out, code = gfsIn(t, "\n", "auth", "set", "a@x.com"); code != 2 {
		t.Fatalf("empty token: exit %d\n%s", code, out)
	}
}

func TestAuthSetHost(t *testing.T) {
	authEnv(t)
	srv := cftest.New()
	defer srv.Close()
	srv.Accounts = map[string]string{"me@x.com": "good"}
	out, code := gfsIn(t, "bad\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "verify on acme.atlassian.net")
	if _, _, err := (creds.Store{Dirs: creds.DefaultDirs()}).Get("me@x.com"); !errors.Is(err, creds.ErrNotFound) {
		t.Fatalf("a failed verify must store nothing: %v", err)
	}
	out, code = gfsIn(t, "good\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL)
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "verified: Me on acme.atlassian.net", "stored: atlassian:me@x.com", "default for acme.atlassian.net: me@x.com")
	g, _ := creds.LoadGlobal(creds.DefaultDirs())
	if got := g.HostEmail("acme.atlassian.net"); got != "me@x.com" {
		t.Fatalf("host default %q", got)
	}
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "default for acme.atlassian.net")
	out, _ = gfsIn(t, "", "auth", "rm", "me@x.com")
	mustContain(t, out, "note: still the default for acme.atlassian.net in "+g.Path())
}

func TestAuthClear(t *testing.T) {
	authEnv(t)
	gfsIn(t, "ta\n", "auth", "set", "a@x.com")
	gfsIn(t, "tb\n", "auth", "set", "b@x.com")
	if out, code := gfsIn(t, "", "auth", "clear"); code != 2 {
		t.Fatalf("clear without a terminal and without --yes: exit %d\n%s", code, out)
	}
	out, code := gfsIn(t, "", "auth", "clear", "--yes")
	if code != 0 {
		t.Fatal(out)
	}
	mustContain(t, out, "deleted 2 stored tokens")
	out, _ = gfsIn(t, "", "auth", "list")
	mustContain(t, out, "no stored tokens")
}

func TestAuthAloginOnly(t *testing.T) {
	home := authEnv(t)
	os.WriteFile(filepath.Join(home, ".alogin.json"), []byte(`[{"name":"work","email":"kb@x.com"}]`), 0o600)
	keyring.Set("alogin", "work", `{"email":"kb@x.com","token":"atok"}`)
	out, _ := gfsIn(t, "", "auth", "list")
	mustContain(t, out, "atlassian:kb@x.com", "alogin")
	out, code := gfsIn(t, "", "auth", "rm", "kb@x.com")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "kb@x.com is stored by alogin, not gfs; nothing removed")
}
