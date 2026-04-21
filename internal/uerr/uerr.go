// Package uerr defines the error type that maps to CLI exit code 2.
// Lives in its own package so both internal/cli and internal/clone can
// reference it without forming an import cycle.
package uerr

import "fmt"

// UserError maps to CLI exit code 2 (bad URL, unknown scheme, non-empty
// dir, missing required URL part).
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Errorf is a sprintf-style constructor for *UserError.
func Errorf(format string, a ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, a...)}
}

// Is reports whether err is a *UserError.
func Is(err error) bool {
	_, ok := err.(*UserError)
	return ok
}
