// Package commit implements `gitfs commit` per the commit design doc
// (2026-04-19-gitfs-commit-design).
package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

const lockTimeout = 5 * time.Second

// Args is the parsed CLI input for a commit call.
type Args struct {
	// Dir is the workdir root. If empty, commit walks up from the
	// current working directory to find a .gitfs/.
	Dir string
	// Paths are user-supplied workdir-relative paths. Empty means
	// auto-select every dirty / new / locally-deleted path.
	Paths []string
	// Message is the value of `-m`. Empty when not supplied.
	Message string
}

// Deps wires collaborators for Run. All fields optional; production
// defaults are supplied when nil.
type Deps struct {
	Adapter adapter.Adapter
	Store   creds.Store
	IO      adapter.IO
	Stdout  io.Writer
	Now     func() time.Time
}

// pathClass is one path's classification before any send.
type pathClass int

const (
	classClean pathClass = iota
	classDirty
	classNew
	classLocallyDeleted
	classNonexistent
)

// classifiedPath is one user or auto-selected path plus its current
// state. preTree / shadow carry the pre-commit content so the snapshot
// and request payloads do not need to re-read the filesystem.
type classifiedPath struct {
	path    string
	class   pathClass
	preTree []byte
	shadow  []byte
}

// logEntry is the JSON payload written to .gitfs/log/<timestamp>.json.
type logEntry struct {
	Timestamp string         `json:"timestamp"`
	Remote    string         `json:"remote"`
	Message   string         `json:"message,omitempty"`
	Entries   []logEntryPath `json:"entries"`
}

type logEntryPath struct {
	Path    string           `json:"path"`
	Kind    string           `json:"kind"`
	Results []logEntryResult `json:"results"`
}

type logEntryResult struct {
	Path   string `json:"path"`
	Delete bool   `json:"delete,omitempty"`
}

// Run executes the commit flow.
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
	} else if raw, cErr := readConfigURL(l.Config()); cErr == nil {
		// Tests often inject Adapter but still want credential lookup to
		// key by the config's scheme/account. Parse best-effort; errors
		// are ignored — the injected adapter is the source of truth.
		if u, pErr := aurl.Parse(raw); pErr == nil {
			url = u
		}
	}

	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	lock, err := workdir.AcquireLock(root, lockTimeout)
	if err != nil {
		return fmt.Errorf(
			"workdir is locked by another process (pid %d)", os.Getpid(),
		)
	}
	defer lock.Release()

	classified, err := classifyPaths(root, args.Paths)
	if err != nil {
		return err
	}

	var sendable []classifiedPath
	var cleanList []string
	var invalidList []string
	for _, cp := range classified {
		switch cp.class {
		case classClean:
			cleanList = append(cleanList, cp.path)
		case classNonexistent:
			invalidList = append(invalidList, cp.path)
		default:
			sendable = append(sendable, cp)
		}
	}

	// Silent no-op: nothing classified at all.
	if len(sendable) == 0 && len(cleanList) == 0 && len(invalidList) == 0 {
		return nil
	}

	remote := url.Raw
	if remote == "" {
		remote = ad.Info().Scheme
	}

	var accepted []acceptedEntry
	var rejected []rejection

	if len(sendable) > 0 {
		store := deps.Store
		if store == nil {
			store = creds.NewKeyringOrNoop()
		}
		ioT := deps.IO
		if ioT == nil {
			ioT = defaultIO()
		}
		blob, fromKeyring, err := resolveCredentials(ctx, ad, url, store, ioT)
		if err != nil {
			return err
		}

		reqs := make([]adapter.CommitRequest, 0, len(sendable))
		for _, cp := range sendable {
			req := adapter.CommitRequest{
				Path:    cp.path,
				Message: args.Message,
			}
			if cp.class == classLocallyDeleted {
				req.Kind = adapter.CommitDeletion
			} else {
				req.Kind = adapter.CommitContent
				req.Content = cp.preTree
			}
			reqs = append(reqs, req)
		}

		ce := newCollector()
		if err := ad.Commit(ctx, blob, reqs, ce, ioT); err != nil {
			return err
		}

		if ce.updatedCreds != nil && fromKeyring {
			key := url.Scheme + ":" + url.Account
			if putErr := store.Put(key, ce.updatedCreds); putErr != nil {
				fmt.Fprintln(os.Stderr,
					"gitfs: could not save credentials to keyring:", putErr)
			}
		}

		tsDir := now().UTC().Format("20060102T150405Z")
		trashBase := filepath.Join(l.Trash(), tsDir)

		for _, cp := range sendable {
			if reason, ok := ce.rejects[cp.path]; ok {
				rejected = append(rejected, rejection{
					path: cp.path, reason: reason,
				})
				continue
			}
			results, ok := ce.accepts[cp.path]
			if !ok {
				rejected = append(rejected, rejection{
					path:   cp.path,
					reason: "adapter did not respond",
				})
				continue
			}
			if err := applyAccept(root, trashBase, cp, results); err != nil {
				return err
			}
			accepted = append(accepted, acceptedEntry{cp: cp, results: results})
		}

		if len(accepted) > 0 {
			if err := writeLog(
				l.Log(), now(), remote, args.Message, accepted,
			); err != nil {
				return err
			}
		}
	}

	printSummary(stdout, remote, accepted, cleanList, invalidList, rejected)

	if len(rejected) > 0 {
		return fmt.Errorf("commit had %d rejection(s)", len(rejected))
	}
	if len(invalidList) > 0 {
		return uerr.Errorf(
			"invalid path(s): %s", strings.Join(invalidList, ", "),
		)
	}
	return nil
}

