// Package cli wires gfs commands to the engine.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence" // registers confluence://
	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/jira"       // registers jira://
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

// NewRoot builds the command tree. Subcommands are added by later tasks.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gfs",
		Short:         "gfs mirrors a net service as a directory of XML files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newClone(), newStatus(), newDiff(), newCommit(), newPull(), newResolve(), newLog(), newActions(), newGet(), newAuth(), newSchema(), newExample())
	root.AddCommand(helpTopics()...)
	return root
}

// Execute runs gfs with os.Args and returns the process exit code.
func Execute() int {
	// Ctrl-C cancels the context instead of killing the process, so clone and
	// pull save the index before they exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := NewRoot().ExecuteContext(ctx)
	if err != nil {
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Err != nil {
			fmt.Fprintln(os.Stderr, "gfs:", err)
		}
	}
	return exitCode(err)
}
