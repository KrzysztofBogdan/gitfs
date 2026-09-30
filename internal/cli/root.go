// Package cli wires gfs commands to the engine.
package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence" // registers confluence://
)

// ExitError carries a non-zero exit code out of a command.
// Code 1: an action failed, was denied or conflicted. Code 2: usage/config error.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 2
}

// Version is set at release time via -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// version falls back to the module version so `go install ...@vX` reports it too.
func version() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return Version
}

// NewRoot builds the command tree. Subcommands are added by later tasks.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gfs",
		Short:         "gfs mirrors a net service as a directory of XML files",
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newClone(), newStatus(), newDiff(), newCommit(), newPull(), newResolve(), newLog(), newActions(), newGet(), newAuth(), newSchema(), newExample())
	root.AddCommand(helpTopics()...)
	return root
}

// Execute runs gfs with os.Args and returns the process exit code.
func Execute() int {
	err := NewRoot().Execute()
	if err != nil {
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Err != nil {
			fmt.Fprintln(os.Stderr, "gfs:", err)
		}
	}
	return exitCode(err)
}
