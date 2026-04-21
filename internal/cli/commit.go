package cli

import (
	"github.com/KrzysztofBogdan/gitfs/internal/commit"
	"github.com/spf13/cobra"
)

func newCommitCmd() *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "commit [path...]",
		Short: "send local changes to the service",
		RunE: func(cmd *cobra.Command, args []string) error {
			return commit.Run(cmd.Context(), commit.Args{
				Paths:   args,
				Message: message,
			}, commit.Deps{})
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "",
		"adapter-defined message (Jira/Linear comment; email Subject; etc.)")
	return cmd
}
