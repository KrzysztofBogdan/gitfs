package bitbucket

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

var _ adapter.Tailer = (*session)(nil)

const (
	maxInterval = 30 * time.Second
	quietAfter  = time.Minute
)

// tailStep is one step as gfs tail follows it.
type tailStep struct {
	name, image string
	off         int64
	started     bool
	ended       bool   // footer printed
	partial     []byte // a line without its newline yet
}

// Tail streams the logs of a run, or of the latest run on a PR's source
// commit, until it completes (bitbucket spec §9).
func (s *session) Tail(ctx context.Context, id string, o adapter.TailOpts, out io.Writer) (adapter.TailResult, error) {
	if o.Interval <= 0 {
		o.Interval = 5 * time.Second
	}
	repo, uuid, ok := parseRunID(id)
	if !ok {
		var n int
		if repo, n, ok = parsePRID(id); !ok {
			return adapter.TailResult{}, fmt.Errorf("gfs tail takes a pipeline run or a pull request file")
		}
		var err error
		if uuid, err = s.awaitRun(ctx, repo, n, o); err != nil || uuid == "" {
			return adapter.TailResult{}, err
		}
	}
	res := adapter.TailResult{RunID: runID(repo, uuid)}
	runPath := s.repoPath(repo) + "/pipelines/{" + unbrace(uuid) + "}"
	var run apiPipeline
	if err := s.get(ctx, runPath, nil, &run); err != nil {
		return res, err
	}
	commit := run.Target.Commit.Hash
	if len(commit) > 7 {
		commit = commit[:7]
	}
	selector := run.Target.Selector.Type
	if run.Target.Selector.Pattern != "" {
		selector += ":" + run.Target.Selector.Pattern
	}
	ref := run.Target.RefName
	if ref == "" {
		ref = run.Target.Source
	}
	fmt.Fprintf(out, "── #%d %s %s %s ──\n", run.BuildNumber, selector, ref, commit)

	steps := map[string]*tailStep{}
	latest := map[string]apiStep{} // each step's state at the last refresh
	var order []string
	interval, quiet, sticky := o.Interval, s.now(), false
	for tick := 0; ; tick++ {
		if tick%3 == 0 || runningSteps(steps) == 0 {
			if err := s.get(ctx, runPath, nil, &run); err != nil {
				return res, err
			}
			list, err := s.steps(ctx, repo, run.UUID)
			if err != nil {
				return res, err
			}
			for _, st := range list {
				ts := steps[st.UUID]
				if ts == nil {
					ts = &tailStep{name: st.Name}
					if st.Image != nil {
						ts.image = st.Image.Name
					}
					steps[st.UUID] = ts
					order = append(order, st.UUID)
				}
				if !ts.started && st.State.Name != "PENDING" {
					ts.started = true
					if ts.image != "" {
						fmt.Fprintf(out, "── %s (%s) ──\n", ts.name, ts.image)
					} else {
						fmt.Fprintf(out, "── %s ──\n", ts.name)
					}
				}
				latest[st.UUID] = st
			}
		}
		running := runningSteps(steps)
		got := false
		for _, u := range order {
			ts := steps[u]
			if !ts.started || ts.ended {
				continue
			}
			var b bytes.Buffer
			total, err := s.c.Range(ctx, s.logPath(repo, run.UUID, u), ts.off, -1, &b)
			if err != nil {
				return res, err
			}
			ts.off += int64(b.Len())
			got = got || b.Len() > 0
			ts.write(out, b.Bytes(), running > 1)
			st := latest[u]
			if st.State.Name == "COMPLETED" && (b.Len() == 0 || total >= 0 && ts.off >= total) {
				ts.flush(out, running > 1)
				ts.ended = true
				fmt.Fprintf(out, "── %s: %s in %s ──\n", ts.name, resultName(st.State), dur(st.DurationInSeconds))
			}
		}
		if run.State.Name == "COMPLETED" && allEnded(steps) {
			res.Result = resultName(run.State)
			fmt.Fprintf(out, "── #%d %s in %s ──\n", run.BuildNumber, res.Result, dur(run.DurationInSeconds))
			return res, nil
		}
		switch {
		case s.c.NearLimit() || sticky:
			sticky, interval = true, maxInterval
		case got:
			interval, quiet = o.Interval, s.now()
		case s.now().Sub(quiet) >= quietAfter && interval < maxInterval:
			interval = min(2*interval, maxInterval)
			quiet = s.now()
		}
		if err := s.sleep(ctx, interval); err != nil {
			return res, nil // Ctrl-C or --timeout: the caller decides
		}
	}
}

func runningSteps(steps map[string]*tailStep) int {
	n := 0
	for _, ts := range steps {
		if ts.started && !ts.ended {
			n++
		}
	}
	return n
}

func allEnded(steps map[string]*tailStep) bool { return runningSteps(steps) == 0 }

// write prints whole lines of b, prefixed with the step name while steps
// run in parallel; a partial last line waits for the rest.
func (ts *tailStep) write(out io.Writer, b []byte, prefix bool) {
	ts.partial = append(ts.partial, b...)
	for {
		i := bytes.IndexByte(ts.partial, '\n')
		if i < 0 {
			return
		}
		ts.line(out, ts.partial[:i+1], prefix)
		ts.partial = ts.partial[i+1:]
	}
}

func (ts *tailStep) flush(out io.Writer, prefix bool) {
	if len(ts.partial) > 0 {
		ts.line(out, append(ts.partial, '\n'), prefix)
		ts.partial = nil
	}
}

func (ts *tailStep) line(out io.Writer, l []byte, prefix bool) {
	if prefix {
		fmt.Fprintf(out, "[%s] ", ts.name)
	}
	out.Write(l)
}

func dur(secs int) string {
	if secs < 60 {
		return strconv.Itoa(secs) + "s"
	}
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

// awaitRun is the newest run on PR n's current source commit, waiting up to
// o.Wait for one to start; "" with a nil error when ctx ended.
func (s *session) awaitRun(ctx context.Context, repo string, n int, o adapter.TailOpts) (string, error) {
	var pr apiPR
	if err := s.get(ctx, s.repoPath(repo)+"/pullrequests/"+strconv.Itoa(n), nil, &pr); err != nil {
		return "", err
	}
	if pr.Source.Commit == nil || pr.Source.Commit.Hash == "" {
		return "", fmt.Errorf("PR %d has no source commit", n)
	}
	hash := pr.Source.Commit.Hash
	deadline := s.now().Add(o.Wait)
	for {
		var p page[apiPipeline]
		q := url.Values{"sort": {"-created_on"}, "pagelen": {"20"}, "target.commit.hash": {hash}}
		if err := s.get(ctx, s.repoPath(repo)+"/pipelines/", q, &p); err != nil {
			return "", err
		}
		for _, run := range p.Values {
			if run.Target.Commit.Hash == hash { // the filter may be ignored
				return run.UUID, nil
			}
		}
		if !s.now().Before(deadline) {
			return "", fmt.Errorf("no pipeline started for %s (PR %d's source commit) within %s", hash, n, o.Wait)
		}
		if err := s.sleep(ctx, o.Interval); err != nil {
			return "", nil
		}
	}
}
