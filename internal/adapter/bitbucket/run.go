package bitbucket

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// stepLog is what a Range read returned for a step: Total -1 when unknown.
type stepLog struct {
	Data  []byte
	Total int64
}

// runInput is everything a <pipeline> file is made of (bitbucket spec §5.5).
// Logs holds an entry for every step that has a log.
type runInput struct {
	Repo  string
	Run   apiPipeline
	Steps []apiStep
	Logs  map[string]stepLog // by step uuid
	Limit int64
}

func runID(repo, uuid string) string { return repo + "/" + runDir + "/" + unbrace(uuid) }

func runPath(repo string, n int) string { return repo + "/" + runDir + "/" + strconv.Itoa(n) + ".xml" }

func parseRunID(id string) (repo, uuid string, ok bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[1] != runDir || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

var slugCharsRe = regexp.MustCompile(`[^a-z0-9]+`)

// logName is the sidecar file of step n's log: 1-build.log.
func logName(n int, step string) string {
	s := strings.Trim(slugCharsRe.ReplaceAllString(strings.ToLower(step), "-"), "-")
	if s == "" {
		s = "step"
	}
	return fmt.Sprintf("%d-%s.log", n, s)
}

// isBig reports whether a log goes to a sidecar file instead of inline.
func isBig(l stepLog, limit int64) bool {
	return l.Total > limit || int64(len(l.Data)) > limit
}

// stateName is a run's or step's state as gfs writes it.
func stateName(s apiState) string {
	if s.Name == "IN_PROGRESS" && s.Stage != nil {
		switch s.Stage.Name {
		case "PAUSED", "HALTED":
			return strings.ToLower(s.Stage.Name)
		}
	}
	return strings.ToLower(s.Name)
}

func resultName(s apiState) string {
	if s.Name != "COMPLETED" || s.Result == nil {
		return ""
	}
	return strings.ToLower(s.Result.Name)
}

func runRoot(in runInput) *xmltree.Node {
	p := in.Run
	root := el("pipeline", "id", runID(in.Repo, p.UUID), "number", strconv.Itoa(p.BuildNumber), "created", stamp(p.CreatedOn),
		"completed", stamp(p.CompletedOn), "version", runVersion(in))
	trigger := strings.ToLower(strings.TrimPrefix(strings.ToLower(p.Trigger.Type), "pipeline_trigger_"))
	if trigger == "" {
		trigger = strings.ToLower(p.Trigger.Name)
	}
	t := p.Target
	typ, ref := t.RefType, t.RefName
	switch t.Type {
	case "pipeline_pullrequest_target":
		typ, ref = "pullrequest", t.Source
	case "pipeline_commit_target":
		typ = "commit"
	}
	selector := t.Selector.Type
	if t.Selector.Pattern != "" {
		selector += ":" + t.Selector.Pattern
	}
	pr := ""
	if t.PullRequest != nil {
		pr = strconv.Itoa(t.PullRequest.ID)
	}
	add(root,
		withText(el("state"), stateName(p.State)),
		withText(el("result"), resultName(p.State)),
		withText(el("trigger", "type", trigger, "account", p.Creator.AccountID), p.Creator.DisplayName),
		el("target", "type", typ, "ref", ref, "commit", t.Commit.Hash, "pipeline", selector, "pr", pr))
	if p.State.Name == "COMPLETED" {
		add(root, withText(el("duration", "build-seconds", strconv.Itoa(p.BuildSecondsUsed)), strconv.Itoa(p.DurationInSeconds)))
	}
	var bigs []*xmltree.Node
	for i, st := range in.Steps {
		n := i + 1
		image := ""
		if st.Image != nil {
			image = st.Image.Name
		}
		dur := ""
		if st.State.Name == "COMPLETED" {
			dur = strconv.Itoa(st.DurationInSeconds)
		}
		sn := el("step", "id", unbrace(st.UUID), "n", strconv.Itoa(n), "name", st.Name, "image", image, "state", stateName(st.State),
			"result", resultName(st.State), "started", stamp(st.StartedOn), "completed", stamp(st.CompletedOn), "duration", dur)
		if l, ok := in.Logs[st.UUID]; ok {
			size := l.Total
			if size < 0 {
				size = int64(len(l.Data))
			}
			sn.SetAttr("log-size", strconv.FormatInt(size, 10))
			if isBig(l, in.Limit) {
				bn := el("steplog", "step", unbrace(st.UUID), "name", logName(n, st.Name))
				if l.Total >= 0 {
					bn.SetAttr("size", strconv.FormatInt(l.Total, 10))
				}
				bigs = append(bigs, bn)
			} else if len(l.Data) > 0 {
				add(sn, withText(el("log"), logText(l.Data)))
			}
		}
		add(root, sn)
	}
	add(root, bigs...)
	return root
}

// runVersion never changes once a run completed; while it runs it moves
// with every step state and log size (bitbucket spec §5.5).
func runVersion(in runInput) string {
	p := in.Run
	if p.State.Name == "COMPLETED" {
		return "completed:" + resultName(p.State) + ":" + stamp(p.CompletedOn)
	}
	h := sha256.New()
	for _, st := range in.Steps {
		l := in.Logs[st.UUID]
		fmt.Fprintf(h, "%s %s %s %d %d\x00", st.UUID, stateName(st.State), resultName(st.State), len(l.Data), l.Total)
	}
	return stateName(p.State) + ":" + hex.EncodeToString(h.Sum(nil))[:8]
}
