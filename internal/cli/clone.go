package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newClone() *cobra.Command {
	return &cobra.Command{
		Use:   "clone <url> [<dir>]",
		Short: "Create a working tree from a remote",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ad, u, err := adapter.ForURL(args[0])
			if err != nil {
				return usage("%v", err)
			}
			dir := ad.DefaultDir(u)
			if len(args) == 2 {
				dir = args[1]
			}
			sess, err := ad.Open(cmd.Context(), u, nil)
			if err != nil {
				return err
			}
			defer sess.Close()
			_, err = engine.Clone(cmd.Context(), ad, sess, args[0], dir, cmd.OutOrStdout())
			return err
		},
	}
}
