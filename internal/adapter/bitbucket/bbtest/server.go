// Package bbtest is an in-memory fake of the Bitbucket Cloud 2.0 API: one
// workspace, its members, repositories, pull requests and pipelines.
package bbtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Me is the account of the token the fake accepts.
const Me = "712020:me"

type Server struct {
	URL string // base for the client: <server>/2.0
	// CommentsBumpUpdated: comment writes move the PR's updated_on (Bitbucket's
	// documented behaviour; false covers the other branch).
	CommentsBumpUpdated bool
	// MergeAsync: the first merge of each PR answers 202 with a task.
	MergeAsync bool
	// AdvanceOnLog: every log request of a running pipeline advances it.
	AdvanceOnLog bool

	srv       *httptest.Server
	mu        sync.Mutex
	clock     time.Time
	members   []Member
	repos     map[string]*Repo
	order     []string
	calls     []string
	limited   int
	nearLimit bool
	failPath  string
	nextID    int
	tasks     map[string]int // merge task -> PR id
}

type Member struct{ Account, Name, Nick string }

type Repo struct {
	Slug     string
	Main     string
	Branches map[string]bool
	PRs      []*PR
	Statuses map[string][]Status // commit -> statuses
	Runs     []*Run
}

type PR struct {
	ID                                      int
	Title, Description, State               string // OPEN, MERGED, DECLINED, SUPERSEDED
	Source, Dest, SourceCommit, DestCommit  string
	Author                                  string
	Draft, CloseSource                      bool
	Reviewers                               []string
	Participants                            []Participant
	Strategies                              []string
	DefaultStrategy, MergeCommit, MergeUsed string
	Comments                                []*Comment
	Created, Updated                        time.Time
	merged                                  bool // a 202 was answered once
}

type Participant struct{ Account, Role, State string } // role REVIEWER|PARTICIPANT; state approved|changes_requested|""

type Comment struct {
	ID, Parent         int
	Account, Raw, Path string
	From, To           int
	Resolved, Deleted  bool
	Created, Updated   time.Time
}

type Status struct{ Key, State, Name, URL string }

type Run struct {
	Number                                         int
	UUID                                           string
	State, Result, Stage                           string // PENDING|IN_PROGRESS|COMPLETED; SUCCESSFUL|FAILED|…
	Trigger, Creator                               string
	TargetType, RefType, RefName, Commit, Selector string // Selector "branches:main", "custom:deploy", "pull-requests:**", "default"
	PR                                             int
	Steps                                          []*Step
	Created, Completed                             time.Time
}

type Step struct {
	UUID, Name, Image, State, Result string
	Log                              []byte
	Started, Completed               time.Time
	pending                          [][]byte // chunks Advance appends
}

func New() *Server {
	s := &Server{CommentsBumpUpdated: true, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		repos: map[string]*Repo{}, nextID: 1000, tasks: map[string]int{}}
	mux := http.NewServeMux()
	s.routes(mux)
	s.srv = httptest.NewServer(s.wrap(mux))
	s.URL = s.srv.URL + "/2.0"
	return s
}

func (s *Server) Close() { s.srv.Close() }

func (s *Server) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+strings.TrimPrefix(r.URL.RequestURI(), "/2.0"))
		limited, near, fail := s.limited > 0, s.nearLimit, s.failPath != "" && strings.Contains(r.URL.Path, s.failPath)
		if limited {
			s.limited--
		}
		s.mu.Unlock()
		if near {
			w.Header().Set("X-RateLimit-NearLimit", "true")
		}
		if user, _, ok := r.BasicAuth(); !ok || user == "" {
			bbError(w, 401, "Unauthorized", "")
			return
		}
		switch {
		case limited:
			w.Header().Set("Retry-After", "1")
			bbError(w, 429, "Rate limit for this resource has been exceeded", "")
		case fail:
			bbError(w, 400, "Injected failure", "")
		default:
			h.ServeHTTP(w, r)
		}
	})
}

// now advances the fake clock by a minute per write.
func (s *Server) now() time.Time { s.clock = s.clock.Add(time.Minute); return s.clock }

func (s *Server) id() int { s.nextID++; return s.nextID }

func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *Server) ResetCalls() { s.mu.Lock(); s.calls = nil; s.mu.Unlock() }

