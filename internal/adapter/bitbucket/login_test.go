package bitbucket

import (
	"bytes"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func script(out *bytes.Buffer, flags map[string]string, answers ...string) adapter.LoginIO {
	next := func(prompt string) (string, error) {
		out.WriteString(prompt + "\n")
		if len(answers) == 0 {
			return "", errors.New("no more answers")
		}
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	return adapter.LoginIO{Out: out, ReadLine: next, ReadSecret: next, OpenURL: func(string) {}, Flags: flags}
}

func TestLogin(t *testing.T) {
	srv := bbtest.New()
	defer srv.Close()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	atlassian.GitEmail = func() string { return "me@x.com" }
	store := creds.Store{Dirs: creds.DefaultDirs()}
	store.Set("me@x.com", "jira-token") // the Jira/Confluence token stays as it is
	u, _ := url.Parse("bitbucket://bitbucket.org/acme")
	var out bytes.Buffer
	if err := (&Adapter{}).Login(bg, u, script(&out, map[string]string{"base": srv.URL}, "", "scoped")); err != nil {
		t.Fatal(err, out.String())
	}
	for _, w := range []string{"Create API token with scopes", "write:pullrequest:bitbucket", "Email [me@x.com]:",
		"verified: Me Myself on bitbucket.org, workspace acme", "stored in keyring: bitbucket:me@x.com"} {
		if !strings.Contains(out.String(), w) {
			t.Fatalf("lacks %q:\n%s", w, out.String())
		}
	}
	if v, err := store.GetEntry("bitbucket:me@x.com"); err != nil || v != "scoped" {
		t.Fatal(v, err)
	}
	if tok, _, err := store.Get("me@x.com"); err != nil || tok != "jira-token" {
		t.Fatal("the Jira token is untouched:", tok, err)
	}
	u, _ = url.Parse("bitbucket://bitbucket.org/nope")
	srv.Fail("/repositories/nope")
	err := (&Adapter{}).Login(bg, u, script(&out, map[string]string{"base": srv.URL}, "me@x.com", "scoped"))
	var refused *adapter.LoginRefused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "workspace nope") {
		t.Fatal(err)
	}
}
