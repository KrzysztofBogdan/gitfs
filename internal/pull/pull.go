// Package pull implements `gitfs pull` per spec §10 and the pull design
// doc (2026-04-18-gitfs-pull-design).
package pull

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/aurl"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/uerr"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// lockTimeout is how long pull waits on a contended .gitfs/lock before
// giving up (pull design §3 step 1).
const lockTimeout = 5 * time.Second

// Args is the parsed CLI input for a pull call.
type Args struct {
	// Dir is the workdir root. If empty, pull walks up from the current
	// working directory to find a .gitfs/ (not yet wired here).
	Dir string
}

// Deps wires collaborators for Run. All fields optional; production
// defaults are supplied when nil.
type Deps struct {
	// Adapter, when set, bypasses scheme lookup. Used by tests; the CLI
	// path reads .gitfs/config.toml and looks up the adapter by scheme.
	Adapter adapter.Adapter
	Store   creds.Store
	IO      adapter.IO
	Stdout  io.Writer
}

// Run executes the pull flow.
func Run(ctx context.Context, args Args, deps Deps) error {
	start := args.Dir
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		start = cwd
	}
	root, err := workdir.FindRoot(start)
	if err != nil {
		return uerr.Errorf("%v", err)
	}
	l := workdir.Layout{Root: root}

	ad := deps.Adapter
	var url adapter.URL
	if ad == nil {
		url, ad, err = resolveAdapter(l.Config())
		if err != nil {
			return err
		}
	}

	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	lock, err := workdir.AcquireLock(root, lockTimeout)
	if err != nil {
		return fmt.Errorf("%v (pid %d holds .gitfs/lock?)", err, os.Getpid())
	}
	defer lock.Release()

	priorHead, err := readHEAD(l.Head())
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}

	blob, err := resolveCredentials(ctx, ad, url, deps)
	if err != nil {
		return err
	}

	tracked, err := trackedPaths(root)
	if err != nil {
		return fmt.Errorf("read tracked paths: %w", err)
	}

	pe := &proposer{tracked: tracked}
	newHead, err := ad.Pull(ctx, blob, priorHead, pe)
	if err != nil {
		return err
	}

	// Classify and apply each proposal.
	results := make([]pathResult, 0, len(pe.proposals))
	anySkip := false
	for _, prop := range pe.proposals {
		r, applyErr := applyProposal(root, prop)
		if applyErr != nil {
			return applyErr
		}
		results = append(results, r)
		if r.action == actionSkipped {
			anySkip = true
		}
	}

	if !anySkip {
		if err := workdir.WriteHEAD(root, newHead); err != nil {
			return err
		}
	}

	remote := url.Raw
	if remote == "" {
		remote = ad.Info().Scheme
	}
	printSummary(stdout, remote, results, anySkip, priorHead, newHead)
	return nil
}

// resolveAdapter reads config.toml, parses the remote URL, and
// instantiates the registered adapter factory.
func resolveAdapter(configPath string) (adapter.URL, adapter.Adapter, error) {
	raw, err := readConfigURL(configPath)
	if err != nil {
		return adapter.URL{}, nil, err
	}
	url, err := aurl.Parse(raw)
	if err != nil {
		return adapter.URL{}, nil, uerr.Errorf("bad url in .gitfs/config.toml: %v", err)
	}
	factory, ok := adapter.Lookup(url.Scheme)
	if !ok {
		installed := adapter.Installed()
		return adapter.URL{}, nil, uerr.Errorf(
			"unknown scheme %q. installed adapters: %s",
			url.Scheme, strings.Join(installed, ", "),
		)
	}
	ad, err := factory(url)
	if err != nil {
		return adapter.URL{}, nil, err
	}
	return url, ad, nil
}