// RateLimit makes the next n requests answer 429 with Retry-After: 1.
func (s *Server) RateLimit(n int) { s.mu.Lock(); s.limited = n; s.mu.Unlock() }

func (s *Server) NearLimit(on bool) { s.mu.Lock(); s.nearLimit = on; s.mu.Unlock() }

// Fail makes every request whose path contains frag answer 400 ("" stops).
func (s *Server) Fail(frag string) { s.mu.Lock(); s.failPath = frag; s.mu.Unlock() }

func (s *Server) AddMember(account, name, nick string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members = append(s.members, Member{account, name, nick})
}

func (s *Server) AddRepo(slug string, branches ...string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &Repo{Slug: slug, Main: "main", Branches: map[string]bool{"main": true}, Statuses: map[string][]Status{}}
	for _, b := range branches {
		r.Branches[b] = true
	}
	s.repos[slug] = r
	s.order = append(s.order, slug)
	return r
}

func (s *Server) RemoveRepo(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repos, slug)
	for i, o := range s.order {
		if o == slug {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// AddPR stores pr with defaults filled in and returns its id.
func (s *Server) AddPR(repo string, pr PR) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[repo]
	pr.ID = len(r.PRs) + 1
	if pr.State == "" {
		pr.State = "OPEN"
	}
	if pr.Dest == "" {
		pr.Dest = "main"
	}
	if pr.Author == "" {
		pr.Author = "712020:ana"
	}
	if pr.Strategies == nil {
		pr.Strategies = []string{"merge_commit", "squash", "fast_forward"}
		pr.DefaultStrategy = "merge_commit"
	}
	if pr.SourceCommit == "" {
		pr.SourceCommit = fmt.Sprintf("c0ffee%04d", pr.ID)
	}
	if pr.DestCommit == "" {
		pr.DestCommit = "0f9e8d7c6b5a"
	}
	if pr.Created.IsZero() {
		pr.Created = s.now()
	}
	if pr.Updated.IsZero() {
		pr.Updated = pr.Created
	}
	r.Branches[pr.Source] = true
	r.PRs = append(r.PRs, &pr)
	return pr.ID
}

// PR returns the stored PR; callers hold no lock, so use it between requests only.
func (s *Server) PR(repo string, id int) *PR {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pr(repo, id)
}

func (s *Server) pr(repo string, id int) *PR {
	r := s.repos[repo]
	if r == nil || id < 1 || id > len(r.PRs) {
		return nil
	}
	return r.PRs[id-1]
}

// EditPR changes a PR as another user would, moving updated_on.
func (s *Server) EditPR(repo string, id int, f func(*PR)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pr(repo, id)
	f(p)
	p.Updated = s.now()
}

func (s *Server) AddComment(repo string, pr int, c Comment) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pr(repo, pr)
	c.ID = s.id()
	if c.Account == "" {
		c.Account = "712020:bo"
	}
	c.Created = s.now()
	c.Updated = c.Created
	p.Comments = append(p.Comments, &c)
	if s.CommentsBumpUpdated {
		p.Updated = c.Created
	}
	return c.ID
}

// SetStatus sets a commit status without touching any PR's updated_on.
func (s *Server) SetStatus(repo, commit string, st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[repo]
	for i, o := range r.Statuses[commit] {
		if o.Key == st.Key {
			r.Statuses[commit][i] = st
			return
		}
	}
	r.Statuses[commit] = append(r.Statuses[commit], st)
}

// AddRun stores a run; steps with State "" are PENDING. Returns its uuid.
func (s *Server) AddRun(repo string, run Run) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[repo]
	run.Number = len(r.Runs) + 1
	run.UUID = fmt.Sprintf("{%08d-0000-4000-8000-%012d}", run.Number, len(s.order))
	if run.State == "" {
		run.State = "COMPLETED"
	}
	if run.Trigger == "" {
		run.Trigger = "PUSH"
	}
	if run.Creator == "" {
		run.Creator = "712020:ana"
	}
	if run.TargetType == "" {
		run.TargetType, run.RefType, run.RefName, run.Selector = "pipeline_ref_target", "branch", "main", "branches:main"
	}
	if run.Commit == "" {
		run.Commit = "a1b2c3d4e5f6"
	}
	if run.Created.IsZero() {
		run.Created = s.now()
	}
	if run.State == "COMPLETED" && run.Completed.IsZero() {
		run.Completed = run.Created.Add(5 * time.Minute)
	}
	for i, st := range run.Steps {
		st.UUID = fmt.Sprintf("{%08d-0000-4000-9000-%012d}", run.Number, i+1)
		if st.State == "" {
			st.State = "PENDING"
		}
	}
	r.Runs = append(r.Runs, &run)
	return run.UUID
}

