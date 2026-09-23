package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newResolve() *cobra.Command {
	var ours, theirs bool
	cmd := &cobra.Command{
		Use:   "resolve (--ours | --theirs) <path>...",
		Short: "Resolve conflicted files by picking one side",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ours == theirs {
				return usage("exactly one of --ours or --theirs is required")
			}
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			var rels []string
			for _, a := range args {
				r, err := env.Tree.Rel(a)
				if err != nil {
					return usage("%v", err)
				}
				rels = append(rels, r)
			}
			return engine.Resolve(cmd.Context(), env, rels, ours)
		},
	}
	cmd.Flags().BoolVar(&ours, "ours", false, "keep the local side")
	cmd.Flags().BoolVar(&theirs, "theirs", false, "keep the remote side")
	return cmd
}
