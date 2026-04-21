package cli

import (
	"github.com/KrzysztofBogdan/gitfs/internal/pull"
	"github.com/spf13/cobra"
)

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "fetch updates from the service",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return UserErrorf("pull takes no arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return pull.Run(cmd.Context(), pull.Args{}, pull.Deps{})
		},
	}
}
