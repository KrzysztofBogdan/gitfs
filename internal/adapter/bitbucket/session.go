package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

var (
	_ adapter.Session    = (*session)(nil)
	_ adapter.Reporter   = (*session)(nil)
	_ adapter.Cacher     = (*session)(nil)
	_ adapter.Identified = (*session)(nil)
)

type session struct {
	c        *atlassian.Client
	t        target
	me       string // account_id of the token's user, read once
	report   func(adapter.Progress)
	cacheDir string
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	// listed PRs and runs by id, so Fetch right after List skips a request
	prs  map[string]apiPR
	runs map[string]apiPipeline
	// merge strategies of fetched PRs by id: Check and Available read them
	strategies map[string]strategies
	people     *people
}

type strategies struct {
	allowed []string
	def     string
}

func newSession(t target) *session {
	s := &session{t: t, report: func(adapter.Progress) {}, now: time.Now, sleep: httpx.SleepCtx,
		prs: map[string]apiPR{}, runs: map[string]apiPipeline{}, strategies: map[string]strategies{}}
	s.c = atlassian.New(atlassian.Target{Base: t.base, Email: t.email, Token: t.token}, "Bitbucket")
	s.c.LoginHint = "gfs auth login bitbucket://" + host + "/" + t.sel.workspace
	s.c.OnWait = func(msg string) { s.report(adapter.Progress{Phase: "wait", Item: msg}) }
	return s
}

func (s *session) SetProgress(f func(adapter.Progress)) { s.report = f }
func (s *session) UseCache(dir string)                  { s.cacheDir = dir }
func (s *session) Identity() string                     { return s.t.email }
func (s *session) Close() error                         { return nil }

func (s *session) repoPath(repo string) string {
	return "/repositories/" + url.PathEscape(s.t.sel.workspace) + "/" + url.PathEscape(repo)
}

func (s *session) get(ctx context.Context, path string, q url.Values, out any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return s.c.Do(ctx, "GET", path, nil, out)
}

// pages calls each for every value of a paged listing until it returns false.
func pages[T any](ctx context.Context, s *session, path string, q url.Values, each func(T) (bool, error)) error {
	next := path
	if len(q) > 0 {
		next += "?" + q.Encode()
	}
	for next != "" {
		var p page[T]
		if err := s.c.Do(ctx, "GET", next, nil, &p); err != nil {
			return err
		}
		for _, v := range p.Values {
			more, err := each(v)
			if err != nil || !more {
				return err
			}
		}
		next = strings.TrimPrefix(p.Next, s.t.base)
		if next == p.Next && next != "" {
			return fmt.Errorf("bitbucket: next page %q is not under %s", p.Next, s.t.base)
		}
	}
	return nil
}

func (s *session) whoami(ctx context.Context) (string, error) {
	if s.me == "" {
		var u apiUser
		if err := s.get(ctx, "/user", nil, &u); err != nil {
			return "", err
		}
		s.me = u.AccountID
	}
	return s.me, nil
}

// cursor is per repository: when its PRs were last listed, the highest run
// number seen, and the runs not completed then (bitbucket spec §6.4).
type cursor map[string]*repoCursor

type repoCursor struct {
	PRs     time.Time      `json:"prs"`
	Run     int            `json:"run"`
	Running map[string]int `json:"running,omitempty"` // uuid -> build number
}

func (s *session) repos(ctx context.Context) ([]string, error) {
	var out []string
	err := pages(ctx, s, "/repositories/"+url.PathEscape(s.t.sel.workspace), url.Values{"pagelen": {"100"}}, func(r struct{ Slug string }) (bool, error) {
		if s.t.sel.wants(r.Slug) {
			out = append(out, r.Slug)
		}
		return true, nil
	})
	return out, err
}

// List returns stubs: the engine fetches each resource it lacks or whose
// version moved, so a clone that stops resumes with gfs pull (bitbucket spec
// §6). Open PRs and unfinished runs have no version and are always fetched.
func (s *session) List(ctx context.Context, raw string) (adapter.Listing, error) {
	defer s.report(adapter.Progress{Phase: "done"})
	cur := cursor{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			cur = cursor{}
		}
	}
	repos, err := s.repos(ctx)
	if err != nil {
		return adapter.Listing{}, err
	}
	var l adapter.Listing
	next := cursor{}
	selected := map[string]bool{}
	for i, repo := range repos {
		selected[repo] = true
		s.report(adapter.Progress{Phase: "pages", Done: i, Total: len(repos), Item: repo})
		started := s.now().UTC()
		rc := cur[repo]
		if rc == nil {
			l.FullDirs = append(l.FullDirs, repo)
		}
		res, nrc, err := s.listRepo(ctx, repo, rc)
		if err != nil {
			return adapter.Listing{}, fmt.Errorf("%s: %w", repo, err)
		}
		nrc.PRs = started
		next[repo] = nrc
		l.Resources = append(l.Resources, res...)
	}
	for repo := range cur {
		if !selected[repo] {
			l.FullDirs = append(l.FullDirs, repo) // left the selection: listed with nothing in it
		}
	}
	b, _ := json.Marshal(next)
	l.Cursor = string(b)
	return l, nil
}

