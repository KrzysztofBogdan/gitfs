package cloudns

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

var _ adapter.Loginer = (*Adapter)(nil)

// Login guides the user to a ClouDNS API user's password, checks it and
// stores it under cloudns:<host> (DNS spec §8.2).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	base := defaultBase
	if b := io.Flags["base"]; b != "" {
		base = b
	}
	a := Auth{}
	kind := "auth-id"
	if id, ok := strings.CutPrefix(n.Host, "sub-"); ok {
		a.SubAuthID, kind = id, "sub-auth-id"
	} else {
		a.AuthID = n.Host
	}
	fmt.Fprint(io.Out, `ClouDNS API users are managed at https://www.cloudns.net/api-settings/
  (a sub-user can be limited to chosen zones; recommended: cloudns://sub-<id>)
`)
	if a.Password, err = io.ReadSecret(fmt.Sprintf("Password for %s %s: ", kind, strings.TrimPrefix(n.Host, "sub-"))); err != nil {
		return err
	}
	c := NewClient(base, a)
	if err := c.Do(ctx, "login", nil, nil); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on cloudns://%s: %w", n.Host, err)}
	}
	s := &session{c: c, t: target{host: n.Host}}
	zs, err := s.zones(ctx)
	if err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("listing zones on cloudns://%s: %w", n.Host, err)}
	}
	fmt.Fprintf(io.Out, "verified: %d zones visible\n", len(zs))
	v, _ := json.Marshal(map[string]string{"password": a.Password})
	if err := (creds.Store{Dirs: creds.DefaultDirs()}).SetEntry(entryName(n.Host), string(v)); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "stored in keyring: gfs %s\n", entryName(n.Host))
	return nil
}
