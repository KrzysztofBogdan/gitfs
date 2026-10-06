package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var (
	errReadOnly   = errors.New("pipeline runs are read-only (use gfs tail to watch one)")
	errNoDelete   = errors.New("pull requests cannot be deleted; set <state>declined</state>")
	errSkipped    = errors.New("skipped: an earlier action failed")
	putGroups     = []string{"title", "description", "destination", "reviewer", "close-source-branch", "draft"}
	serviceGroups = map[string]string{"author": "the PR's author", "participant": "who took part", "merge-commit": "the merge commit", "build": "commit statuses"}
)

const mergePoll, mergeWait = 2 * time.Second, 5 * time.Minute

func results(acts []adapter.Action) []adapter.Result {
	out := make([]adapter.Result, len(acts))
	for i, a := range acts {
		out[i].Action = a
	}
	return out
}

func setErr(r *adapter.Result, err error) {
	r.Err, r.Code = err, atlassian.Code(err)
}

// isRun reports whether a request concerns a pipeline file.
func isRun(req adapter.ApplyRequest) bool {
	for _, r := range []*adapter.Resource{req.Local, req.Base} {
		if r == nil {
			continue
		}
		if _, _, ok := parseRunID(r.ID); ok {
			return true
		}
		if parts := strings.Split(r.Path, "/"); len(parts) >= 2 && parts[1] == runDir {
			return true
		}
		if r.Root != nil && r.Root.Name == runSchema.Root {
			return true
		}
	}
	return false
}

// plan is one file's actions checked and resolved: what Check reports and
// Apply sends (bitbucket spec §7).
type plan struct {
	repo      string
	n         int // 0 for a new PR
	errs      []error
	reviewers []string // account_ids when the reviewer group is sent
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	p := s.plan(ctx, req)
	for i := range out {
		setErr(&out[i], p.errs[i])
	}
	return out
}

// plan checks every action against the files and the remote, without writing.
func (s *session) plan(ctx context.Context, req adapter.ApplyRequest) *plan {
	p := &plan{errs: make([]error, len(req.Actions))}
	all := func(err error) *plan {
		for i := range p.errs {
			p.errs[i] = err
		}
		return p
	}
	if isRun(req) {
		return all(errReadOnly)
	}
	var local, base *xmltree.Node
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
		var ok bool
		if p.repo, p.n, ok = parsePRID(req.Base.ID); !ok {
			return all(fmt.Errorf("%s is not a pull request", req.Base.ID))
		}
	} else if req.Local != nil {
		parts := strings.Split(req.Local.Path, "/")
		if len(parts) != 3 || parts[1] != prDir {
			return all(fmt.Errorf("new files go in <repo>/%s/; %s is not a PR folder", prDir, req.Local.Path))
		}
		if p.repo = parts[0]; !s.t.sel.wants(p.repo) {
			return all(fmt.Errorf("%s is not a repository of this tree", p.repo))
		}
	}
	if local != nil {
		if err := prOnly(local); err != nil {
			return all(err)
		}
	}
	for i, a := range req.Actions {
		switch {
		case a.Target != "":
			p.errs[i] = s.checkComment(ctx, a, local, base)
		case a.Verb == "create":
			p.errs[i] = s.checkCreate(ctx, p, local)
		case a.Verb == "delete":
			p.errs[i] = errNoDelete
		default:
			p.errs[i] = s.checkGroup(ctx, p, a.Group, local, base)
		}
	}
	return p
}

// prOnly refuses pipeline elements in a pull request file.
func prOnly(root *xmltree.Node) error {
	if root.Name != prSchema.Root {
		return fmt.Errorf("root element <%s>, want <%s>", root.Name, prSchema.Root)
	}
	for _, c := range root.Elements() {
		if schema.Find(prSchema.Elems, c.Name) == nil {
			return fmt.Errorf("<%s> belongs in a pipeline file, not a pull request", c.Name)
		}
	}
	return nil
}

