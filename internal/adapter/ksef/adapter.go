package ksef

import (
	"context"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

func init() { adapter.Register(&Adapter{}) }

// Adapter is ksef://<host>/<nip>.
type Adapter struct{}

var (
	_ adapter.Normalizer = (*Adapter)(nil)
	_ adapter.Placer     = (*Adapter)(nil)
	_ adapter.ReadOnly   = (*Adapter)(nil)
)

const readOnlyMsg = "KSeF invoices are read-only"

func (*Adapter) Name() string                 { return "ksef" }
func (*Adapter) Schemes() []string            { return []string{"ksef"} }
func (*Adapter) Schema() *schema.Schema       { return invoiceSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }
func (*Adapter) ReadOnly() string             { return readOnlyMsg }

func (*Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	a.Class = a.Verb
	a.Detail = readOnlyMsg
}

func (*Adapter) Normalize(u *url.URL) (string, error) {
	n, err := normalize(u)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, os.Getenv, creds.Store{Dirs: creds.DefaultDirs()})
	if err != nil {
		return nil, err
	}
	return newSession(t), nil
}

// ResourceDir, AttachmentPath and ResourceOf place the originals (ksef spec
// §2.1, §4): metadata under _gfs/, each original at the same path without it.
func (*Adapter) ResourceDir() string { return roleRoot }

func (*Adapter) AttachmentPath(resPath, name string) string {
	return path.Dir(strings.TrimPrefix(resPath, roleRoot+"/")) + "/" + name
}

var placedRe = regexp.MustCompile(`^(issued|received|third-party)/[0-9]{4}/[0-9]{2}/[^/]+\.(xml|pdf)$`)

// ResourceOf maps an original or its PDF to the metadata file beside which
// it is placed (ksef pdf spec §5.3).
func (*Adapter) ResourceOf(p string) (string, bool) {
	if !placedRe.MatchString(p) {
		return "", false
	}
	return roleRoot + "/" + strings.TrimSuffix(p, path.Ext(p)) + ".xml", true
}
