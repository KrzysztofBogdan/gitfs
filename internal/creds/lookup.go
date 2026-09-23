package creds

// Lookup is what an adapter needs to resolve an identity (spec §5).
type Lookup interface {
	Token(email string) (string, error) // ErrNotFound when nothing is stored
	HostEmail(host string) (string, error)
	SoleIdentity() string // "" unless exactly one identity is known
}

// System uses the real keyring and config dirs, resolved on every call so
// tests can redirect HOME and XDG_CONFIG_HOME.
type System struct{}

func (System) Token(email string) (string, error) {
	tok, _, err := Store{Dirs: DefaultDirs()}.Get(email)
	return tok, err
}

func (System) HostEmail(host string) (string, error) {
	g, err := LoadGlobal(DefaultDirs())
	if err != nil {
		return "", err
	}
	return g.HostEmail(host), nil
}

// SoleIdentity ignores keyring errors: it is the last resort for the email,
// and a broken keyring is reported by the token lookup that follows.
func (System) SoleIdentity() string {
	ids, _, err := Store{Dirs: DefaultDirs()}.Identities()
	if err != nil {
		return ""
	}
	var live []string
	for _, id := range ids {
		if !id.Missing {
			live = append(live, id.Email)
		}
	}
	if len(live) == 1 {
		return live[0]
	}
	return ""
}
