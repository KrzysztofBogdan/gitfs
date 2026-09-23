package creds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Global is $XDG_CONFIG_HOME/gfs/config: [host "<host>"] email = <email>.
type Global struct {
	path string
	cfg  *workdir.Config
}

func LoadGlobal(d Dirs) (*Global, error) {
	path := filepath.Join(d.Config, "config")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Global{path: path, cfg: workdir.NewConfig()}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := workdir.ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Global{path: path, cfg: cfg}, nil
}

func (g *Global) Path() string { return g.path }

func hostSection(host string) string { return fmt.Sprintf("host %q", strings.ToLower(host)) }

func (g *Global) HostEmail(host string) string { return g.cfg.Get(hostSection(host), "email") }

func (g *Global) SetHostEmail(host, email string) { g.cfg.Set(hostSection(host), "email", email) }

// HostsFor returns, sorted, the hosts whose default email is email.
func (g *Global) HostsFor(email string) []string {
	var hosts []string
	for _, sec := range g.cfg.Sections() {
		var h string
		if _, err := fmt.Sscanf(sec, "host %q", &h); err != nil {
			continue
		}
		if strings.EqualFold(g.cfg.Get(sec, "email"), email) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (g *Global) Save() error { return workdir.WriteAtomic(g.path, g.cfg.Bytes()) }
