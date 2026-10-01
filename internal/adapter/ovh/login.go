package ovh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/httpx"
)

var _ adapter.Loginer = (*Adapter)(nil)

// accessRule is one right of a consumer key.
type accessRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// dnsRules are the rights gfs asks for: DNS zones only (DNS spec §8.1).
var dnsRules = []accessRule{{"GET", "/domain/zone"}, {"GET", "/domain/zone/*"}, {"POST", "/domain/zone/*"}, {"PUT", "/domain/zone/*"}, {"DELETE", "/domain/zone/*"}}

var (
	loginSleep   = httpx.SleepCtx
	loginTimeout = 10 * time.Minute
	pollEvery    = 3 * time.Second
)

func within(r accessRule) bool {
	return r.Path == "/domain/zone" || strings.HasPrefix(r.Path, "/domain/zone/")
}

// Login guides the user to an OVH consumer key for DNS zones and stores the
// three keys under ovh:<endpoint> (DNS spec §8.1).
func (*Adapter) Login(ctx context.Context, u *url.URL, io adapter.LoginIO) error {
	n, err := normalize(u)
	if err != nil {
		return err
	}
	ep := n.Host
	base := endpoints[ep]
	if b := io.Flags["base"]; b != "" {
		base = b
	}
	site := strings.TrimSuffix(endpoints[ep], "/1.0")
	store := creds.Store{Dirs: creds.DefaultDirs()}
	var cr Creds
	if v, err := store.GetEntry(entryName(ep)); err == nil {
		json.Unmarshal([]byte(v), &cr)
	}
	out := io.Out
	if io.Flags["paste"] == "true" {
		fmt.Fprintf(out, "Paste the three keys of an existing OVH token (made at %s/createToken).\n", site)
		if cr.AppKey, err = io.ReadLine("Application key: "); err != nil {
			return err
		}
		if cr.AppSecret, err = io.ReadSecret("Application secret: "); err != nil {
			return err
		}
		if cr.ConsumerKey, err = io.ReadSecret("Consumer key: "); err != nil {
			return err
		}
		c := NewClient(base, cr)
		var cur struct {
			Status string
			Rules  []accessRule
		}
		if err := c.Do(ctx, http.MethodGet, "/auth/currentCredential", nil, &cur); err != nil {
			return &adapter.LoginRefused{Err: fmt.Errorf("checking the keys on ovh://%s: %w", ep, err)}
		}
		var wide []string
		for _, r := range cur.Rules {
			if !within(r) {
				wide = append(wide, r.Method+" "+r.Path)
			}
		}
		if len(wide) > 0 {
			fmt.Fprintf(out, "warning: this key reaches beyond DNS zones: %s\n", strings.Join(wide, ", "))
		}
		return verifyAndStore(ctx, out, store, ep, base, cr)
	}
	if cr.AppKey == "" || cr.AppSecret == "" {
		fmt.Fprintf(out, `OVH needs an application key and secret, made once per person.
  1. Open %s/createApp (log in with your OVH account)
  2. Application name: gfs   Description: anything
  3. Copy the two values it shows.
`, site)
		if cr.AppKey, err = io.ReadLine("Application key: "); err != nil {
			return err
		}
		if cr.AppSecret, err = io.ReadSecret("Application secret: "); err != nil {
			return err
		}
		if cr.AppKey == "" || cr.AppSecret == "" {
			return errors.New("both the application key and secret are needed")
		}
	}
	c := NewClient(base, Creds{AppKey: cr.AppKey})
	var created struct {
		ConsumerKey   string `json:"consumerKey"`
		ValidationURL string `json:"validationUrl"`
	}
	body := map[string]any{"accessRules": dnsRules, "redirection": "https://github.com/KrzysztofBogdan/gitfs"}
	if err := c.DoApp(ctx, http.MethodPost, "/auth/credential", body, &created); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("asking OVH for a consumer key: %w", err)}
	}
	fmt.Fprintln(out, "\nAsking OVH for access to DNS zones only:")
	for _, r := range dnsRules {
		fmt.Fprintf(out, "  %-6s %s\n", r.Method, r.Path)
	}
	fmt.Fprintf(out, "Open this link, log in, choose how long the key lasts and press \"Authorize\":\n  %s\n", created.ValidationURL)
	io.OpenURL(created.ValidationURL)
	fmt.Fprintln(out, "Waiting for approval… (Ctrl-C to stop)")
	cr.ConsumerKey = created.ConsumerKey
	c = NewClient(base, cr)
	for waited := time.Duration(0); ; waited += pollEvery {
		var cur struct{ Status string }
		if err := c.Do(ctx, http.MethodGet, "/auth/currentCredential", nil, &cur); err != nil {
			return err
		}
		switch cur.Status {
		case "validated":
			return verifyAndStore(ctx, out, store, ep, base, cr)
		case "refused", "expired":
			return &adapter.LoginRefused{Err: fmt.Errorf("the consumer key was %s on OVH's page; nothing stored", cur.Status)}
		}
		if waited >= loginTimeout {
			return &adapter.LoginRefused{Err: fmt.Errorf("the consumer key was not approved within %s; run gfs auth login ovh://%s again", loginTimeout, ep)}
		}
		if err := loginSleep(ctx, pollEvery); err != nil {
			return err
		}
	}
}

func verifyAndStore(ctx context.Context, out interface{ Write([]byte) (int, error) }, store creds.Store, ep, base string, cr Creds) error {
	var zones []string
	if err := NewClient(base, cr).Do(ctx, http.MethodGet, "/domain/zone", nil, &zones); err != nil {
		return &adapter.LoginRefused{Err: fmt.Errorf("verify on ovh://%s: %w", ep, err)}
	}
	fmt.Fprintf(out, "verified: %d zones visible on ovh://%s\n", len(zones), ep)
	v, _ := json.Marshal(cr)
	if err := store.SetEntry(entryName(ep), string(v)); err != nil {
		return err
	}
	fmt.Fprintf(out, "stored in keyring: gfs %s\n", entryName(ep))
	return nil
}
