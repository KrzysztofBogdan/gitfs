package cloudns

import (
	"context"
	"net/url"
	"os"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

func init() { adapter.Register(&Adapter{}) }

// Adapter is cloudns://<auth-id> and cloudns://sub-<sub-auth-id>.
type Adapter struct{}

var (
	_ adapter.Normalizer    = (*Adapter)(nil)
	_ adapter.BaseDescriber = (*Adapter)(nil)
	_ adapter.Session       = (*session)(nil)
	_ adapter.Reporter      = (*session)(nil)
)

func (*Adapter) Name() string                 { return "cloudns" }
func (*Adapter) Schemes() []string            { return []string{"cloudns"} }
func (*Adapter) Schema() *schema.Schema       { return zoneSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Flat }
func (*Adapter) Verbs() []adapter.Verb        { return nil }
func (*Adapter) DefaultDir(u *url.URL) string { return defaultDir(u) }

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