func (s *session) branchExists(ctx context.Context, repo, name string) error {
	err := s.get(ctx, s.repoPath(repo)+"/refs/branches/"+url.PathEscape(name), nil, nil)
	if errors.Is(err, adapter.ErrNotFound) {
		return fmt.Errorf("branch %q does not exist in %s", name, repo)
	}
	return err
}

func (s *session) checkCreate(ctx context.Context, p *plan, root *xmltree.Node) error {
	if strings.TrimSpace(text(child(root, "title"))) == "" {
		return errors.New("a new PR needs <title>")
	}
	src := attr(child(root, "source"), "branch")
	if src == "" {
		return errors.New(`a new PR needs <source branch="…"/>`)
	}
	if st := text(child(root, "state")); st != "" && st != "open" {
		return fmt.Errorf("a new PR is open, not %s", st)
	}
	if mr := text(child(root, "my-review")); mr != "" && mr != "none" {
		return errors.New("review a PR after it exists: leave <my-review> none")
	}
	for _, c := range root.ChildrenNamed("comment") {
		if attr(c, "parent") != "" {
			return errors.New("a new PR's comments cannot be replies")
		}
	}
	if err := s.branchExists(ctx, p.repo, src); err != nil {
		return err
	}
	if d := attr(child(root, "destination"), "branch"); d != "" {
		if err := s.branchExists(ctx, p.repo, d); err != nil {
			return err
		}
	}
	for _, g := range []string{"draft", "close-source-branch"} {
		if err := checkBool(root, g); err != nil {
			return err
		}
	}
	return s.resolveReviewers(ctx, p, root, nil)
}

func checkBool(root *xmltree.Node, name string) error {
	if v := text(child(root, name)); v != "" && v != "true" && v != "false" {
		return fmt.Errorf("<%s> is true or false, not %q", name, v)
	}
	return nil
}

// resolveReviewers fills p.reviewers from root's reviewers: kept ones by
// account (their text unchanged), new ones by name.
func (s *session) resolveReviewers(ctx context.Context, p *plan, root, base *xmltree.Node) error {
	p.reviewers = nil
	known := map[string]string{}
	for _, r := range childrenOf(base, "reviewer") {
		known[attr(r, "account")] = text(r)
	}
	for _, r := range root.ChildrenNamed("reviewer") {
		acc := attr(r, "account")
		if acc == "" {
			m, err := s.resolveUser(ctx, text(r))
			if err != nil {
				return err
			}
			acc = m.Account
		} else if name, ok := known[acc]; ok && name != text(r) {
			return fmt.Errorf("reviewer %s: to change a reviewer, replace the element with one without account", acc)
		}
		if !slices.Contains(p.reviewers, acc) {
			p.reviewers = append(p.reviewers, acc)
		}
	}
	return nil
}

func childrenOf(n *xmltree.Node, name string) []*xmltree.Node {
	if n == nil {
		return nil
	}
	return n.ChildrenNamed(name)
}

func (s *session) strategiesOf(ctx context.Context, p *plan) (strategies, error) {
	id := prID(p.repo, p.n)
	if st, ok := s.strategies[id]; ok {
		return st, nil
	}
	if _, err := s.fetchPR(ctx, p.repo, p.n); err != nil {
		return strategies{}, err
	}
	return s.strategies[id], nil
}

