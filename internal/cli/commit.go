package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newCommit() *cobra.Command {
	var o engine.CommitOpts
	var allow []string
	cmd := &cobra.Command{
		Use:   "commit [<path>...]",
		Short: "Make the remote look like the working tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			if o.Filter, err = changes.PathFilter(env.Tree, args); err != nil {
				return usage("%v", err)
			}
			o.Allow = map[string]bool{}
			for _, a := range allow {
				o.Allow[a] = true
			}
			r, err := engine.Commit(cmd.Context(), env, o)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "resolve and check everything, execute nothing")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "treat ask as allow for this policy class (repeatable)")
	cmd.Flags().BoolVar(&o.Force, "force", false, "answer yes to every ask (policy deny still refuses)")
	cmd.Flags().BoolVar(&o.NoMerge, "no-merge", false, "refuse files whose remote moved instead of merging")
	return cmd
}
