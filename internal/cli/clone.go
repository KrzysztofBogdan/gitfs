package cli

import (
	"github.com/KrzysztofBogdan/gitfs/internal/clone"
	"github.com/spf13/cobra"
)

func newCloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clone <url> [<dir>]",
		Short: "clone a service into a local workdir",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return UserErrorf("clone takes 1 or 2 args: gitfs clone <url> [<dir>]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a := clone.Args{URL: args[0]}
			if len(args) == 2 {
				a.Dir = args[1]
			}
			return clone.Run(cmd.Context(), a, clone.Deps{})
		},
	}
}
