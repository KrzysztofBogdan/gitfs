// Package imap is the built-in IMAP adapter.
package imap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

func init() {
	adapter.Register("imap", func(u adapter.URL) (adapter.Adapter, error) {
		if u.Account == "" {
			return nil, fmt.Errorf(
				"imap:// URL must include an account (imap://me@example.com)")
		}
		host, err := resolveHost(u)
		if err != nil {
			return nil, err
		}
		return &Adapter{account: u.Account, host: host}, nil
	})
}

// Adapter implements adapter.Adapter for IMAPS on port 993.
type Adapter struct {
	account string
	host    string

	// discovery is test-injectable; production callers get the default
	// net/http/SMTP-backed dependencies.
	discovery discoveryDeps
}

// Info returns public adapter metadata.
func (a *Adapter) Info() adapter.Info {
	return adapter.Info{
		Scheme:       "imap",
		AuthStrategy: adapter.AuthTokenPaste,
		AuthPrompt:   "IMAP app password",
	}
}

// credsBlob is the serialized form of an IMAP credential. SMTP fields are
// added lazily on first successful commit per the imap-smtp-credentials
// design — missing fields in an older blob are valid (additive migration).
type credsBlob struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Password string `json:"password"`

	// SMTP cache — populated after a successful end-to-end SMTP exchange.
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUser     string `json:"smtp_user,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	SMTPMode     string `json:"smtp_mode,omitempty"` // "starttls" | "tls"
}

// smtpPasswordOrShared returns smtp_password if present, else password.
func (b credsBlob) smtpPasswordOrShared() string {
	if b.SMTPPassword != "" {
		return b.SMTPPassword
	}
	return b.Password
}

// Authenticate resolves credentials: env first, then interactive prompt.
func (a *Adapter) Authenticate(
	_ context.Context, prior adapter.Credentials, io adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	if prior != nil {
		return prior, false, nil
	}
	if pw, ok := envPasswordFor(a.account); ok {
		blob, _ := json.Marshal(credsBlob{
			Type: "basic", Username: a.account, Password: pw,
		})
		return blob, false, nil
	}
	if !io.IsTTY() {
		return nil, false, adapter.ErrNeedsInteractive
	}
	pw, err := io.ReadSecret(fmt.Sprintf(
		"IMAP app password for %s "+
			"(create an app password in your provider's security settings): ",
		a.account))
	if err != nil {
		return nil, false, err
	}
	pw = strings.TrimSpace(pw)
	blob, _ := json.Marshal(credsBlob{
		Type: "basic", Username: a.account, Password: pw,
	})
	return blob, true, nil
}

// Pull does a full refresh: IMAP's Fetch walks the mailbox and emits
// every visible message. The core classifies unchanged paths as applied
// no-ops. Incremental pull (via HIGHESTMODSEQ / UIDVALIDITY) is left
// for a later revision.
func (a *Adapter) Pull(
	ctx context.Context, creds adapter.Credentials, _ []byte, emit adapter.PullEmitter,
) ([]byte, error) {
	return adapter.FullRefreshPull(ctx, a.Fetch, creds, emit)
}

// envPasswordFor looks up GITFS_IMAP_<ACCOUNT_SLUG>_PASSWORD. Per
// imap-smtp-credentials §5 the credentials design describes this as
// GITFS_..._TOKEN — the clone-implementation design still calls the
// IMAP field PASSWORD, and that is what the rest of gitfs already
// exports. We accept both for forward compatibility.
func envPasswordFor(account string) (string, bool) {
	slug := envAccountSlug(account)
	if v, ok := os.LookupEnv("GITFS_IMAP_" + slug + "_PASSWORD"); ok {
		return v, true
	}
	if v, ok := os.LookupEnv("GITFS_IMAP_" + slug + "_TOKEN"); ok {
		return v, true
	}
	return "", false
}

// envAccountSlug uppercases and replaces non-alnum runs with '_'.
func envAccountSlug(s string) string {
	var b strings.Builder
	lastUnder := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnder = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
			lastUnder = false
		default:
			if !lastUnder {
				b.WriteByte('_')
				lastUnder = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
