package ksef

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

var _ adapter.Loginer = (*Adapter)(nil)

func defaultStore(name, value string) error {
	return creds.Store{Dirs: creds.DefaultDirs()}.SetEntry(name, value)
}

// storeEntry keeps a keyring entry; tests replace it.
var storeEntry = defaultStore

// Login guides the user to a KSeF token, checks it and stores it under
// ksef:<host>/<nip> (ksef spec §7.1).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	nip := strings.Trim(n.Path, "/")
	base := apiBase[n.Host]
	if b := io.Flags["base"]; b != "" {
		base = b
	}
	if io.Flags["paste"] == "" {
		fmt.Fprintf(io.Out, `A KSeF token is generated in the KSeF taxpayer application (linked from https://ksef.podatki.gov.pl):
  1. log in with a trusted profile, e-signature or seal, in the context of NIP %s;
  2. generate a token with only the InvoiceRead permission (viewing invoices);
  3. copy it: KSeF shows it once.
`, nip)
	}
	tok, err := io.ReadSecret(fmt.Sprintf("KSeF token for NIP %s: ", nip))
	if err != nil {
		return err
	}
	c := NewClient(base, nip, strings.TrimSpace(tok))
	c.LoginHint = loginHint(n.Host, nip)
	if _, err := c.access(ctx); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on ksef://%s/%s: %w", n.Host, nip, err)}
	}
	q := map[string]any{"subjectType": "Subject1",
		"dateRange": map[string]any{"dateType": "PermanentStorage", "from": utc(c.Now().Add(-24 * time.Hour))}}
	if err := c.json(ctx, http.MethodPost, "/invoices/query/metadata?pageSize=10", true, q, nil); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("reading invoices on ksef://%s/%s: %w", n.Host, nip, err)}
	}
	fmt.Fprintf(io.Out, "verified: the token reads invoices of NIP %s\n", nip)
	if err := storeEntry(entryName(n.Host, nip), c.Token); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "stored in keyring: gfs %s\n", entryName(n.Host, nip))
	return nil
}
