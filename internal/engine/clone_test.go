package engine

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

type identified struct {
	adapter.Session
	id string
}

func (s identified) Identity() string { return s.id }

func TestClonePinsIdentity(t *testing.T) {
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, identified{Session: sess, id: "me@x.com"}, "fake://x", filepath.Join(t.TempDir(), "wt"), &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := env.Tree.LoadConfig()
	if got := cfg.Get("remote", "email"); got != "me@x.com" {
		t.Fatalf("remote email %q", got)
	}

	plain, _, _ := cloned(t) // the fake session has no identity
	cfg, _ = plain.Tree.LoadConfig()
	if got := cfg.Get("remote", "email"); got != "" {
		t.Fatalf("remote email %q", got)
	}
}
