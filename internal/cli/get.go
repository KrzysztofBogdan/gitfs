package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get <path>...",
		Short: "Download attachment bytes on demand",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usage("gfs get needs a path: a resource file, a folder or an attachment ('gfs get .' for everything)")
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
			r, err := engine.Get(cmd.Context(), env, rels)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
}
