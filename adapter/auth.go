package adapter

// AuthStrategy is how an adapter obtains credentials when none are cached.
type AuthStrategy int

const (
	// AuthNone — adapter needs no credentials (public query services).
	AuthNone AuthStrategy = iota
	// AuthTokenPaste — prompt the user to paste a token on a TTY.
	AuthTokenPaste
	// AuthOAuth — reserved; not used by v1 built-ins.
	AuthOAuth
)

// Credentials is an opaque JSON blob. gitfs stores/retrieves verbatim and
// never inspects it. Each adapter defines its own shape.
type Credentials []byte

// Persist is true when the returned credentials should be written to the
// OS keyring. Env-derived credentials return false.
type Persist bool