// readConfigURL extracts the remote URL from a minimal TOML file of the
// shape written by workdir.Init: `[remote]\nurl = "..."\n`.
func readConfigURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "url") {
			continue
		}
		eq := strings.IndexByte(t, '=')
		if eq < 0 {
			continue
		}
		v := strings.TrimSpace(t[eq+1:])
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			return v[1 : len(v)-1], nil
		}
		return v, nil
	}
	return "", uerr.Errorf("no url in .gitfs/config.toml")
}

// resolveCredentials mirrors clone's auth handling: consult the keyring,
// falling back to the adapter's Authenticate when no entry is cached.
func resolveCredentials(
	ctx context.Context, ad adapter.Adapter, url adapter.URL, deps Deps,
) (adapter.Credentials, error) {
	if ad.Info().AuthStrategy == adapter.AuthNone {
		return nil, nil
	}
	store := deps.Store
	if store == nil {
		store = creds.NewKeyringOrNoop()
	}
	ioT := deps.IO
	if ioT == nil {
		ioT = defaultIO()
	}
	key := url.Scheme + ":" + url.Account
	prior, _, _ := store.Get(key)
	if prior != nil {
		return prior, nil
	}
	newBlob, persist, err := ad.Authenticate(ctx, nil, ioT)
	if err != nil {
		return nil, err
	}
	if persist {
		if err := store.Put(key, newBlob); err != nil {
			fmt.Fprintln(os.Stderr,
				"gitfs: could not save credentials to keyring:", err)
		}
	}
	return newBlob, nil
}

// proposer collects adapter proposals in emission order.
type proposer struct {
	proposals []proposal
	tracked   []string
}

type proposalKind int

const (
	kindFile proposalKind = iota
	kindTombstone
)

type proposal struct {
	kind    proposalKind
	path    string
	content []byte
}

func (p *proposer) File(path string, content []byte) error {
	if err := workdir.ValidatePath(path); err != nil {
		return err
	}
	p.proposals = append(p.proposals, proposal{
		kind:    kindFile,
		path:    path,
		content: append([]byte(nil), content...),
	})
	return nil
}

func (p *proposer) Tombstone(path string) error {
	if err := workdir.ValidatePath(path); err != nil {
		return err
	}
	p.proposals = append(p.proposals, proposal{kind: kindTombstone, path: path})
	return nil
}

func (p *proposer) TrackedPaths() []string {
	return append([]string(nil), p.tracked...)
}

type actionKind int

const (
	actionUpdated actionKind = iota
	actionRemoved
	actionSkipped
	// actionUnchanged: clean-already-matches no-op (pull design §3). Not
	// printed in the summary; still counts as applied for HEAD purposes.
	actionUnchanged
)

type pathResult struct {
	path   string
	action actionKind
}

func applyProposal(root string, p proposal) (pathResult, error) {
	l := workdir.Layout{Root: root}
	treePath := filepath.Join(root, filepath.FromSlash(p.path))
	shadowPath := filepath.Join(l.Shadow(), filepath.FromSlash(p.path))

	treeExists, treeContent, err := readIfExists(treePath)
	if err != nil {
		return pathResult{}, err
	}
	shadowExists, shadowContent, err := readIfExists(shadowPath)
	if err != nil {
		return pathResult{}, err
	}

	state := classifyLocal(treeExists, treeContent, shadowExists, shadowContent)

	if p.kind == kindFile {
		return applyFile(treePath, shadowPath, p, state, treeContent)
	}
	return applyTombstone(treePath, shadowPath, p, state)
}

type localState int

const (
	localNew            localState = iota // tree absent, shadow absent
	localClean                            // tree present, matches shadow
	localDirty                            // tree present, differs from shadow
	localUntracked                        // tree present, shadow absent
	localLocallyDeleted                   // tree absent, shadow present
)

func classifyLocal(treeExists bool, treeContent []byte, shadowExists bool, shadowContent []byte) localState {
	switch {
	case !treeExists && !shadowExists:
		return localNew
	case !treeExists && shadowExists:
		return localLocallyDeleted
	case treeExists && !shadowExists:
		return localUntracked
	case bytes.Equal(treeContent, shadowContent):
		return localClean
	default:
		return localDirty
	}
}