func (s *session) checkGroup(ctx context.Context, p *plan, group string, local, base *xmltree.Node) error {
	if what, ok := serviceGroups[group]; ok {
		return fmt.Errorf("<%s> is %s, set by Bitbucket", group, what)
	}
	switch group {
	case "title":
		if strings.TrimSpace(text(child(local, "title"))) == "" {
			return errors.New("<title> cannot be empty")
		}
	case "draft", "close-source-branch":
		return checkBool(local, group)
	case "source":
		if attr(child(local, "source"), "branch") != attr(child(base, "source"), "branch") {
			return errors.New("the source branch of a PR cannot change; open a new PR")
		}
	case "destination":
		if d := attr(child(local, "destination"), "branch"); d != "" {
			return s.branchExists(ctx, p.repo, d)
		}
		return errors.New(`<destination branch="…"/> cannot be empty`)
	case "reviewer":
		return s.resolveReviewers(ctx, p, local, base)
	case "my-review":
		switch v := text(child(local, "my-review")); v {
		case "none", "approved", "changes_requested", "":
		default:
			return fmt.Errorf("<my-review> is none, approved or changes_requested, not %q", v)
		}
	case "state":
		from, to := text(child(base, "state")), text(child(local, "state"))
		switch {
		case from == to: // only strategy or message changed: nothing to send
			return nil
		case from != "open":
			return fmt.Errorf("a %s PR cannot be reopened or changed", from)
		case to == "superseded":
			return errors.New("superseded is set by Bitbucket")
		case to == "declined":
			return nil
		case to != "merged":
			return fmt.Errorf("<state> goes from open to merged or declined, not %q", to)
		}
		strategy := attr(child(local, "state"), "strategy")
		if strategy == "" {
			return nil
		}
		st, err := s.strategiesOf(ctx, p)
		if err != nil {
			return err
		}
		if len(st.allowed) > 0 && !slices.Contains(st.allowed, strategy) {
			return fmt.Errorf("merge strategy %q not allowed on %s; allowed: %s", strategy, attr(child(local, "destination"), "branch"), strategyList(st))
		}
	case "description":
	default:
		return fmt.Errorf("<%s> does not belong in a pull request", group)
	}
	return nil
}

func strategyList(st strategies) string {
	var out []string
	for _, a := range st.allowed {
		if a == st.def {
			a += " (default)"
		}
		out = append(out, a)
	}
	return strings.Join(out, ", ")
}

func (s *session) checkComment(ctx context.Context, a adapter.Action, local, base *xmltree.Node) error {
	name, id, nth, _ := changes.ParseTarget(a.Target)
	if name != "comment" {
		return fmt.Errorf("<%s> is read-only", name)
	}
	me, err := s.whoami(ctx)
	if err != nil {
		return err
	}
	switch a.Verb {
	case "create":
		c := nthNew(local, "comment", nth)
		if c == nil {
			return errors.New("new comment not found")
		}
		if strings.TrimSpace(text(c)) == "" {
			return errors.New("a comment needs text")
		}
		if par := attr(c, "parent"); par != "" && findSub(base, "comment", par) == nil {
			return fmt.Errorf("reply to a comment that exists; comment %s is not on the PR (commit the parent first)", par)
		}
		if path := attr(c, "path"); path != "" {
			if _, err := strconv.Atoi(attr(c, "line")); err != nil {
				return fmt.Errorf(`an inline comment needs line="<number>" with path`)
			}
			if side := attr(c, "side"); side != "" && side != "new" && side != "old" {
				return fmt.Errorf(`side is "new" or "old", not %q`, side)
			}
		}
		return nil
	case "update":
		l, b := findSub(local, "comment", id), findSub(base, "comment", id)
		if l == nil || b == nil {
			return fmt.Errorf("comment %s not found", id)
		}
		for _, k := range []string{"parent", "path", "line", "side"} {
			if attr(l, k) != attr(b, k) {
				return fmt.Errorf("comment %s: %s cannot change", id, k)
			}
		}
		if text(l) != text(b) && attr(b, "account") != me {
			return fmt.Errorf("comment %s is %s's; you can only edit your own", id, attr(b, "author"))
		}
		if attr(l, "resolved") != attr(b, "resolved") && attr(b, "parent") != "" {
			return fmt.Errorf("comment %s is a reply; resolve its thread on comment %s", id, attr(b, "parent"))
		}
	case "delete":
		if b := findSub(base, "comment", id); b != nil && attr(b, "account") != me {
			return fmt.Errorf("comment %s is %s's; you can only delete your own", id, attr(b, "author"))
		}
	}
	return nil
}

