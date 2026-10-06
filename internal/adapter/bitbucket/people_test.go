package bitbucket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/bitbucket/bbtest"
)

func TestResolveUser(t *testing.T) {
	srv := bbtest.New()
	defer srv.Close()
	srv.AddMember("712020:bo", "Bo Smith", "bo")
	srv.AddMember("712020:bl", "Bo Li", "boli")
	srv.AddMember("712020:b2", "Bo Li", "boli2")
	s := testSession(t, srv, "bitbucket://bitbucket.org/acme")
	for in, want := range map[string]string{"712020:bo": "712020:bo", "bo": "712020:bo", "Bo Smith": "712020:bo", "boli": "712020:bl"} {
		m, err := s.resolveUser(bg, in)
		if err != nil || m.Account != want {
			t.Fatal(in, m, err)
		}
	}
	if _, err := s.resolveUser(bg, "Bo Li"); err == nil || !strings.Contains(err.Error(), "matches 2: Bo Li (712020:bl), Bo Li (712020:b2)") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.cacheDir, "bitbucket", "people.json")); err != nil {
		t.Fatal("members are cached", err)
	}
	// a new session reads the cache; an unknown name refreshes it once
	cache := s.cacheDir
	srv.AddMember("712020:cy", "Cy Lee", "cy")
	s = testSession(t, srv, "bitbucket://bitbucket.org/acme")
	s.UseCache(cache)
	srv.ResetCalls()
	if _, err := s.resolveUser(bg, "bo"); err != nil || len(srv.Calls()) != 0 {
		t.Fatal(err, srv.Calls())
	}
	if m, err := s.resolveUser(bg, "Cy Lee"); err != nil || m.Account != "712020:cy" || len(srv.Calls()) != 1 {
		t.Fatal(m, err, srv.Calls())
	}
	if _, err := s.resolveUser(bg, "Nobody"); err == nil || !strings.Contains(err.Error(), "no workspace member matches") || len(srv.Calls()) != 1 {
		t.Fatal(err, srv.Calls())
	}
}
