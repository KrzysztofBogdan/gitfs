package cli

import "github.com/KrzysztofBogdan/gitfs/internal/uerr"

// UserErrorf re-exports uerr.Errorf so placeholder verbs can produce
// exit-2 errors without importing the uerr package directly.
func UserErrorf(format string, a ...any) error { return uerr.Errorf(format, a...) }

// IsUserError re-exports uerr.Is for the exit-code mapper.
func IsUserError(err error) bool { return uerr.Is(err) }