// Apply checks, then sends one file's actions in the order of bitbucket spec
// §7.2: fields, comments, review, then merge or decline.
func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	out := results(req.Actions)
	p := s.plan(ctx, req)
	for i := range out {
		setErr(&out[i], p.errs[i])
	}
	for i, a := range req.Actions {
		if a.Target == "" && a.Verb == "create" {
			if out[i].Err == nil {
				out[i].ID, out[i].Detail, out[i].Err = s.create(ctx, p, req.Local.Root)
				out[i].Code = atlassian.Code(out[i].Err)
			}
			return out
		}
	}
	if req.Base == nil {
		return out
	}
	local, base := req.Local.Root, req.Base.Root
	prPath := s.repoPath(p.repo) + "/pullrequests/" + strconv.Itoa(p.n)
	// 1. one PUT for every field group
	var put []int
	for i, a := range req.Actions {
		if a.Target == "" && slices.Contains(putGroups, a.Group) && out[i].Err == nil {
			put = append(put, i)
		}
	}
	if len(put) > 0 {
		body := map[string]any{"title": text(child(local, "title"))}
		for _, i := range put {
			switch g := req.Actions[i].Group; g {
			case "description":
				body["description"] = text(child(local, "description"))
			case "destination":
				body["destination"] = map[string]any{"branch": map[string]any{"name": attr(child(local, "destination"), "branch")}}
			case "reviewer":
				body["reviewers"] = accounts(p.reviewers)
			case "close-source-branch":
				body["close_source_branch"] = text(child(local, g)) == "true"
			case "draft":
				body["draft"] = text(child(local, g)) == "true"
			}
		}
		err := s.c.Do(ctx, "PUT", prPath, body, nil)
		for _, i := range put {
			setErr(&out[i], err)
		}
	}
	// 2. comments, in the engine's order (new ones in document order)
	for i, a := range req.Actions {
		if a.Target != "" && out[i].Err == nil {
			setErr(&out[i], s.applyComment(ctx, prPath, a, local, base))
		}
	}
	// 3. review, then 4. merge or decline, last: it closes the PR
	for i, a := range req.Actions {
		if a.Group == "my-review" && out[i].Err == nil {
			setErr(&out[i], s.review(ctx, prPath, text(child(base, "my-review")), text(child(local, "my-review"))))
		}
	}
	for i, a := range req.Actions {
		if a.Group != "state" || out[i].Err != nil {
			continue
		}
		if slices.ContainsFunc(out[:i], func(r adapter.Result) bool { return r.Err != nil }) ||
			slices.ContainsFunc(out[i+1:], func(r adapter.Result) bool { return r.Err != nil }) {
			setErr(&out[i], errSkipped)
			continue
		}
		switch text(child(local, "state")) {
		case "merged":
			setErr(&out[i], s.merge(ctx, prPath, local))
		case "declined":
			setErr(&out[i], s.c.Do(ctx, "POST", prPath+"/decline", nil, nil))
		}
	}
	return out
}

func accounts(ids []string) []map[string]string {
	out := []map[string]string{}
	for _, id := range ids {
		out = append(out, map[string]string{"account_id": id})
	}
	return out
}

func (s *session) create(ctx context.Context, p *plan, root *xmltree.Node) (string, string, error) {
	body := map[string]any{
		"title":       text(child(root, "title")),
		"description": text(child(root, "description")),
		"source":      map[string]any{"branch": map[string]any{"name": attr(child(root, "source"), "branch")}},
		"reviewers":   accounts(p.reviewers),
	}
	if d := attr(child(root, "destination"), "branch"); d != "" {
		body["destination"] = map[string]any{"branch": map[string]any{"name": d}}
	}
	for elem, field := range map[string]string{"close-source-branch": "close_source_branch", "draft": "draft"} {
		if v := text(child(root, elem)); v != "" {
			body[field] = v == "true"
		}
	}
	var pr apiPR
	if err := s.c.Do(ctx, "POST", s.repoPath(p.repo)+"/pullrequests", body, &pr); err != nil {
		return "", "", err
	}
	path := s.repoPath(p.repo) + "/pullrequests/" + strconv.Itoa(pr.ID)
	detail := "PR " + strconv.Itoa(pr.ID)
	for _, c := range root.ChildrenNamed("comment") {
		if _, ok := c.Attr("id"); ok {
			continue
		}
		if err := s.c.Do(ctx, "POST", path+"/comments", commentBody(c), nil); err != nil {
			detail += "; a comment failed: " + err.Error()
		}
	}
	return prID(p.repo, pr.ID), detail, nil
}

