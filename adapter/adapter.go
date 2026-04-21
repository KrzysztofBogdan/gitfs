// Package adapter is the public ABI for gitfs adapters.
//
// Third-party adapters import this package and register a Factory via
// Register in an init() function. The gitfs binary compiles them in by
// blank-importing the adapter module from cmd/gitfs/main.go.
package adapter

import (
	"context"
	"errors"
	"io"
	"time"
)

// URL is the parsed form of an gitfs URL: scheme://account[/path][?query].
// It is NOT an RFC 3986 URI — the account is a single opaque token and is
// never split into userinfo/host/port. See CLI UX §5 for the grammar.
type URL struct {
	Raw     string
	Scheme  string
	Account string
	Path    string
	Query   map[string]string
}

// String returns the original raw input.
func (u URL) String() string { return u.Raw }

// Adapter is the contract every service adapter implements.
type Adapter interface {
	Info() Info

	// Authenticate runs auth if needed. prior is the previously persisted
	// blob (or nil). Returns the blob to round-trip and a Persist flag:
	// env-derived credentials MUST return Persist=false so gitfs does not
	// write env-only creds to the keyring.
	Authenticate(ctx context.Context, prior Credentials, io IO) (Credentials, Persist, error)

	// Fetch drives the initial full reconciliation. The adapter calls
	// emit.File for every tracked path. Returns the opaque HEAD token on
	// success.
	Fetch(ctx context.Context, creds Credentials, emit Emitter) (head []byte, err error)

	// Pull proposes changes since the given opaque HEAD token. The adapter
	// calls emit.File for each path it wants updated (content) and
	// emit.Tombstone for each path it wants removed. Returns the new
	// opaque HEAD token (which gitfs only persists if every offered path
	// was applied without a skip). Full refresh is allowed — offer
	// everything as File; paths whose tree already matches shadow classify
	// as applied no-ops.
	Pull(ctx context.Context, creds Credentials, head []byte, emit PullEmitter) (newHead []byte, err error)

	// Commit sends a batch of per-path changes to the service. For each
	// request, the adapter calls emit.Accept with the canonical
	// post-commit state (possibly multi-path for renames/derivations) or
	// emit.Reject with a human-readable reason. The adapter may
	// parallelise per-path work; ordering between requests is not defined.
	// A non-nil error aborts the whole invocation before any tree writes.
	//
	// io is the same terminal handle passed to Authenticate. Adapters
	// that need to prompt the user for lazily-discovered credentials
	// (e.g. an SMTP password that differs from IMAP) use it; adapters
	// with no interactive needs ignore it. When io.IsTTY() is false and
	// the adapter cannot proceed without prompting, it should surface a
	// non-nil error that abort the whole invocation.
	Commit(ctx context.Context, creds Credentials, requests []CommitRequest, emit CommitEmitter, io IO) error
}

// Info describes an adapter's public properties.
type Info struct {
	Scheme       string
	AuthStrategy AuthStrategy
	AuthPrompt   string
}

// Emitter writes a working-tree path and its shadow mirror in lock-step.
// Implementations reject paths that escape the workdir, contain "..", or
// have a .gitfs prefix.
type Emitter interface {
	File(path string, content []byte) error
	// FileAt writes content like File, then sets mtime/atime on both the
	// working-tree and shadow copies. A zero mtime means "do not adjust" —
	// FileAt behaves identically to File.
	FileAt(path string, content []byte, mtime time.Time) error
}

// PullEmitter is the adapter→core callback during Pull. Adapters call
// File to propose a content update for a path, or Tombstone to propose
// a deletion. Neither call mutates the workdir directly — the core
// classifies each proposal against local state before applying.
type PullEmitter interface {
	File(path string, content []byte) error
	Tombstone(path string) error
}

// IO abstracts the adapter's terminal access so it can be stubbed in tests.
type IO interface {
	Stdout() io.Writer
	Stderr() io.Writer
	IsTTY() bool
	ReadLine(prompt string) (string, error)
	ReadSecret(prompt string) (string, error)
}

// Factory constructs an adapter for a parsed URL. gitfs calls this once
// per invocation.
type Factory func(url URL) (Adapter, error)

// ErrNeedsInteractive is returned when credentials are required but no
// TTY is available and no env-var override was found.
var ErrNeedsInteractive = errors.New("credentials needed but no TTY and no env override")
