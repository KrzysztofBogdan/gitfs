// Package bitbucket mirrors Bitbucket Cloud pull requests and pipeline runs
// (bitbucket spec).
package bitbucket

import (
	"context"
	"net/url"
	"os"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

func init() { adapter.Register(&Adapter{}) }

// Adapter is bitbucket://bitbucket.org/<workspace>.
type Adapter struct{}

var _ adapter.Normalizer = (*Adapter)(nil)

func (*Adapter) Name() string                 { return "bitbucket" }
func (*Adapter) Schemes() []string            { return []string{"bitbucket"} }
func (*Adapter) Schema() *schema.Schema       { return mergedSchema }
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
	t, err := parseTarget(u, cfg, os.Getenv, creds.System{})
	if err != nil {
		return nil, err
	}
	return newSession(t), nil
}
