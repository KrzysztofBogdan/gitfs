package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/spf13/cobra"
)

// Main is the process entrypoint used by cmd/gitfs. Returns the desired
// exit code (0, 1, or 2) — see CLI UX §4.3.
func Main(args []string) int {
	root := NewRoot()
	root.SetArgs(args)
	// Silence default cobra error/usage printing; we handle formatting.
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.ExecuteContext(context.Background())
	return mapExitCode(err)
}

// NewRoot builds the top-level cobra command with every v1 verb wired
// in. Verbs other than `clone` print a hint and exit 2.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gitfs",
		Short:         "git-for-services: clone and edit internet services as files",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetHelpTemplate(helpTemplate())
	root.AddCommand(newCloneCmd())
	root.AddCommand(notYetImplemented("status", "show working tree status"))
	root.AddCommand(notYetImplemented("diff", "show local changes vs shadow"))
	root.AddCommand(notYetImplemented("log", "show adapter-defined history"))
	root.AddCommand(newPullCmd())
	root.AddCommand(newCommitCmd())
	root.AddCommand(notYetImplemented("restore", "revert local changes to shadow"))

	for _, n := range gitUnknownVerbs {
		root.AddCommand(unknownVerbHint(n.name, n.hint))
	}
	return root
}

func notYetImplemented(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			return UserErrorf("%s: not implemented in v1", name)
		},
	}
}

type verbHint struct{ name, hint string }

var gitUnknownVerbs = []verbHint{
	{"push", "commit sends; there is no push."},
	{"fetch", "use `gitfs pull`."},
	{"remote", "edit `.gitfs/config.toml`; credentials live in the OS keyring."},
	{"checkout", "use `gitfs restore <path>` to revert local changes; branches and history rewrites are not supported."},
	{"revert", "use `gitfs restore <path>` to revert local changes; branches and history rewrites are not supported."},
	{"branch", "not supported; services are the source of truth."},
	{"merge", "not supported; services are the source of truth."},
	{"stash", "not supported; services are the source of truth."},
	{"reset", "not supported; services are the source of truth."},
	{"tag", "not supported; services are the source of truth."},
	{"add", "commit takes file paths directly; there is no staging."},
}

func unknownVerbHint(name, hint string) *cobra.Command {
	return &cobra.Command{
		Use:    name,
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return UserErrorf("%s: %s", name, hint)
		},
	}
}

func helpTemplate() string {
	return `gitfs — git for services

Usage:
  gitfs <verb> [args...]

Verbs:
  clone    clone a service into a local workdir
  status   show working tree status (not implemented in v1)
  diff     show local changes vs shadow (not implemented in v1)
  log      show adapter-defined history (not implemented in v1)
  pull     fetch updates from the service
  commit   send local changes to the service
  restore  revert local changes to shadow (not implemented in v1)

Installed adapters: {{installedAdapters}}
`
}

// installedAdaptersLine returns the sorted list for the help footer.
func installedAdaptersLine() string {
	inst := adapter.Installed()
	if len(inst) == 0 {
		return "(none)"
	}
	return strings.Join(inst, ", ")
}

// mapExitCode converts a returned error into the spec's exit code table.
func mapExitCode(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "gitfs: "+err.Error())
	if IsUserError(err) {
		return 2
	}
	if errors.Is(err, adapter.ErrNeedsInteractive) {
		return 1
	}
	return 1
}

func init() {
	cobra.AddTemplateFunc("installedAdapters", installedAdaptersLine)
}
