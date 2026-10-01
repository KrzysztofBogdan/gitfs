package ovh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh/ovhtest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// script answers prompts in order; secrets come from the same list.
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
	return adapter.LoginIO{Out: out, ReadLine: next, ReadSecret: next, OpenURL: func(u string) { out.WriteString("open " + u + "\n") }, Flags: flags}
}

func loginEnv(t *testing.T, srv *ovhtest.Server) map[string]string {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	old := loginSleep
	loginSleep = func(ctx context.Context, d time.Duration) error { return nil }
	t.Cleanup(func() { loginSleep = old })
	return map[string]string{"base": srv.URL + "/1.0"}
}

func stored(t *testing.T) Creds {
	t.Helper()
	v, err := creds.Store{Dirs: creds.DefaultDirs()}.GetEntry("ovh:eu")
	if err != nil {
		t.Fatal(err)
	}
	var c Creds
	json.Unmarshal([]byte(v), &c)
	return c
}

func TestLoginConsumerKey(t *testing.T) {
	srv := site(t)
	flags := loginEnv(t, srv)
	polls := 0
	loginSleep = func(ctx context.Context, d time.Duration) error {
		if polls++; polls == 2 {
			srv.Validate("ck2") // ck (site) is the first consumer
		}
		return nil
	}
	var out bytes.Buffer
	err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags, srv.AppKey, srv.AppSecret))
	if err != nil {
		t.Fatal(err, out.String())
	}
	mustHave(t, out.String(), "https://eu.api.ovh.com/createApp", "Application key", "Application secret",
		"GET    /domain/zone\n", "DELETE /domain/zone/*", "open https://eu.api.ovh.com/auth/?credentialToken=tok-ck2",
		"verified: 2 zones visible on ovh://eu", "stored in keyring: gfs ovh:eu")
	if strings.Contains(out.String(), srv.AppSecret+"\n") && !strings.Contains(out.String(), "Application secret") {
		t.Fatal("secret echoed")
	}
	if c := stored(t); c.ConsumerKey != "ck2" || c.AppKey != srv.AppKey {
		t.Fatalf("%+v", c)
	}
	cons, _ := srv.Consumer("ck2")
	if len(cons.Rules) != 5 || cons.Rules[0] != (ovhtest.Rule{Method: "GET", Path: "/domain/zone"}) {
		t.Fatalf("%+v", cons.Rules)
	}
	// again: the stored application is reused, only a new consumer key is asked for
	out.Reset()
	polls = 0
	loginSleep = func(ctx context.Context, d time.Duration) error { srv.Validate("ck3"); return nil }
	if err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags)); err != nil {
		t.Fatal(err, out.String())
	}
	if strings.Contains(out.String(), "createApp") || stored(t).ConsumerKey != "ck3" {
		t.Fatal(out.String())
	}
}

func TestLoginRefused(t *testing.T) {
	srv := site(t)
	flags := loginEnv(t, srv)
	loginSleep = func(ctx context.Context, d time.Duration) error { srv.Refuse("ck2"); return nil }
	var out bytes.Buffer
	err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags, srv.AppKey, srv.AppSecret))
	var refused *adapter.LoginRefused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "refused") {
		t.Fatal(err)
	}
	if _, err := (creds.Store{Dirs: creds.DefaultDirs()}).GetEntry("ovh:eu"); !errors.Is(err, creds.ErrNotFound) {
		t.Fatal("nothing may be stored")
	}
}

func TestLoginTimeout(t *testing.T) {
	srv := site(t)
	flags := loginEnv(t, srv)
	old := loginTimeout
	loginTimeout = 3 * time.Second
	t.Cleanup(func() { loginTimeout = old })
	var out bytes.Buffer
	err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags, srv.AppKey, srv.AppSecret))
	if err == nil || !strings.Contains(err.Error(), "not approved within") {
		t.Fatal(err)
	}
}

// --paste takes existing keys and warns when they reach beyond DNS zones.
func TestLoginPaste(t *testing.T) {
	srv := site(t)
	flags := loginEnv(t, srv)
	flags["paste"] = "true"
	srv.AddConsumer("wide", ovhtest.Rule{Method: "GET", Path: "/*"}, ovhtest.Rule{Method: "POST", Path: "/*"})
	var out bytes.Buffer
	if err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags, srv.AppKey, srv.AppSecret, "wide")); err != nil {
		t.Fatal(err, out.String())
	}
	mustHave(t, out.String(), "warning: this key reaches beyond DNS zones: GET /*, POST /*", "stored in keyring: gfs ovh:eu")
	out.Reset()
	if err := (&Adapter{}).Login(bg, mustURL(t, "ovh://eu"), script(&out, flags, srv.AppKey, "wrong", "wide")); err == nil {
		t.Fatal("a bad secret must fail")
	}
}

func mustHave(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}