func (s *session) listRepo(ctx context.Context, repo string, rc *repoCursor) ([]adapter.Resource, *repoCursor, error) {
	prs, err := s.listPRs(ctx, repo, rc)
	if err != nil {
		return nil, nil, err
	}
	runs, nrc, err := s.listRuns(ctx, repo, rc)
	if err != nil {
		return nil, nil, err
	}
	return append(prs, runs...), nrc, nil
}

func (s *session) prStub(repo string, p apiPR) adapter.Resource {
	id := prID(repo, p.ID)
	s.prs[id] = p
	v := stamp(p.UpdatedOn)
	if p.State == "OPEN" {
		v = "" // statuses and comments may change without updated_on: always fetched
	}
	return adapter.Resource{ID: id, Path: prPath(repo, p.ID, p.Title), Version: v}
}

func (s *session) listPRs(ctx context.Context, repo string, rc *repoCursor) ([]adapter.Resource, error) {
	var out []adapter.Resource
	seen := map[int]bool{}
	path := s.repoPath(repo) + "/pullrequests"
	all := url.Values{"state": {"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}, "pagelen": {"50"}}
	if rc == nil {
		q := all
		q.Set("sort", "-updated_on")
		if since := sinceTime(s.t.sel.since, s.now()); !since.IsZero() {
			q.Set("q", fmt.Sprintf(`updated_on >= "%s"`, since.UTC().Format(time.RFC3339)))
		}
		err := pages(ctx, s, path, q, func(p apiPR) (bool, error) {
			out = append(out, s.prStub(repo, p))
			return s.t.sel.limit == 0 || len(out) < s.t.sel.limit, nil
		})
		return out, err
	}
	err := pages(ctx, s, path, url.Values{"state": {"OPEN"}, "pagelen": {"50"}}, func(p apiPR) (bool, error) {
		seen[p.ID] = true
		out = append(out, s.prStub(repo, p))
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	q := all
	q.Set("sort", "updated_on")
	q.Set("q", fmt.Sprintf(`updated_on > "%s"`, rc.PRs.Add(-10*time.Minute).UTC().Format(time.RFC3339)))
	err = pages(ctx, s, path, q, func(p apiPR) (bool, error) {
		if !seen[p.ID] {
			out = append(out, s.prStub(repo, p))
		}
		return true, nil
	})
	return out, err
}

func (s *session) runStub(repo string, p apiPipeline) adapter.Resource {
	id := runID(repo, p.UUID)
	s.runs[id] = p
	v := ""
	if p.State.Name == "COMPLETED" {
		v = runVersion(runInput{Run: p})
	}
	return adapter.Resource{ID: id, Path: runPath(repo, p.BuildNumber), Version: v}
}

func (s *session) listRuns(ctx context.Context, repo string, rc *repoCursor) ([]adapter.Resource, *repoCursor, error) {
	nrc := &repoCursor{Running: map[string]int{}}
	if rc != nil {
		nrc.Run = rc.Run
	}
	var out []adapter.Resource
	since := sinceTime(s.t.sel.since, s.now())
	full := rc == nil
	q := url.Values{"sort": {"-created_on"}, "pagelen": {"100"}}
	err := pages(ctx, s, s.repoPath(repo)+"/pipelines/", q, func(p apiPipeline) (bool, error) {
		if !full && p.BuildNumber <= rc.Run {
			return false, nil
		}
		if full && (!since.IsZero() && parseStamp(p.CreatedOn).Before(since) || s.t.sel.limit > 0 && len(out) >= s.t.sel.limit) {
			return false, nil
		}
		out = append(out, s.runStub(repo, p))
		if p.BuildNumber > nrc.Run {
			nrc.Run = p.BuildNumber
		}
		if p.State.Name != "COMPLETED" {
			nrc.Running[p.UUID] = p.BuildNumber
		}
		return true, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if rc != nil {
		for uuid := range rc.Running {
			var p apiPipeline
			err := s.get(ctx, s.repoPath(repo)+"/pipelines/"+url.PathEscape(uuid), nil, &p)
			if errors.Is(err, adapter.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			out = append(out, s.runStub(repo, p))
			if p.State.Name != "COMPLETED" {
				nrc.Running[p.UUID] = p.BuildNumber
			}
		}
	}
	return out, nrc, nil
}

func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if repo, n, ok := parsePRID(id); ok {
		return s.fetchPR(ctx, repo, n)
	}
	if repo, uuid, ok := parseRunID(id); ok {
		return s.fetchRun(ctx, repo, uuid)
	}
	return nil, fmt.Errorf("%s: %w", id, adapter.ErrNotFound)
}

func (s *session) outside(repo, id string) error {
	if !s.t.sel.wants(repo) {
		return fmt.Errorf("%s: %w (outside the selection)", id, adapter.ErrNotFound)
	}
	return nil
}

func (s *session) fetchPR(ctx context.Context, repo string, n int) (*adapter.Resource, error) {
	id := prID(repo, n)
	if err := s.outside(repo, id); err != nil {
		return nil, err
	}
	me, err := s.whoami(ctx)
	if err != nil {
		return nil, err
	}
	in := prInput{Repo: repo, Me: me}
	var ok bool
	if in.PR, ok = s.prs[id]; ok {
		delete(s.prs, id) // a later Fetch (after a write) reads it fresh
	} else if err := s.get(ctx, s.repoPath(repo)+"/pullrequests/"+strconv.Itoa(n), nil, &in.PR); err != nil {
		return nil, err
	}
	base := s.repoPath(repo) + "/pullrequests/" + strconv.Itoa(n)
	if err := pages(ctx, s, base+"/comments", url.Values{"pagelen": {"100"}}, func(c apiComment) (bool, error) {
		in.Comments = append(in.Comments, c)
		return true, nil
	}); err != nil {
		return nil, err
	}
	if err := pages(ctx, s, base+"/statuses", url.Values{"pagelen": {"100"}}, func(st apiStatus) (bool, error) {
		in.Statuses = append(in.Statuses, st)
		return true, nil
	}); err != nil {
		return nil, err
	}
	d := in.PR.Destination.Branch
	s.strategies[id] = strategies{allowed: d.MergeStrategies, def: d.DefaultMergeStrategy}
	return &adapter.Resource{ID: id, Path: prPath(repo, n, in.PR.Title), Version: stamp(in.PR.UpdatedOn),
		By: in.PR.Author.DisplayName, At: stamp(in.PR.UpdatedOn), Root: prRoot(in)}, nil
}

func (s *session) fetchRun(ctx context.Context, repo, uuid string) (*adapter.Resource, error) {
	id := runID(repo, uuid)
	if err := s.outside(repo, id); err != nil {
		return nil, err
	}
	in := runInput{Repo: repo, Limit: s.t.sel.logLimit, Logs: map[string]stepLog{}}
	var ok bool
	if in.Run, ok = s.runs[id]; ok && in.Run.State.Name == "COMPLETED" {
		delete(s.runs, id)
	} else if err := s.get(ctx, s.repoPath(repo)+"/pipelines/{"+url.PathEscape(uuid)+"}", nil, &in.Run); err != nil {
		return nil, err
	}
	steps, err := s.steps(ctx, repo, in.Run.UUID)
	if err != nil {
		return nil, err
	}
	in.Steps = steps
	for _, st := range steps {
		if st.State.Name == "PENDING" {
			continue
		}
		var b strings.Builder
		total, err := s.c.Range(ctx, s.logPath(repo, in.Run.UUID, st.UUID), 0, in.Limit, &b)
		if err != nil {
			return nil, err
		}
		if total < 0 && b.Len() == 0 && st.State.Name == "COMPLETED" {
			continue // a completed step without a log
		}
		if st.State.Name == "COMPLETED" && total < 0 && int64(b.Len()) <= in.Limit {
			total = int64(b.Len())
		}
		in.Logs[st.UUID] = stepLog{Data: []byte(b.String()), Total: total}
	}
	return &adapter.Resource{ID: id, Path: runPath(repo, in.Run.BuildNumber), Version: runVersion(in), Root: runRoot(in)}, nil
}

func (s *session) steps(ctx context.Context, repo, uuid string) ([]apiStep, error) {
	var out []apiStep
	err := pages(ctx, s, s.repoPath(repo)+"/pipelines/"+url.PathEscape(uuid)+"/steps/", url.Values{"pagelen": {"100"}}, func(st apiStep) (bool, error) {
		out = append(out, st)
		return true, nil
	})
	return out, err
}

func (s *session) logPath(repo, run, step string) string {
	return s.repoPath(repo) + "/pipelines/" + url.PathEscape(run) + "/steps/" + url.PathEscape(step) + "/log"
}

// Download writes a step's whole log (bitbucket spec §8.1).
func (s *session) Download(ctx context.Context, resourceID, step string, w io.Writer) (adapter.AttachmentInfo, error) {
	repo, uuid, ok := parseRunID(resourceID)
	if !ok {
		return adapter.AttachmentInfo{}, fmt.Errorf("%s has no logs", resourceID)
	}
	n, err := s.c.Download(ctx, s.logPath(repo, "{"+uuid+"}", "{"+step+"}"), w)
	return adapter.AttachmentInfo{Version: "-", Size: n}, err
}
