package creds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// alogin (forge-cli) keeps profiles in ~/.alogin.json and each profile's
// {email, token, accountId} in the keyring under service "alogin". Read only.
type aloginProfile struct{ Name, Email string }

// aloginProfiles returns no profiles when the file is missing or unreadable,
// and an error only when it does not parse.
func aloginProfiles(home string) ([]aloginProfile, error) {
	path := filepath.Join(home, ".alogin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var ps []aloginProfile
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ps, nil
}

func aloginToken(home, email string) (string, bool) {
	ps, _ := aloginProfiles(home)
	for _, p := range ps {
		if !strings.EqualFold(p.Email, email) {
			continue
		}
		raw, err := keyring.Get("alogin", p.Name)
		if err != nil {
			return "", false
		}
		var c struct {
			Token string `json:"token"`
		}
		if json.Unmarshal([]byte(raw), &c) != nil || c.Token == "" {
			return "", false
		}
		return c.Token, true
	}
	return "", false
}
