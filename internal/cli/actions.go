package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
)

func newActions() *cobra.Command {
	return &cobra.Command{
		Use:   "actions",
		Short: "List the verbs this remote understands and their policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cfg, err := env.Tree.LoadConfig()
			if err != nil {
				return err
			}
			pol, err := policy.FromConfig(cfg.Section("policy"))
			if err != nil {
				return err
			}
			verbs := []adapter.Verb{
				{Name: "create", Class: "create", Help: "new file"},
				{Name: "update", Class: "update", Help: "changed element or sub-resource"},
				{Name: "delete", Class: "delete", Help: "removed file or sub-resource"},
				{Name: "move", Class: "move", Help: "moved or renamed file"},
			}
			verbs = append(verbs, env.Adapter.Verbs()...)
			for _, v := range verbs {
				params := "-"
				if len(v.Params) > 0 {
					params = strings.Join(v.Params, ",")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", v.Name, pol.Level(v.Class), params, v.Help)
			}
			return nil
		},
	}
}