// Queue gives a step log chunks that Advance appends one at a time.
func (s *Server) Queue(repo string, number, step int, chunks ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.repos[repo].Runs[number-1].Steps[step]
	for _, c := range chunks {
		st.pending = append(st.pending, []byte(c))
	}
}

func (s *Server) Run(repo string, number int) *Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repos[repo].Runs[number-1]
}

// Advance moves a run one step forward: the running steps take their next
// queued chunk; a running step with none left completes (FAILED when its
// Result is set to FAILED beforehand, else SUCCESSFUL); then pending steps
// start: all steps sharing the first pending step's Image prefix "par:"
// start together. A run with every step completed completes.
func (s *Server) Advance(repo string, number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advance(s.repos[repo].Runs[number-1])
}

func (s *Server) advance(run *Run) {
	if run.State == "COMPLETED" {
		return
	}
	run.State, run.Stage = "IN_PROGRESS", "RUNNING"
	running := false
	for _, st := range run.Steps {
		if st.State != "IN_PROGRESS" {
			continue
		}
		if len(st.pending) > 0 {
			st.Log = append(st.Log, st.pending[0]...)
			st.pending = st.pending[1:]
			running = true
			continue
		}
		st.State, st.Completed = "COMPLETED", s.now()
		if st.Result == "" {
			st.Result = "SUCCESSFUL"
		}
	}
	if !running {
		started := false
		for _, st := range run.Steps {
			if st.State == "IN_PROGRESS" {
				started = true
			}
		}
		for i, st := range run.Steps {
			if started || st.State != "PENDING" {
				continue
			}
			st.State, st.Started = "IN_PROGRESS", s.now()
			// parallel: following steps whose Image starts with "par:" start too
			for _, nx := range run.Steps[i+1:] {
				if nx.State == "PENDING" && strings.HasPrefix(nx.Image, "par:") && strings.HasPrefix(st.Image, "par:") {
					nx.State, nx.Started = "IN_PROGRESS", st.Started
				}
			}
			started = true
		}
	}
	done := true
	result := "SUCCESSFUL"
	for _, st := range run.Steps {
		if st.State != "COMPLETED" {
			done = false
		} else if st.Result != "SUCCESSFUL" {
			result = st.Result
		}
	}
	if done {
		run.State, run.Stage, run.Result, run.Completed = "COMPLETED", "", result, s.now()
	}
}

func bbError(w http.ResponseWriter, code int, msg, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	e := map[string]any{"message": msg}
	if detail != "" {
		e["detail"] = detail
	}
	json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": e})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

// page answers a paged listing of values, following page= and pagelen=.
func (s *Server) page(w http.ResponseWriter, r *http.Request, values []any) {
	n, _ := strconv.Atoi(r.URL.Query().Get("pagelen"))
	if n <= 0 {
		n = 10
	}
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p <= 0 {
		p = 1
	}
	lo, hi := (p-1)*n, p*n
	if lo > len(values) {
		lo = len(values)
	}
	if hi > len(values) {
		hi = len(values)
	}
	out := map[string]any{"pagelen": n, "page": p, "size": len(values), "values": values[lo:hi]}
	if hi < len(values) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p+1))
		out["next"] = s.srv.URL + r.URL.Path + "?" + q.Encode()
	}
	writeJSON(w, out)
}

func (s *Server) user(account string) map[string]any {
	if account == Me {
		return map[string]any{"account_id": Me, "display_name": "Me Myself", "nickname": "me", "uuid": "{me}", "type": "user"}
	}
	for _, m := range s.members {
		if m.Account == account {
			return map[string]any{"account_id": m.Account, "display_name": m.Name, "nickname": m.Nick, "uuid": "{" + m.Nick + "}", "type": "user"}
		}
	}
	return map[string]any{"account_id": account, "display_name": account, "nickname": account, "type": "user"}
}
