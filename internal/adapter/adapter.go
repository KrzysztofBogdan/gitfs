// Package adapter is the contract between the gfs engine and a service.
package adapter

import (
	"context"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type PathModel int

const (
	Tree    PathModel = iota // path is meaningful; any rename is a move
	Flat                     // path derived from fields; local rename is a no-op
	DirTree                  // directories meaningful, file name derived
)

func IsMove(m PathModel, from, to string) bool {
	switch m {
	case Tree:
		return from != to
	case DirTree:
		return path.Dir(from) != path.Dir(to)
	}
	return false
}

type Resource struct {
	ID, Version, Path string
	By, At            string        // last modifier and time, for <conflict>
	Root              *xmltree.Node // nil in a listing stub
}

type Action struct {
	Verb     string // create, update, delete, move, or an explicit verb
	Class    string // policy class; defaults to Verb
	Target   string // "" for the resource itself, else e.g. comment[id=7] or comment[2]
	Group    string // update of a field group: element name
	Detail   string // human-readable resolved action
	Warning  string // printed before the action's ask and in a dry run, e.g. what a DNSSEC change can break
	From, To string // move
	Params   map[string]string
	File     string // attachment actions: sidecar path of the bytes; "" for resource actions
}

// IsAttachment reports whether a acts on an attachment (attachments spec 4.4).
func (a Action) IsAttachment() bool { return a.File != "" }

var attTargetRe = regexp.MustCompile(`^[\w:.-]+\[(?:id=([^\]]+)|file=(.+))\]$`)

// AttachmentTarget names an existing attachment: attachment[id=att9].
func AttachmentTarget(elem, id string) string { return elem + "[id=" + id + "]" }

// NewAttachmentTarget names a new attachment by its file: attachment[file=a.png].
func NewAttachmentTarget(elem, file string) string { return elem + "[file=" + file + "]" }

// ParseAttachmentTarget returns the attachment id, or the file name of a new one.
func ParseAttachmentTarget(t string) (id, file string, ok bool) {
	m := attTargetRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

type Verb struct {
	Name, Class, Help string
	Params            []string
}

type Listing struct {
	Resources []Resource
	Deleted   []string
	Full      bool
	// FullDirs are top-level folders this listing covers completely even when
	// Full is false: an indexed resource under one of them that the listing
	// omits was deleted on the remote.
	FullDirs []string
	Cursor   string
}

type ApplyRequest struct {
	Local    *Resource // content to make the remote look like; Path is the working path
	Base     *Resource // nil for create
	Actions  []Action
	Lock     string // remote version observed before Apply
	IDByPath func(path string) (string, bool)
	Open     func(rel string) (io.ReadCloser, error) // reads the sidecar file named by Action.File
	Files    []string                                // explicit verbs: the resource's sidecar files
	// Secret reads a secret without echo (a DynHost login's password);
	// nil without a terminal.
	Secret func(prompt string) (string, error)
}

type Result struct {
	Action  Action
	Err     error
	Code    string // service error code, e.g. HTTP status
	ID      string // create: new identity
	Detail  string // e.g. "v2 -> v3"
	Version string // attachment create/update: the attachment's new version
	// Partial: the action failed after changing the remote (a DNS type change
	// whose delete ran and create failed); the engine re-reads the resource.
	Partial bool
}

// AttachmentInfo describes downloaded attachment bytes.
type AttachmentInfo struct {
	Version string // "-" when the service has no versions
	Size    int64
}

var (
	ErrLock     = errors.New("remote version changed")
	ErrNotFound = errors.New("not found on remote")
)

type Adapter interface {
	Name() string
	Schemes() []string
	Schema() *schema.Schema
	PathModel() PathModel
	DefaultDir(u *url.URL) string
	Verbs() []Verb
	Describe(a *Action, local *Resource)
	Open(ctx context.Context, u *url.URL, cfg map[string]string) (Session, error)
}

type Session interface {
	List(ctx context.Context, cursor string) (Listing, error)
	Fetch(ctx context.Context, id string) (*Resource, error)
	Apply(ctx context.Context, req ApplyRequest) []Result
	Check(ctx context.Context, req ApplyRequest) []Result
	Download(ctx context.Context, resourceID, attachmentID string, w io.Writer) (AttachmentInfo, error)
	Close() error
}

// BaseDescriber is implemented by adapters whose descriptions need the
// remote's copy too (a DNS record delete names the record it removes).
// The engine calls it instead of Describe when the resource has a base.
type BaseDescriber interface {
	DescribeBase(a *Action, local, base *Resource)
}

// Identified is implemented by sessions that know which account they act as.
// Clone records the identity as [remote] email (credentials spec §6).
type Identified interface{ Identity() string }

// Progress is one step of a long operation, for display. Phase is "list",
// "pages" (Done of Total spaces listed), "fetch" (Done of Total resources
// downloaded), "wait" (Item says why and for how long) or "done".
type Progress struct {
	Phase       string
	Done, Total int
	Item        string // the space or path being worked on
}

// Reporter is implemented by sessions that report progress while listing,
// and retry waits at any time.
type Reporter interface{ SetProgress(func(Progress)) }

// Normalizer is implemented by adapters that accept several spellings of a
// remote. Clone records the canonical one as [remote] url.
type Normalizer interface {
	Normalize(u *url.URL) (string, error)
}

// Linter is implemented by adapters that can warn about content the service
// is not known to store unchanged (storage reference spec §3.2).
type Linter interface {
	Lint(root *xmltree.Node) []string
}

// Cacher is implemented by sessions that keep metadata between runs. The
// engine passes <tree>/.gfs/cache (possibly not yet created) before listing
// or applying; the session owns what it writes there.
type Cacher interface{ UseCache(dir string) }

// Advisor is implemented by sessions that can say what the user may do to one
// resource now (jira spec §8). local is the file's current content.
type Advisor interface {
	Available(ctx context.Context, id string, local *Resource) (Advice, error)
}

type Advice struct {
	State string // shown after the path, e.g. "status: In Progress"
	Items []Available
	Note  string // e.g. what the local <status> resolves to
}

type Available struct {
	Verb, Name, To string // "transition", "Resolve this issue", "Closed"
	Fields         []AvailableField
}

type AvailableField struct {
	Element  string // "resolution", "field[id=customfield_10040]"
	Required bool
	Allowed  []string // shown when there are at most 8
}

// Loginer is implemented by adapters that guide the user to credentials for
// a remote and store them (gfs auth login, DNS spec §8).
type Loginer interface {
	Login(ctx context.Context, u *url.URL, io LoginIO) error
}

// LoginIO is the terminal a Loginer talks through.
type LoginIO struct {
	Out        io.Writer
	ReadLine   func(prompt string) (string, error) // prompt printed, answer trimmed
	ReadSecret func(prompt string) (string, error) // not echoed on a terminal
	OpenURL    func(u string)                      // best effort; no-op without a terminal
	Flags      map[string]string                   // paste, base (tests)
}

// LoginRefused is a Login the service refused (bad credentials, approval
// not given): gfs exits 1, not 2.
type LoginRefused struct{ Err error }

func (e *LoginRefused) Error() string { return e.Err.Error() }
func (e *LoginRefused) Unwrap() error { return e.Err }