// --- classification ------------------------------------------------------

func classifyPaths(root string, userPaths []string) ([]classifiedPath, error) {
	if len(userPaths) == 0 {
		return discoverAuto(root)
	}
	out := make([]classifiedPath, 0, len(userPaths))
	for _, p := range userPaths {
		rel := filepath.ToSlash(filepath.Clean(p))
		cp, err := classifyOne(root, rel)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, nil
}

// discoverAuto walks tree + shadow and returns every dirty, new, or
// locally-deleted path. Clean and nonexistent paths are excluded — the
// no-args flow sends only paths the user implicitly changed.
func discoverAuto(root string) ([]classifiedPath, error) {
	l := workdir.Layout{Root: root}
	tree, err := walkFiles(root, l.Gitfs())
	if err != nil {
		return nil, err
	}
	shadow, err := walkFiles(l.Shadow(), "")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for p := range tree {
		seen[p] = struct{}{}
	}
	for p := range shadow {
		seen[p] = struct{}{}
	}
	sorted := make([]string, 0, len(seen))
	for p := range seen {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	out := make([]classifiedPath, 0, len(sorted))
	for _, p := range sorted {
		treeC, hasTree := tree[p]
		shadowC, hasShadow := shadow[p]
		cp := classifyFromContent(p, hasTree, treeC, hasShadow, shadowC)
		if cp.class == classClean || cp.class == classNonexistent {
			continue
		}
		out = append(out, cp)
	}
	return out, nil
}

// classifyOne reads tree + shadow for a single workdir-relative path and
// returns its classification. Errors only on non-ENOENT IO failures.
func classifyOne(root, rel string) (classifiedPath, error) {
	l := workdir.Layout{Root: root}
	treePath := filepath.Join(root, filepath.FromSlash(rel))
	shadowPath := filepath.Join(l.Shadow(), filepath.FromSlash(rel))

	treeC, hasTree, err := readIfExists(treePath)
	if err != nil {
		return classifiedPath{}, err
	}
	shadowC, hasShadow, err := readIfExists(shadowPath)
	if err != nil {
		return classifiedPath{}, err
	}
	return classifyFromContent(rel, hasTree, treeC, hasShadow, shadowC), nil
}

func classifyFromContent(
	path string,
	hasTree bool, tree []byte,
	hasShadow bool, shadow []byte,
) classifiedPath {
	cp := classifiedPath{path: path, preTree: tree, shadow: shadow}
	switch {
	case hasTree && hasShadow && bytes.Equal(tree, shadow):
		cp.class = classClean
	case hasTree && hasShadow:
		cp.class = classDirty
	case hasTree && !hasShadow:
		cp.class = classNew
	case !hasTree && hasShadow:
		cp.class = classLocallyDeleted
	default:
		cp.class = classNonexistent
	}
	return cp
}

// walkFiles returns every regular file under root (keyed by slash-form
// path relative to root). An absent root returns an empty map.
// excludePrefix, if non-empty, prunes any subtree matching that prefix
// (used to skip .gitfs/ when walking the working tree).
func walkFiles(root, excludePrefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if root == "" {
		return out, nil
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if excludePrefix != "" && p == excludePrefix {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- adapter / credentials plumbing --------------------------------------

func resolveAdapter(configPath string) (adapter.URL, adapter.Adapter, error) {
	raw, err := readConfigURL(configPath)
	if err != nil {
		return adapter.URL{}, nil, err
	}
	url, err := aurl.Parse(raw)
	if err != nil {
		return adapter.URL{}, nil, uerr.Errorf(
			"bad url in .gitfs/config.toml: %v", err,
		)
	}
	factory, ok := adapter.Lookup(url.Scheme)
	if !ok {
		return adapter.URL{}, nil, uerr.Errorf(
			"unknown scheme %q. installed adapters: %s",
			url.Scheme, strings.Join(adapter.Installed(), ", "),
		)
	}
	ad, err := factory(url)
	if err != nil {
		return adapter.URL{}, nil, err
	}
	return url, ad, nil
}

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

// resolveCredentials returns the credential blob plus a flag indicating
// whether it came from the keyring. The flag gates later persistence
// of adapter-requested blob updates — env-derived credentials must not
// be written back to the keyring.
func resolveCredentials(
	ctx context.Context, ad adapter.Adapter, url adapter.URL,
	store creds.Store, ioT adapter.IO,
) (adapter.Credentials, bool, error) {
	if ad.Info().AuthStrategy == adapter.AuthNone {
		return nil, false, nil
	}
	key := url.Scheme + ":" + url.Account
	prior, _, _ := store.Get(key)
	if prior != nil {
		return prior, true, nil
	}
	newBlob, persist, err := ad.Authenticate(ctx, nil, ioT)
	if err != nil {
		return nil, false, err
	}
	if persist {
		if err := store.Put(key, newBlob); err != nil {
			fmt.Fprintln(os.Stderr,
				"gitfs: could not save credentials to keyring:", err)
		}
	}
	return newBlob, bool(persist), nil
}

// --- adapter callback collection -----------------------------------------

type collector struct {
	accepts      map[string][]adapter.CommitResult
	rejects      map[string]string
	updatedCreds adapter.Credentials
}

func newCollector() *collector {
	return &collector{
		accepts: map[string][]adapter.CommitResult{},
		rejects: map[string]string{},
	}
}

func (c *collector) Accept(path string, results []adapter.CommitResult) error {
	copied := make([]adapter.CommitResult, len(results))
	for i, r := range results {
		r2 := r
		if r.Content != nil {
			r2.Content = append([]byte(nil), r.Content...)
		}
		copied[i] = r2
	}
	c.accepts[path] = copied
	return nil
}

func (c *collector) Reject(path string, reason string) error {
	c.rejects[path] = reason
	return nil
}

func (c *collector) UpdateCredentials(blob adapter.Credentials) error {
	if blob == nil {
		c.updatedCreds = nil
		return nil
	}
	c.updatedCreds = append([]byte(nil), blob...)
	return nil
}

// --- apply ---------------------------------------------------------------

type acceptedEntry struct {
	cp      classifiedPath
	results []adapter.CommitResult
}

type rejection struct {
	path   string
	reason string
}

// applyAccept applies one accepted request's canonical state to the
// tree + shadow, preceded by a pre-commit snapshot into trashBase.
func applyAccept(
	root, trashBase string,
	cp classifiedPath,
	results []adapter.CommitResult,
) error {
	l := workdir.Layout{Root: root}

	// Gather affected workdir-relative paths (input + each result).
	affected := map[string]struct{}{cp.path: {}}
	for _, r := range results {
		affected[r.Path] = struct{}{}
	}

	// (a) Snapshot each affected path's pre-commit content, if any.
	for ap := range affected {
		content, ok, err := readPreCommit(
			filepath.Join(root, filepath.FromSlash(ap)),
			filepath.Join(l.Shadow(), filepath.FromSlash(ap)),
		)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		snapPath := filepath.Join(trashBase, filepath.FromSlash(ap))
		if err := writeAtomic(snapPath, content); err != nil {
			return err
		}
	}

	// Work out whether the input path itself is covered by results.
	inputCovered := false
	for _, r := range results {
		if r.Path == cp.path {
			inputCovered = true
			break
		}
	}

	// (b, c) Apply each result, writing tree then shadow.
	for _, r := range results {
		treePath := filepath.Join(root, filepath.FromSlash(r.Path))
		shadowPath := filepath.Join(l.Shadow(), filepath.FromSlash(r.Path))
		if r.Delete {
			if err := removePath(treePath); err != nil {
				return err
			}
			if err := removePath(shadowPath); err != nil {
				return err
			}
			continue
		}
		if err := writeAtomic(treePath, r.Content); err != nil {
			return err
		}
		if err := writeAtomic(shadowPath, r.Content); err != nil {
			return err
		}
	}

	// If the input path isn't in the results, the adapter implicitly
	// removed it (deletion accepted, or rename away).
	if !inputCovered {
		treePath := filepath.Join(root, filepath.FromSlash(cp.path))
		shadowPath := filepath.Join(l.Shadow(), filepath.FromSlash(cp.path))
		if err := removePath(treePath); err != nil {
			return err
		}
		if err := removePath(shadowPath); err != nil {
			return err
		}
	}
	return nil
}

func readPreCommit(treePath, shadowPath string) ([]byte, bool, error) {
	if b, err := os.ReadFile(treePath); err == nil {
		return b, true, nil
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	if b, err := os.ReadFile(shadowPath); err == nil {
		return b, true, nil
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	return nil, false, nil
}

// --- IO helpers ----------------------------------------------------------

func readIfExists(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

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

func removePath(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- log -----------------------------------------------------------------

func writeLog(
	logDir string, ts time.Time, remote, message string,
	accepted []acceptedEntry,
) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	entries := make([]logEntryPath, 0, len(accepted))
	for _, a := range accepted {
		kind := "content"
		if a.cp.class == classLocallyDeleted {
			kind = "deletion"
		}
		results := make([]logEntryResult, 0, len(a.results))
		for _, r := range a.results {
			results = append(results, logEntryResult{
				Path: r.Path, Delete: r.Delete,
			})
		}
		entries = append(entries, logEntryPath{
			Path: a.cp.path, Kind: kind, Results: results,
		})
	}
	body := logEntry{
		Timestamp: ts.UTC().Format(time.RFC3339),
		Remote:    remote,
		Message:   message,
		Entries:   entries,
	}
	b, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	name := ts.UTC().Format("20060102T150405Z") + ".json"
	return writeAtomic(filepath.Join(logDir, name), b)
}

// --- output --------------------------------------------------------------

func printSummary(
	w io.Writer, remote string,
	accepted []acceptedEntry,
	clean []string,
	invalid []string,
	rejected []rejection,
) {
	if len(accepted) == 0 && len(clean) == 0 &&
		len(invalid) == 0 && len(rejected) == 0 {
		return
	}
	fmt.Fprintf(w, "Committed to %s\n", remote)
	sentPaths := make([]string, 0, len(accepted))
	for _, a := range accepted {
		sentPaths = append(sentPaths, a.cp.path)
	}
	sort.Strings(sentPaths)
	for _, p := range sentPaths {
		fmt.Fprintf(w, "  sent:      %s\n", p)
	}
	clean2 := append([]string(nil), clean...)
	sort.Strings(clean2)
	for _, p := range clean2 {
		fmt.Fprintf(w, "  no change: %s\n", p)
	}
	invalid2 := append([]string(nil), invalid...)
	sort.Strings(invalid2)
	for _, p := range invalid2 {
		fmt.Fprintf(w, "  invalid:   %s (not in tree or shadow)\n", p)
	}
	sort.SliceStable(rejected, func(i, j int) bool {
		return rejected[i].path < rejected[j].path
	})
	for _, r := range rejected {
		fmt.Fprintf(w, "  rejected:  %s — %s\n", r.path, r.reason)
	}
}
