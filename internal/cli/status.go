package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
)

func newStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status [<path>...]",
		Short: "Show changed files and the remote actions they resolve to",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, url, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cs, pol, err := computeChanges(env, args)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "On remote %s\n", url)
			if len(cs) == 0 {
				fmt.Fprintln(w, "nothing to commit, working tree matches the remote")
			}
			for _, c := range cs {
				printChange(w, c, pol)
			}
			return nil
		},
	}
}

func computeChanges(env *engine.Env, args []string) ([]changes.FileChange, *policy.Policy, error) {
	filter, err := changes.PathFilter(env.Tree, args)
	if err != nil {
		return nil, nil, usage("%v", err)
	}
	cfg, err := env.Tree.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	pol, err := policy.FromConfig(cfg.Section("policy"))
	if err != nil {
		return nil, nil, err
	}
	cs, err := changes.Compute(env.Tree, env.Index, env.Adapter, filter)
	return cs, pol, err
}

func mark(pol *policy.Policy, a adapter.Action) string {
	switch pol.Level(a.Class) {
	case policy.Ask:
		return "  [ask]"
	case policy.Deny:
		return "  [deny]"
	}
	return ""
}

func printChange(w io.Writer, c changes.FileChange, pol *policy.Policy) {
	if !c.Quiet {
		printResource(w, c, pol)
	}
	for _, a := range c.Attachments {
		detail := a.Note
		if a.Action != nil {
			detail = a.Action.Detail + mark(pol, *a.Action)
		}
		fmt.Fprintf(w, "  %c  %-40s %s\n", a.Status, a.Path, detail)
	}
}

func printResource(w io.Writer, c changes.FileChange, pol *policy.Policy) {
	var acts []adapter.Action
	for _, a := range c.Actions {
		if !a.IsAttachment() {
			acts = append(acts, a)
		}
	}
	p := c.Path
	if c.OldPath != "" {
		p = c.OldPath + " -> " + c.Path
	}
	detail := ""
	switch {
	case c.Status == 'C':
		detail = "unresolved conflict"
	case c.Err != nil:
		detail = "invalid: " + strings.ReplaceAll(c.Err.Error(), "\n", "; ")
	case c.Status == '!' && c.Local != nil && len(c.Local.Errors) > 0:
		detail = "failed: " + c.Local.Errors[0].Msg
	case len(acts) == 1:
		detail = acts[0].Detail + mark(pol, acts[0])
	}
	if c.Note != "" {
		detail = strings.TrimSpace(detail + "  (" + c.Note + ")")
	}
	fmt.Fprintf(w, "  %c  %-40s %s\n", c.Status, p, detail)
	for _, warn := range c.Warnings {
		fmt.Fprintf(w, "        warning: %s\n", warn)
	}
	if len(acts) > 1 && c.Err == nil {
		for _, a := range acts {
			fmt.Fprintf(w, "        %s%s\n", a.Detail, mark(pol, a))
		}
	}
}
