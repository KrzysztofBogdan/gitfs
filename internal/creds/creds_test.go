package creds

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func testStore(t *testing.T) Store {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	return Store{Dirs: Dirs{Config: filepath.Join(home, ".config", "gfs"), Home: home}}
}

func listFile(t *testing.T, s Store) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(s.Dirs.Config, "identities"))
	return string(b)
}

func TestSetGetReplace(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.Get("me@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := s.Set("me@x.com", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("Me@X.com", "two"); err != nil {
		t.Fatal(err)
	}
	tok, src, err := s.Get("me@x.com")
	if err != nil || tok != "two" || src != "gfs" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	if got := listFile(t, s); got != "atlassian:me@x.com\n" {
		t.Fatalf("identities file %q", got)
	}
	if err := s.Set("a@x.com", ""); err == nil {
		t.Fatal("empty token must fail")
	}
}

func TestDeleteAndClear(t *testing.T) {
	s := testStore(t)
	s.Set("b@x.com", "tb")
	s.Set("a@x.com", "ta")
	if got := listFile(t, s); got != "atlassian:a@x.com\natlassian:b@x.com\n" {
		t.Fatalf("identities file %q", got)
	}
	if err := s.Delete("a@x.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("a@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete("a@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	n, err := s.Clear()
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if got := listFile(t, s); got != "" {
		t.Fatalf("identities file after clear %q", got)
	}
	if _, _, err := s.Get("b@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after clear: %v", err)
	}
}

func TestMissingEntry(t *testing.T) {
	s := testStore(t)
	s.Set("a@x.com", "ta")
	keyring.Delete("gfs", "atlassian:a@x.com") // entry removed behind gfs's back
	ids, _, err := s.Identities()
	if err != nil || len(ids) != 1 || !ids[0].Missing || ids[0].Source != "gfs" {
		t.Fatalf("got %+v %v", ids, err)
	}
	if err := s.Delete("a@x.com"); err != nil {
		t.Fatalf("delete of a missing entry: %v", err)
	}
	if got := listFile(t, s); got != "" {
		t.Fatalf("identities file %q", got)
	}
}

func TestAloginFallback(t *testing.T) {
	s := testStore(t)
	os.WriteFile(filepath.Join(s.Dirs.Home, ".alogin.json"), []byte(`[{"name":"work","email":"KB@x.com","createdAt":"x"}]`), 0o600)
	keyring.Set("alogin", "work", `{"email":"KB@x.com","token":"atok","accountId":"1"}`)
	tok, src, err := s.Get("kb@x.com")
	if err != nil || tok != "atok" || src != "alogin" {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
	ids, warns, err := s.Identities()
	if err != nil || len(warns) != 0 || len(ids) != 1 || ids[0] != (Identity{Email: "kb@x.com", Source: "alogin"}) {
		t.Fatalf("got %+v %v %v", ids, warns, err)
	}
	s.Set("kb@x.com", "gtok") // gfs entry wins and the identity is listed once
	if tok, src, _ := s.Get("kb@x.com"); tok != "gtok" || src != "gfs" {
		t.Fatalf("got %q %q", tok, src)
	}
	if ids, _, _ := s.Identities(); len(ids) != 1 || ids[0].Source != "gfs" {
		t.Fatalf("got %+v", ids)
	}
}

func TestAloginBadJSON(t *testing.T) {
	s := testStore(t)
	os.WriteFile(filepath.Join(s.Dirs.Home, ".alogin.json"), []byte(`{`), 0o600)
	if _, _, err := s.Get("kb@x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad alogin JSON must read as not found: %v", err)
	}
	if _, warns, err := s.Identities(); err != nil || len(warns) != 1 || !strings.Contains(warns[0], ".alogin.json") {
		t.Fatalf("got %v %v", warns, err)
	}
}

func TestKeyringUnavailable(t *testing.T) {
	s := testStore(t)
	keyring.MockInitWithError(errors.New("no dbus"))
	defer keyring.MockInit()
	want := "keyring unavailable (no dbus); set GFS_CONFLUENCE_TOKEN or GFS_JIRA_TOKEN instead"
	if _, _, err := s.Get("a@x.com"); err == nil || err.Error() != want {
		t.Fatalf("get: %v", err)
	}
	if err := s.Set("a@x.com", "t"); err == nil || err.Error() != want {
		t.Fatalf("set: %v", err)
	}
}

// Entries are keyring items named by a remote (ovh:eu, cloudns:sub-1),
// listed with the identities, value opaque to creds.
func TestEntries(t *testing.T) {
	s := testStore(t)
	if _, err := s.GetEntry("ovh:eu"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.SetEntry("ovh:eu", `{"appKey":"k"}`); err != nil {
		t.Fatal(err)
	}
	s.Set("me@x.com", "tok")
	if v, err := s.GetEntry("ovh:eu"); err != nil || v != `{"appKey":"k"}` {
		t.Fatal(v, err)
	}
	ids, _, _ := s.Identities()
	if len(ids) != 2 || ids[0].Email != "me@x.com" || ids[1].Entry != "ovh:eu" || ids[1].Source != "gfs" {
		t.Fatalf("%+v", ids)
	}
	if err := s.DeleteEntry("ovh:eu"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEntry("ovh:eu"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if got := listFile(t, s); got != "atlassian:me@x.com\n" {
		t.Fatalf("identities file %q", got)
	}
}
