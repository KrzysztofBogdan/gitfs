package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func newLog() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log [-n <count>] [<path>...]",
		Short: "Show what commits did on the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := workdir.Find(".")
			if err != nil {
				return err
			}
			filter, err := changes.PathFilter(t, args)
			if err != nil {
				return usage("%v", err)
			}
			es, err := t.ReadLog()
			if err != nil {
				return err
			}
			var kept []workdir.LogEntry
			for _, e := range es {
				if filter == nil || filter(e.Path) || (e.NewPath != "" && filter(e.NewPath)) {
					kept = append(kept, e)
				}
			}
			if n > 0 && len(kept) > n {
				kept = kept[len(kept)-n:]
			}
			for _, e := range kept {
				fmt.Fprintln(cmd.OutOrStdout(), e.String())
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "count", "n", 0, "show only the last n entries")
	return cmd
}
