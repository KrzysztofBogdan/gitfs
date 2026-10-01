package atlassian

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

// GitEmail is `git config user.email`, the email suggested when a site has
// none remembered; a variable for tests.
var GitEmail = func() string {
	out, err := exec.Command("git", "config", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Login guides the user to an API token for host and stores it the way
// `gfs auth set <email> --host <host>` does (DNS spec §8.3).
func Login(ctx context.Context, host string, io adapter.LoginIO) error {
	out := io.Out
	fmt.Fprint(out, `Atlassian needs your account email and an API token.
  1. Open https://id.atlassian.com/manage-profile/security/api-tokens
  2. Press "Create API token" (not "...with scopes": scoped tokens only work
     through api.atlassian.com, which gfs does not use)
  3. Name it gfs, pick an expiry (at most one year), copy the token.
`)
	g, err := creds.LoadGlobal(creds.DefaultDirs())
	if err != nil {
		return err
	}
	def := g.HostEmail(host)
	if def == "" {
		def = GitEmail()
	}
	prompt := "Email: "
	if def != "" {
		prompt = "Email [" + def + "]: "
	}
	email, err := io.ReadLine(prompt)
	if err != nil {
		return err
	}
	if email == "" {
		email = def
	}
	email = strings.ToLower(email)
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
		base = "https://" + host
	}
	name, err := VerifyToken(ctx, base, email, token)
	if err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on %s: %w", host, err)}
	}
	fmt.Fprintf(out, "verified: %s on %s\n", name, host)
	if err := (creds.Store{Dirs: creds.DefaultDirs()}).Set(email, token); err != nil {
		return err
	}
	fmt.Fprintf(out, "stored: %s:%s\n", creds.Realm, email)
	g.SetHostEmail(host, email)
	if err := g.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "default for %s: %s\n", host, email)
	return nil
}
