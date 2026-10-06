package bitbucket

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

var _ adapter.Loginer = (*Adapter)(nil)

const tokenPage = "https://id.atlassian.com/manage-profile/security/api-tokens"

// scopes are what gfs reads and writes (bitbucket spec §3.1).
var scopes = []string{"read:user:bitbucket", "read:workspace:bitbucket", "read:repository:bitbucket",
	"read:pullrequest:bitbucket", "write:pullrequest:bitbucket", "read:pipeline:bitbucket"}

// Login guides the user to a scoped Bitbucket API token, checks it and
// stores it as the keyring entry bitbucket:<email> (bitbucket spec §3.1).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	ws := strings.Trim(n.Path, "/")
	out := io.Out
	fmt.Fprintf(out, `Bitbucket needs your Atlassian account email and an API token with Bitbucket scopes.
  1. Open %s
  2. Press "Create API token with scopes", name it gfs, pick an expiry, choose Bitbucket
  3. Tick: %s
  4. Copy the token. It is not your Jira/Confluence token; gfs keeps it apart.
`, tokenPage, strings.Join(scopes, ", "))
	io.OpenURL(tokenPage)
	g, err := creds.LoadGlobal(creds.DefaultDirs())
	if err != nil {
		return err
	}
	def := g.HostEmail(host)
	if def == "" {
		def = atlassian.GitEmail()
	}
	prompt := "Email: "
	if def != "" {
		prompt = "Email [" + def + "]: "
	}
	email, err := io.ReadLine(prompt)
	if err != nil {
		return err
	}
	if email = strings.ToLower(strings.TrimSpace(email)); email == "" {
		email = strings.ToLower(def)
	}
	if email == "" {
		return fmt.Errorf("no email given")
	}
	token, err := io.ReadSecret("API token: ")
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no token given")
	}
	base := io.Flags["base"]
	if base == "" {
		base = defaultBase
	}
	c := atlassian.New(atlassian.Target{Base: strings.TrimRight(base, "/"), Email: email, Token: token}, "Bitbucket")
	var me apiUser
	if err := c.Do(ctx, "GET", "/user", nil, &me); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on %s: %w", host, err)}
	}
	if err := c.Do(ctx, "GET", "/repositories/"+url.PathEscape(ws)+"?pagelen=1", nil, nil); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("workspace %s: %w (the token needs read:repository:bitbucket and access to %s)", ws, err, ws)}
	}
	fmt.Fprintf(out, "verified: %s on %s, workspace %s\n", me.DisplayName, host, ws)
	if err := (creds.Store{Dirs: creds.DefaultDirs()}).SetEntry(tokenEntry(email), token); err != nil {
		return err
	}
	fmt.Fprintf(out, "stored in keyring: %s\n", tokenEntry(email))
	g.SetHostEmail(host, email)
	if err := g.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "default for %s: %s\n", host, email)
	return nil
}
