package cloudns

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
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
	srv := site(t)
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	flags := map[string]string{"base": srv.URL}
	var out bytes.Buffer
	if err := (&Adapter{}).Login(bg, mustURL(t, "cloudns://sub-95884"), script(&out, flags, srv.Password)); err != nil {
		t.Fatal(err, out.String())
	}
	for _, w := range []string{"https://www.cloudns.net/api-settings/", "sub-user", "Password for sub-auth-id 95884:",
		"verified: 2 zones visible", "stored in keyring: gfs cloudns:sub-95884"} {
		if !strings.Contains(out.String(), w) {
			t.Fatalf("lacks %q:\n%s", w, out.String())
		}
	}
	v, err := creds.Store{Dirs: creds.DefaultDirs()}.GetEntry("cloudns:sub-95884")
	if err != nil || v != `{"password":"pw"}` {
		t.Fatal(v, err)
	}
	out.Reset()
	err = (&Adapter{}).Login(bg, mustURL(t, "cloudns://1234"), script(&out, flags, "wrong"))
	var refused *adapter.LoginRefused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "Invalid authentication") {
		t.Fatal(err)
	}
}