func applyFile(treePath, shadowPath string, p proposal, state localState, treeContent []byte) (pathResult, error) {
	switch state {
	case localClean:
		if bytes.Equal(treeContent, p.content) {
			return pathResult{path: p.path, action: actionUnchanged}, nil
		}
		if err := writeAtomic(treePath, p.content); err != nil {
			return pathResult{}, err
		}
		if err := writeAtomic(shadowPath, p.content); err != nil {
			return pathResult{}, err
		}
		return pathResult{path: p.path, action: actionUpdated}, nil
	case localNew:
		if err := writeAtomic(treePath, p.content); err != nil {
			return pathResult{}, err
		}
		if err := writeAtomic(shadowPath, p.content); err != nil {
			return pathResult{}, err
		}
		return pathResult{path: p.path, action: actionUpdated}, nil
	case localDirty, localUntracked, localLocallyDeleted:
		return pathResult{path: p.path, action: actionSkipped}, nil
	}
	return pathResult{}, fmt.Errorf("unreachable classification")
}

func applyTombstone(treePath, shadowPath string, p proposal, state localState) (pathResult, error) {
	switch state {
	case localClean:
		if err := os.Remove(treePath); err != nil && !os.IsNotExist(err) {
			return pathResult{}, err
		}
		if err := os.Remove(shadowPath); err != nil && !os.IsNotExist(err) {
			return pathResult{}, err
		}
		return pathResult{path: p.path, action: actionRemoved}, nil
	case localLocallyDeleted:
		if err := os.Remove(shadowPath); err != nil && !os.IsNotExist(err) {
			return pathResult{}, err
		}
		return pathResult{path: p.path, action: actionRemoved}, nil
	case localDirty, localUntracked, localNew:
		return pathResult{path: p.path, action: actionSkipped}, nil
	}
	return pathResult{}, fmt.Errorf("unreachable classification")
}

// readHEAD returns the current HEAD bytes or nil if the file does not exist.
func readHEAD(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// readIfExists returns (exists, content, err). A missing file yields
// (false, nil, nil).
func readIfExists(path string) (bool, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, b, nil
}

// writeAtomic writes content to path via temp file + rename.
func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gitfs-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func printSummary(w io.Writer, scheme string, results []pathResult, anySkip bool, priorHead, newHead []byte) {
	visible := make([]pathResult, 0, len(results))
	for _, r := range results {
		if r.action != actionUnchanged {
			visible = append(visible, r)
		}
	}
	if len(visible) == 0 {
		fmt.Fprintln(w, "Already up to date.")
		return
	}

	// Sort deterministically for stable output (adapter may emit in any order).
	sorted := append([]pathResult(nil), visible...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].action != sorted[j].action {
			return sorted[i].action < sorted[j].action
		}
		return sorted[i].path < sorted[j].path
	})

	fmt.Fprintf(w, "Pulled from %s\n", scheme)
	skipCount := 0
	for _, r := range sorted {
		switch r.action {
		case actionUpdated:
			fmt.Fprintf(w, "  updated:  %s\n", r.path)
		case actionRemoved:
			fmt.Fprintf(w, "  removed:  %s\n", r.path)
		case actionSkipped:
			skipCount++
			fmt.Fprintf(w, "  skipped (local changes):  %s\n", r.path)
		}
	}
	if anySkip {
		fmt.Fprintf(w, "HEAD held at %s (%d skipped path%s)\n",
			displayHead(priorHead), skipCount, pluralSuffix(skipCount))
	} else {
		fmt.Fprintf(w, "HEAD now at %s\n", displayHead(newHead))
	}
}

func displayHead(head []byte) string {
	s := strings.TrimSpace(string(head))
	if s == "" {
		return "<initial>"
	}
	return s
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func trackedPaths(root string) ([]string, error) {
	base := workdir.Layout{Root: root}.Shadow()
	var out []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
