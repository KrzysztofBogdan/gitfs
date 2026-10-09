package ksef

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// key is the Ministry's public key for usage (KsefTokenEncryption,
// SymmetricKeyEncryption), read once per client.
func (c *Client) key(ctx context.Context, usage string) (*rsa.PublicKey, error) {
	if c.keys == nil {
		var certs []struct {
			Certificate string   `json:"certificate"`
			Usage       []string `json:"usage"`
		}
		if err := c.json(ctx, http.MethodGet, "/security/public-key-certificates", false, nil, &certs); err != nil {
			return nil, err
		}
		c.keys = map[string]*rsa.PublicKey{}
		for _, ct := range certs {
			der, err := base64.StdEncoding.DecodeString(ct.Certificate)
			if err != nil {
				continue
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}
			if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
				for _, u := range ct.Usage {
					c.keys[u] = pub
				}
			}
		}
	}
	if k := c.keys[usage]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("ksef: no public key for %s", usage)
}

type tokenInfo struct {
	Token      string    `json:"token"`
	ValidUntil time.Time `json:"validUntil"`
}

// Login authenticates with the KSeF token (ksef spec §7 steps 1-4).
func (c *Client) Login(ctx context.Context) error {
	pub, err := c.key(ctx, "KsefTokenEncryption")
	if err != nil {
		return err
	}
	var ch struct {
		Challenge   string `json:"challenge"`
		TimestampMs int64  `json:"timestampMs"`
	}
	if err := c.json(ctx, http.MethodPost, "/auth/challenge", false, nil, &ch); err != nil {
		return err
	}
	enc, err := encryptOAEP(pub, []byte(fmt.Sprintf("%s|%d", c.Token, ch.TimestampMs)))
	if err != nil {
		return err
	}
	var init struct {
		ReferenceNumber     string    `json:"referenceNumber"`
		AuthenticationToken tokenInfo `json:"authenticationToken"`
	}
	req := map[string]any{"challenge": ch.Challenge, "contextIdentifier": map[string]string{"type": "Nip", "value": c.NIP}, "encryptedToken": enc}
	if err := c.json(ctx, http.MethodPost, "/auth/ksef-token", false, req, &init); err != nil {
		return err
	}
	op := init.AuthenticationToken.Token
	wait := 500 * time.Millisecond
	deadline := c.Now().Add(2 * time.Minute)
	for {
		var st struct {
			Status struct {
				Code        int      `json:"code"`
				Description string   `json:"description"`
				Details     []string `json:"details"`
			} `json:"status"`
		}
		if err := c.withBearer(ctx, http.MethodGet, "/auth/"+init.ReferenceNumber, op, &st); err != nil {
			return err
		}
		if st.Status.Code == 200 {
			break
		}
		if st.Status.Code >= 400 {
			msg := st.Status.Description
			if len(st.Status.Details) > 0 {
				msg += ": " + strings.Join(st.Status.Details, "; ")
			}
			return &APIError{Status: http.StatusUnauthorized, Op: "login", Msg: msg, Hint: c.LoginHint}
		}
		if c.Now().After(deadline) {
			return &APIError{Op: "login", Msg: "authentication did not finish in 2 minutes"}
		}
		if err := c.Sleep(ctx, wait); err != nil {
			return err
		}
		wait = min(2*wait, 5*time.Second)
	}
	var tok struct {
		AccessToken  tokenInfo `json:"accessToken"`
		RefreshToken tokenInfo `json:"refreshToken"`
	}
	if err := c.withBearer(ctx, http.MethodPost, "/auth/token/redeem", op, &tok); err != nil {
		return err
	}
	c.accessTok, c.accessUntil, c.refreshTok = tok.AccessToken.Token, tok.AccessToken.ValidUntil, tok.RefreshToken.Token
	return nil
}

// access is a live access token: it logs in the first time and refreshes
// when less than a minute is left (ksef spec §7).
func (c *Client) access(ctx context.Context) (string, error) {
	switch {
	case c.accessTok == "":
		if err := c.Login(ctx); err != nil {
			return "", err
		}
	case c.Now().Add(time.Minute).After(c.accessUntil):
		var r struct {
			AccessToken tokenInfo `json:"accessToken"`
		}
		if err := c.withBearer(ctx, http.MethodPost, "/auth/token/refresh", c.refreshTok, &r); err == nil {
			c.accessTok, c.accessUntil = r.AccessToken.Token, r.AccessToken.ValidUntil
		} else if err := c.Login(ctx); err != nil {
			return "", err
		}
	}
	return c.accessTok, nil
}
