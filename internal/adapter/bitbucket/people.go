package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// member is a workspace member as reviewers are resolved against (bitbucket spec §5.4).
type member struct{ Account, Name, Nick string }

type people struct {
	list  []member
	fresh bool // read from Bitbucket in this run
}

func (s *session) peoplePath() string {
	if s.cacheDir == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, "bitbucket", "people.json")
}

func (s *session) loadPeople(ctx context.Context, refresh bool) error {
	if s.people != nil && (!refresh || s.people.fresh) {
		return nil
	}
	if !refresh {
		if b, err := os.ReadFile(s.peoplePath()); err == nil {
			p := &people{}
			if json.Unmarshal(b, &p.list) == nil {
				s.people = p
				return nil
			}
		}
	}
	p := &people{fresh: true}
	err := pages(ctx, s, "/workspaces/"+url.PathEscape(s.t.sel.workspace)+"/members", url.Values{"pagelen": {"100"}},
		func(m struct{ User apiUser }) (bool, error) {
			p.list = append(p.list, member{Account: m.User.AccountID, Name: m.User.DisplayName, Nick: m.User.Nickname})
			return true, nil
		})
	if err != nil {
		return fmt.Errorf("workspace members: %w", err)
	}
	s.people = p
	if path := s.peoplePath(); path != "" {
		if b, err := json.MarshalIndent(p.list, "", " "); err == nil && os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			os.WriteFile(path, b, 0o600)
		}
	}
	return nil
}

// match finds text among members: account_id, then a unique nickname, then
// a unique display name.
func (p *people) match(text string) ([]member, bool) {
	for _, m := range p.list {
		if m.Account == text {
			return []member{m}, true
		}
	}
	for _, field := range []func(member) string{func(m member) string { return m.Nick }, func(m member) string { return m.Name }} {
		var hits []member
		for _, m := range p.list {
			if field(m) == text {
				hits = append(hits, m)
			}
		}
		if len(hits) > 0 {
			return hits, len(hits) == 1
		}
	}
	return nil, false
}

// resolveUser is the member a reviewer's text names; an unknown name
// refreshes the member list once per run.
func (s *session) resolveUser(ctx context.Context, text string) (member, error) {
	text = strings.TrimSpace(text)
	if err := s.loadPeople(ctx, false); err != nil {
		return member{}, err
	}
	hits, ok := s.people.match(text)
	if len(hits) == 0 && !s.people.fresh {
		if err := s.loadPeople(ctx, true); err != nil {
			return member{}, err
		}
		hits, ok = s.people.match(text)
	}
	switch {
	case ok:
		return hits[0], nil
	case len(hits) == 0:
		return member{}, fmt.Errorf("reviewer %q: no workspace member matches (write a display name, nickname or account_id)", text)
	}
	var cands []string
	for _, m := range hits {
		cands = append(cands, fmt.Sprintf("%s (%s)", m.Name, m.Account))
	}
	return member{}, fmt.Errorf("reviewer %q matches %d: %s; write the account_id", text, len(hits), strings.Join(cands, ", "))
}
