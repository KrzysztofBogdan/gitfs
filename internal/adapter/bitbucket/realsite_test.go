package bitbucket

// Real-site checks, skipped unless the environment names a repository:
//
//	GFS_BITBUCKET_SITE=bitbucket://bitbucket.org/acme/api go test ./internal/adapter/bitbucket/ -run TestReal -v
//
// Writes also need a scratch repository, named twice (never inferred):
//
//	GFS_BITBUCKET_SCRATCH=acme/scratch GFS_BITBUCKET_WRITE_CONFIRM=acme/scratch
//
// Credentials as for gfs: gfs auth login, or GFS_BITBUCKET_EMAIL and
// GFS_BITBUCKET_TOKEN. The measurements of bitbucket spec §11.4 are logged.

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func realSession(t *testing.T, remote string) *session {
	t.Helper()
	u, err := url.Parse(remote)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{}, creds.Store{Dirs: creds.DefaultDirs()})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(tg)
	s.UseCache(t.TempDir())
	s.SetProgress(func(p adapter.Progress) {
		if p.Phase == "wait" {
			t.Log("wait:", p.Item)
		}
	})
	return s
}

func TestRealRead(t *testing.T) {
	site := os.Getenv("GFS_BITBUCKET_SITE")
	if site == "" {
		t.Skip("GFS_BITBUCKET_SITE not set")
	}
	sep := "?"
	if strings.Contains(site, "?") {
		sep = "&"
	}
	s := realSession(t, site+sep+"limit=20")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listed %d resources in %v", len(l.Resources), l.FullDirs)
	bigDone := false
	for _, r := range l.Resources {
		res, err := s.Fetch(bg, r.ID)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		if r.Version != "" && res.Version != r.Version {
			t.Errorf("%s: listed %s, fetched %s", r.ID, r.Version, res.Version)
		}
		// round trip: what gfs writes parses back to the same canonical content
		written := string(envelope.Bytes(envelope.New(res.Root.Clone()), mergedSchema))
		doc, err := envelope.Parse([]byte(written))
		if err != nil {
			t.Fatalf("%s: %v\n%s", r.ID, err, written)
		}
		if a, b := changes.CanonContent(res.Root, mergedSchema), changes.CanonContent(doc.Content, mergedSchema); a != b {
			t.Errorf("%s does not round-trip:\n%s\n---\n%s", r.ID, a, b)
		}
		if err := validate.Resource(doc.Content, res.Root, mergedSchema); err != nil {
			t.Errorf("%s: %v", r.ID, err)
		}
		if sl := res.Root.Child("steplog"); sl != nil && !bigDone {
			var b bytes.Buffer
			info, err := s.Download(bg, res.ID, attr(sl, "step"), &b)
			if err != nil || fmt.Sprint(info.Size) != attr(sl, "size") {
				t.Errorf("download %s: %v size %d, file says %s", res.ID, err, info.Size, attr(sl, "size"))
			}
			bigDone = true
		}
		if res.Root.Name == "pullrequest" {
			measureComments(t, res.Root)
		}
		if res.Root.Name == "pipeline" && strings.Contains(written, `state="in_progress"`) {
			t.Logf("MEASURE running step in %s: inline log present=%v", r.Path, strings.Contains(written, "<log>"))
		}
	}
	// the pipelines filter by commit, used by gfs tail on a PR file
	for _, r := range l.Resources {
		if _, _, ok := parseRunID(r.ID); ok {
			run := s.runs[r.ID]
			var p page[apiPipeline]
			q := url.Values{"target.commit.hash": {run.Target.Commit.Hash}, "pagelen": {"5"}}
			repo, _, _ := parseRunID(r.ID)
			if err := s.get(bg, s.repoPath(repo)+"/pipelines/", q, &p); err == nil {
				other := 0
				for _, v := range p.Values {
					if v.Target.Commit.Hash != run.Target.Commit.Hash {
						other++
					}
				}
				t.Logf("MEASURE pipelines target.commit.hash filter honoured: %v", other == 0)
			}
			break
		}
	}
}

