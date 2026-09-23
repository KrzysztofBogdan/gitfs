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
	Cursor    string
}

type ApplyRequest struct {
	Local    *Resource // content to make the remote look like; Path is the working path
	Base     *Resource // nil for create
	Actions  []Action
	Lock     string // remote version observed before Apply
	IDByPath func(path string) (string, bool)
	Open     func(rel string) (io.ReadCloser, error) // reads the sidecar file named by Action.File
	Files    []string                                // explicit verbs: the resource's sidecar files
}

type Result struct {
	Action  Action
	Err     error
	Code    string // service error code, e.g. HTTP status
	ID      string // create: new identity
	Detail  string // e.g. "v2 -> v3"
	Version string // attachment create/update: the attachment's new version
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

// Identified is implemented by sessions that know which account they act as.
// Clone records the identity as [remote] email (credentials spec §6).
type Identified interface{ Identity() string }
