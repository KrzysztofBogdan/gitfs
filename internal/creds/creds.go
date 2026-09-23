// Package creds keeps service tokens in the system keyring, and the global
// per-host defaults (credentials spec).
package creds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const (
	Realm   = "atlassian"
	service = "gfs"
)

var ErrNotFound = errors.New("no stored token")

// Dirs locates gfs's config dir and the home dir (for ~/.alogin.json).
type Dirs struct {
	Config string // $XDG_CONFIG_HOME/gfs
	Home   string
}

func DefaultDirs() Dirs {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return Dirs{Config: filepath.Join(cfg, "gfs"), Home: home}
}

type Store struct{ Dirs Dirs }

type Identity struct {
	Email   string
	Source  string // "gfs" or "alogin"
	Missing bool   // listed by gfs, but the keyring entry is gone
}

func key(email string) string { return Realm + ":" + strings.ToLower(email) }

func unavailable(err error) error {
	return fmt.Errorf("keyring unavailable (%v); set GFS_CONFLUENCE_TOKEN instead", err)
}

func (s Store) listPath() string { return filepath.Join(s.Dirs.Config, "identities") }

func (s Store) readList() ([]string, error) {
	data, err := os.ReadFile(s.listPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keys = append(keys, l)
		}
	}
	return keys, nil
}

func (s Store) writeList(keys []string) error {
	sort.Strings(keys)
	keys = slices.Compact(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\n")
	}
	return workdir.WriteAtomic(s.listPath(), []byte(b.String()))
}

// Get returns the token for email and where it came from: "gfs" or "alogin".
func (s Store) Get(email string) (token, source string, err error) {
	tok, err := keyring.Get(service, key(email))
	if err == nil {
		return tok, "gfs", nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return "", "", unavailable(err)
	}
	if tok, ok := aloginToken(s.Dirs.Home, email); ok {
		return tok, "alogin", nil
	}
	return "", "", ErrNotFound
}

// Set stores or replaces the token for email.
func (s Store) Set(email, token string) error {
	if token == "" {
		return errors.New("empty token")
	}
	if err := keyring.Set(service, key(email), token); err != nil {
		return unavailable(err)
	}
	keys, err := s.readList()
	if err != nil {
		return err
	}
	return s.writeList(append(keys, key(email)))
}

// Delete removes gfs's entry for email; ErrNotFound when gfs never stored it.
func (s Store) Delete(email string) error {
	keys, err := s.readList()
	if err != nil {
		return err
	}
	k := key(email)
	if err := keyring.Delete(service, k); err != nil {
		if !errors.Is(err, keyring.ErrNotFound) {
			return unavailable(err)
		}
		if !slices.Contains(keys, k) {
			return ErrNotFound
		}
	}
	return s.writeList(slices.DeleteFunc(keys, func(x string) bool { return x == k }))
}

// Clear deletes every gfs entry and returns how many identities were listed.
func (s Store) Clear() (int, error) {
	keys, err := s.readList()
	if err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := keyring.Delete(service, k); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return 0, unavailable(err)
		}
	}
	return len(keys), s.writeList(nil)
}

// Identities lists gfs's identities, then alogin-only ones. The warnings
// report an unreadable ~/.alogin.json.
func (s Store) Identities() ([]Identity, []string, error) {
	keys, err := s.readList()
	if err != nil {
		return nil, nil, err
	}
	var ids []Identity
	seen := map[string]bool{}
	for _, k := range keys {
		email, ok := strings.CutPrefix(k, Realm+":")
		if !ok {
			continue
		}
		_, gerr := keyring.Get(service, k)
		if gerr != nil && !errors.Is(gerr, keyring.ErrNotFound) {
			return nil, nil, unavailable(gerr)
		}
		ids = append(ids, Identity{Email: email, Source: "gfs", Missing: gerr != nil})
		seen[email] = true
	}
	var warnings []string
	profiles, perr := aloginProfiles(s.Dirs.Home)
	if perr != nil {
		warnings = append(warnings, perr.Error())
	}
	var extra []string
	for _, p := range profiles {
		if e := strings.ToLower(p.Email); e != "" && !seen[e] {
			seen[e] = true
			extra = append(extra, e)
		}
	}
	sort.Strings(extra)
	for _, e := range extra {
		ids = append(ids, Identity{Email: e, Source: "alogin"})
	}
	return ids, warnings, nil
}
