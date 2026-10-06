package bitbucket

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// prInput is everything a <pullrequest> file is made of (bitbucket spec §5.1).
type prInput struct {
	Repo     string
	PR       apiPR
	Comments []apiComment
	Statuses []apiStatus
	Me       string // account_id of the token's user: <my-review>
}

const prDir, runDir = "pr", "pipelines"

func prID(repo string, n int) string { return repo + "/" + prDir + "/" + strconv.Itoa(n) }

func prPath(repo string, n int, title string) string {
	name := strconv.Itoa(n)
	if t := strings.TrimSpace(title); t != "" {
		name += " " + t
	}
	return repo + "/" + prDir + "/" + sanitize(cut(name)) + ".xml"
}

func parsePRID(id string) (repo string, n int, ok bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[1] != prDir {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	return parts[0], n, err == nil
}

func boolText(b bool) string { return strconv.FormatBool(b) }

func reviewState(p apiParticipant) string {
	switch {
	case p.State != nil && *p.State != "":
		return *p.State
	case p.Approved:
		return "approved"
	}
	return ""
}

func userNode(name string, u apiUser) *xmltree.Node {
	return withText(el(name, "account", u.AccountID), u.DisplayName)
}

var runURLRe = regexp.MustCompile(`results/(\d+)`)

func prRoot(in prInput) *xmltree.Node {
	p := in.PR
	root := el("pullrequest", "id", prID(in.Repo, p.ID), "number", strconv.Itoa(p.ID), "created", stamp(p.CreatedOn),
		"updated", stamp(p.UpdatedOn), "version", prVersion(in))
	commit := func(c *apiCommit) string {
		if c == nil {
			return ""
		}
		return c.Hash
	}
	states := map[string]string{}
	my := "none"
	for _, pa := range p.Participants {
		states[pa.User.AccountID] = reviewState(pa)
		if pa.User.AccountID == in.Me && reviewState(pa) != "" {
			my = reviewState(pa)
		}
	}
	add(root,
		withText(el("title"), p.Title),
		withText(el("state"), strings.ToLower(p.State)),
		withText(el("draft"), boolText(p.Draft)),
		el("source", "branch", p.Source.Branch.Name, "commit", commit(p.Source.Commit)),
		el("destination", "branch", p.Destination.Branch.Name, "commit", commit(p.Destination.Commit)),
		withText(el("close-source-branch"), boolText(p.CloseSourceBranch)),
		userNode("author", p.Author))
	var reviewers []string
	for _, r := range p.Reviewers {
		reviewers = append(reviewers, r.AccountID)
		n := userNode("reviewer", r)
		if st := states[r.AccountID]; st != "" {
			n.SetAttr("state", st)
		}
		add(root, n)
	}
	for _, pa := range p.Participants {
		if slices.Contains(reviewers, pa.User.AccountID) {
			continue
		}
		n := userNode("participant", pa.User)
		if st := reviewState(pa); st != "" {
			n.SetAttr("state", st)
		}
		add(root, n)
	}
	add(root, withText(el("my-review"), my), withText(el("merge-commit"), commit(p.MergeCommit)), withText(el("description"), p.Description))
	for _, st := range in.Statuses {
		run := ""
		if m := runURLRe.FindStringSubmatch(st.URL); m != nil && strings.Contains(st.URL, "pipelines") {
			run = "../" + runDir + "/" + m[1] + ".xml"
		}
		add(root, withText(el("build", "key", st.Key, "state", st.State, "url", st.URL, "run", run), st.Name))
	}
	for _, c := range in.Comments {
		if c.Deleted {
			continue
		}
		n := el("comment", "id", strconv.Itoa(c.ID), "account", c.User.AccountID, "author", c.User.DisplayName,
			"created", stamp(c.CreatedOn), "updated", stamp(c.UpdatedOn))
		if c.Parent != nil {
			n.SetAttr("parent", strconv.Itoa(c.Parent.ID))
		}
		if in := c.Inline; in != nil {
			n.SetAttr("path", xmlText(in.Path))
			switch {
			case in.To != nil:
				n.SetAttr("line", strconv.Itoa(*in.To))
				n.SetAttr("side", "new")
			case in.From != nil:
				n.SetAttr("line", strconv.Itoa(*in.From))
				n.SetAttr("side", "old")
			}
		}
		if c.Parent == nil {
			n.SetAttr("resolved", boolText(c.Resolution != nil))
		}
		add(root, withText(n, c.Content.Raw))
	}
	return root
}

// prVersion is updated_on plus a digest of what can change without moving it
// (bitbucket spec §6.3): statuses, comments, review states.
func prVersion(in prInput) string {
	h := sha256.New()
	for _, st := range in.Statuses {
		fmt.Fprintf(h, "s %s %s %s\x00", st.Key, st.State, st.Name)
	}
	for _, c := range in.Comments {
		fmt.Fprintf(h, "c %d %s %t %t\x00", c.ID, c.UpdatedOn, c.Deleted, c.Resolution != nil)
	}
	for _, pa := range in.PR.Participants {
		fmt.Fprintf(h, "p %s %s\x00", pa.User.AccountID, reviewState(pa))
	}
	return stamp(in.PR.UpdatedOn) + "+" + hex.EncodeToString(h.Sum(nil))[:8]
}