func commentBody(c *xmltree.Node) map[string]any {
	body := map[string]any{"content": map[string]any{"raw": text(c)}}
	if par := attr(c, "parent"); par != "" {
		n, _ := strconv.Atoi(par)
		body["parent"] = map[string]any{"id": n}
	}
	if path := attr(c, "path"); path != "" {
		line, _ := strconv.Atoi(attr(c, "line"))
		side := "to"
		if attr(c, "side") == "old" {
			side = "from"
		}
		body["inline"] = map[string]any{"path": path, side: line}
	}
	return body
}

func (s *session) applyComment(ctx context.Context, prPath string, a adapter.Action, local, base *xmltree.Node) error {
	_, id, nth, _ := changes.ParseTarget(a.Target)
	cpath := prPath + "/comments/" + url.PathEscape(id)
	switch a.Verb {
	case "create":
		return s.c.Do(ctx, "POST", prPath+"/comments", commentBody(nthNew(local, "comment", nth)), nil)
	case "delete":
		return s.c.Do(ctx, "DELETE", cpath, nil, nil)
	}
	l, b := findSub(local, "comment", id), findSub(base, "comment", id)
	if text(l) != text(b) {
		if err := s.c.Do(ctx, "PUT", cpath, map[string]any{"content": map[string]any{"raw": text(l)}}, nil); err != nil {
			return err
		}
	}
	if r := attr(l, "resolved"); r != attr(b, "resolved") {
		method := "POST"
		if r != "true" {
			method = "DELETE"
		}
		return s.c.Do(ctx, method, cpath+"/resolve", nil, nil)
	}
	return nil
}

var reviewPath = map[string]string{"approved": "/approve", "changes_requested": "/request-changes"}

func (s *session) review(ctx context.Context, prPath, from, to string) error {
	if p, ok := reviewPath[from]; ok {
		if err := s.c.Do(ctx, "DELETE", prPath+p, nil, nil); err != nil {
			return err
		}
	}
	if p, ok := reviewPath[to]; ok {
		return s.c.Do(ctx, "POST", prPath+p, nil, nil)
	}
	return nil
}

// merge sends the merge and, when Bitbucket answers 202, polls its task
// (bitbucket spec §7.4).
func (s *session) merge(ctx context.Context, prPath string, local *xmltree.Node) error {
	st := child(local, "state")
	body := map[string]any{"close_source_branch": text(child(local, "close-source-branch")) == "true"}
	if v := attr(st, "strategy"); v != "" {
		body["merge_strategy"] = v
	}
	if v := attr(st, "message"); v != "" {
		body["message"] = v
	}
	code, hdr, err := s.c.DoHeader(ctx, "POST", prPath+"/merge", body, nil)
	if err != nil || code != 202 {
		return err
	}
	task := strings.TrimPrefix(hdr.Get("Location"), s.t.base)
	if task == "" {
		return errors.New("merge accepted without a task to follow; run gfs pull to see the result")
	}
	deadline := s.now().Add(mergeWait)
	for {
		var ts struct {
			TaskStatus string `json:"task_status"`
		}
		if err := s.c.Do(ctx, "GET", task, nil, &ts); err != nil {
			return err
		}
		if ts.TaskStatus == "SUCCESS" {
			return nil
		}
		if s.now().After(deadline) {
			return errors.New("merge still running after 5 minutes; run gfs pull to see the result")
		}
		if err := s.sleep(ctx, mergePoll); err != nil {
			return err
		}
	}
}
