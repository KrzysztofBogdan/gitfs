package imap

import (
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

var providerHosts = map[string]string{
	"gmail.com":      "imap.gmail.com:993",
	"googlemail.com": "imap.gmail.com:993",
	"outlook.com":    "outlook.office365.com:993",
	"hotmail.com":    "outlook.office365.com:993",
	"live.com":       "outlook.office365.com:993",
	"msn.com":        "outlook.office365.com:993",
	"icloud.com":     "imap.mail.me.com:993",
	"me.com":         "imap.mail.me.com:993",
	"mac.com":        "imap.mail.me.com:993",
	"fastmail.com":   "imap.fastmail.com:993",
	"fastmail.fm":    "imap.fastmail.com:993",
	"yahoo.com":      "imap.mail.yahoo.com:993",
	"ymail.com":      "imap.mail.yahoo.com:993",
}

// resolveHost chooses the IMAP host for an account URL.
// Priority: ?host=... query param, then the provider map.
func resolveHost(u adapter.URL) (string, error) {
	if h, ok := u.Query["host"]; ok && h != "" {
		return h, nil
	}
	at := strings.LastIndexByte(u.Account, '@')
	if at < 0 {
		return "", fmt.Errorf(
			"imap:// account %q has no '@' domain; pass ?host=imap.example.com:993",
			u.Account)
	}
	domain := strings.ToLower(u.Account[at+1:])
	if h, ok := providerHosts[domain]; ok {
		return h, nil
	}
	return "", fmt.Errorf(
		"unknown IMAP host for domain %q; pass ?host=imap.example.com:993", domain)
}
