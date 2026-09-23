package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/textdiff"
)

func newDiff() *cobra.Command {
	return &cobra.Command{
		Use:   "diff [<path>...]",
		Short: "Show resolved actions and the canonical diff against base",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cs, pol, err := computeChanges(env, args)
			if err != nil {
				return err
			}
			s := env.Adapter.Schema()
			w := cmd.OutOrStdout()
			for _, c := range cs {
				printChange(w, c, pol)
				var before, after string
				if c.Base != nil {
					before = string(envelope.Bytes(c.Base, s))
				}
				switch {
				case c.Status == 'D':
				case c.Local != nil:
					after = string(envelope.Bytes(c.Local, s))
				default:
					raw, _ := env.Tree.ReadFile(c.Path)
					after = string(raw)
				}
				basePath := c.Path
				if c.OldPath != "" {
					basePath = c.OldPath
				}
				fmt.Fprint(w, textdiff.Unified("a/"+basePath, "b/"+c.Path, textdiff.Lines(before), textdiff.Lines(after)))
			}
			return nil
		},
	}
}
