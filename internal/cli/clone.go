package cli

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newClone() *cobra.Command {
	var quiet bool
	cmd := &cobra.Command{
		Use:   "clone <url> [<dir>]",
		Short: "Create a working tree from a remote",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ad, u, err := adapter.ForURL(args[0])
			if err != nil {
				return usage("%v", err)
			}
			raw := args[0]
			if n, ok := ad.(adapter.Normalizer); ok {
				if raw, err = n.Normalize(u); err != nil {
					return usage("%v", err)
				}
				if u, err = url.Parse(raw); err != nil {
					return err
				}
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
			progress, out := progressBar(cmd, quiet).attach(sess, cmd.OutOrStdout())
			_, err = engine.Clone(cmd.Context(), ad, sess, raw, dir, out, progress)
			return err
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "no progress bar")
	return cmd
}