// measureComments logs whether the newest comment is later than the PR's
// updated time, which says whether comments bump updated_on (spec §6.4).
func measureComments(t *testing.T, root *xmltree.Node) {
	newest := ""
	for _, c := range root.ChildrenNamed("comment") {
		if v := attr(c, "created"); v > newest {
			newest = v
		}
	}
	if newest != "" {
		t.Logf("MEASURE PR %s: newest comment %s, updated %s, comments bump updated_on: %v",
			attr(root, "number"), newest, attr(root, "updated"), newest <= attr(root, "updated"))
	}
}

func TestRealWrite(t *testing.T) {
	scratch, confirm := os.Getenv("GFS_BITBUCKET_SCRATCH"), os.Getenv("GFS_BITBUCKET_WRITE_CONFIRM")
	if scratch == "" || scratch != confirm {
		t.Skip("writes need GFS_BITBUCKET_SCRATCH and GFS_BITBUCKET_WRITE_CONFIRM naming the same repository")
	}
	ws, repo, ok := strings.Cut(scratch, "/")
	if !ok {
		t.Fatal("GFS_BITBUCKET_SCRATCH is <workspace>/<repo>")
	}
	s := realSession(t, "bitbucket://bitbucket.org/"+ws+"/"+repo)
	stamp := time.Now().UTC().Format("20060102-150405")
	var cleanup []func() error
	defer func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			if err := cleanup[i](); err != nil {
				t.Errorf("CLEANUP FAILED: %v", err)
			}
		}
	}()
	branch := func(name string) {
		// a commit on a new branch, made from the main branch through /src
		form := url.Values{"branch": {name}, "message": {"gfs real-site check"}, "gfs-check/" + name + ".txt": {stamp}}
		req, err := s.c.NewRequest(bg, "POST", s.repoPath(repo)+"/src", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := s.c.HTTPClient().Do(req)
		if err != nil || resp.StatusCode >= 300 {
			t.Fatalf("branch %s: %v %v", name, err, resp)
		}
		resp.Body.Close()
		cleanup = append(cleanup, func() error {
			err := s.c.Do(bg, "DELETE", s.repoPath(repo)+"/refs/branches/"+url.PathEscape(name), nil, nil)
			if err != nil && strings.Contains(err.Error(), "404") {
				return nil // deleted by the merge (close_source_branch)
			}
			return err
		})
	}
	newPR := func(name, title string) *adapter.Resource {
		branch(name)
		root, _ := xmltree.ParseString(`<pullrequest><title>` + title + `</title><source branch="` + name + `"/><close-source-branch>true</close-source-branch><description>made by gfs</description></pullrequest>`)
		req := adapter.ApplyRequest{Local: &adapter.Resource{Path: repo + "/pr/new.xml", Root: root},
			Actions: changes.ResolveActions(&Adapter{}, nil, &envelope.Doc{Content: root}, "", repo+"/pr/new.xml")}
		rs := s.Apply(bg, req)
		if rs[0].Err != nil {
			t.Fatalf("create: %v", rs[0].Err)
		}
		res, err := s.Fetch(bg, rs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		_, n, _ := parsePRID(res.ID)
		cleanup = append(cleanup, func() error {
			cur, err := s.Fetch(bg, res.ID)
			if err != nil || text(cur.Root.Child("state")) != "open" {
				return err
			}
			return s.c.Do(bg, "POST", s.repoPath(repo)+"/pullrequests/"+fmt.Sprint(n)+"/decline", nil, nil)
		})
		return res
	}
	apply := func(res *adapter.Resource, f func(*xmltree.Node)) *adapter.Resource {
		t.Helper()
		local := res.Root.Clone()
		f(local)
		acts := changes.ResolveActions(&Adapter{}, res.Root, &envelope.Doc{Content: local}, res.Path, res.Path)
		for _, r := range s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: res.ID, Path: res.Path, Root: local}, Base: res, Actions: acts}) {
			if r.Err != nil {
				t.Errorf("%s %s%s: %v", r.Action.Verb, r.Action.Target, r.Action.Group, r.Err)
			}
		}
		next, err := s.Fetch(bg, res.ID)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	before := time.Now()
	pr := newPR("gfs-check-a-"+stamp, "gfs check A "+stamp)
	pr = apply(pr, func(r *xmltree.Node) {
		setText(r, "title", "gfs check A edited "+stamp)
		setText(r, "description", "edited by gfs")
		appendXML(t, r, `<comment>top-level from gfs</comment>`)
		appendXML(t, r, `<comment path="gfs-check/gfs-check-a-`+stamp+`.txt" line="1">inline from gfs</comment>`)
	})
	updatedBefore := attr(pr.Root, "updated")
	top := ""
	for _, c := range pr.Root.ChildrenNamed("comment") {
		if text(c) == "top-level from gfs" {
			top = attr(c, "id")
		}
	}
	if top == "" || text(pr.Root.Child("title")) != "gfs check A edited "+stamp {
		t.Fatalf("edits not visible:\n%s", canonical(t, pr.Root))
	}
	time.Sleep(time.Second)
	pr = apply(pr, func(r *xmltree.Node) { appendXML(t, r, `<comment parent="`+top+`">reply from gfs</comment>`) })
	t.Logf("MEASURE a reply bumps the PR's updated_on: %v", attr(pr.Root, "updated") != updatedBefore)
	pr = apply(pr, func(r *xmltree.Node) { findSub(r, "comment", top).SetAttr("resolved", "true") })
	if attr(findSub(pr.Root, "comment", top), "resolved") != "true" {
		t.Error("resolve not visible")
	}
	// the author cannot approve their own PR; measure what Bitbucket says
	for _, mr := range []string{"approved", "changes_requested"} {
		req := pr.Root.Clone()
		setText(req, "my-review", mr)
		acts := changes.ResolveActions(&Adapter{}, pr.Root, &envelope.Doc{Content: req}, pr.Path, pr.Path)
		rs := s.Apply(bg, adapter.ApplyRequest{Local: &adapter.Resource{ID: pr.ID, Path: pr.Path, Root: req}, Base: pr, Actions: acts})
		t.Logf("MEASURE author sets my-review %s: %v", mr, rs[0].Err)
		pr, _ = s.Fetch(bg, pr.ID)
	}
	pr = apply(pr, func(r *xmltree.Node) { setText(r, "state", "declined") })
	if text(pr.Root.Child("state")) != "declined" {
		t.Error("decline not visible")
	}
	b := newPR("gfs-check-b-"+stamp, "gfs check B "+stamp)
	b = apply(b, func(r *xmltree.Node) {
		setText(r, "state", "merged")
		r.Child("state").SetAttr("strategy", "squash")
	})
	if text(b.Root.Child("state")) != "merged" {
		t.Errorf("merge not visible:\n%s", canonical(t, b.Root))
	}
	t.Logf("write checks took %v", time.Since(before).Round(time.Second))
	// the merge commit triggers a pipeline when the repository has one: tail it
	var p page[apiPipeline]
	if err := s.get(bg, s.repoPath(repo)+"/pipelines/", url.Values{"sort": {"-created_on"}, "pagelen": {"1"}}, &p); err == nil && len(p.Values) > 0 &&
		parseStamp(p.Values[0].CreatedOn).After(before) {
		var out bytes.Buffer
		res, err := s.Tail(bg, runID(repo, p.Values[0].UUID), adapter.TailOpts{Interval: 5 * time.Second}, &out)
		t.Logf("MEASURE tail: %+v %v\n%s", res, err, out.String())
	}
}
