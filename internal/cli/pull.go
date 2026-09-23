package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newPull() *cobra.Command {
	var o engine.PullOpts
	cmd := &cobra.Command{
		Use:   "pull [<path>...]",
		Short: "Bring the working tree up to date with the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			if o.Filter, err = changes.PathFilter(env.Tree, args); err != nil {
				return usage("%v", err)
			}
			r, err := engine.Pull(cmd.Context(), env, o)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
	cmd.Flags().BoolVar(&o.Force, "force", false, "replace conflicted files with the remote version")
	return cmd
}
