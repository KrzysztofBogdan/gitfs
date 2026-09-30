package cli

import (
	"fmt"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
)

func newActions() *cobra.Command {
	return &cobra.Command{
		Use:   "actions [<path>...]",
		Short: "List the verbs this remote understands, or what you can do to each file now",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			if len(args) > 0 {
				return printAdvice(cmd, env, args)
			}
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
			if el := env.Adapter.Schema().Attachment(); el != nil {
				ops := strings.Join(el.Ops, " ")
				if ops == "" {
					ops = "read-only"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "attachments  <%s>  %s\n", el.Name, ops)
			}
			return nil
		},
	}
}

// printAdvice asks the session, per file, what the user can do now (jira spec §8).
func printAdvice(cmd *cobra.Command, env *engine.Env, args []string) error {
	w := cmd.OutOrStdout()
	adv, ok := env.Session.(adapter.Advisor)
	failed := false
	for _, a := range args {
		rel, err := env.Tree.Rel(a)
		if err != nil {
			return usage("%v", err)
		}
		data, err := env.Tree.ReadFile(rel)
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		id, _ := doc.Content.Attr(env.Adapter.Schema().ID)
		switch {
		case id == "":
			fmt.Fprintf(w, "%s    not on the remote yet\n", rel)
			continue
		case !ok:
			fmt.Fprintf(w, "%s    no per-file actions for %s\n", rel, env.Adapter.Name())
			continue
		}
		advice, err := adv.Available(cmd.Context(), id, &adapter.Resource{ID: id, Path: rel, Root: doc.Content})
		if err != nil {
			fmt.Fprintf(w, "%s    %v\n", rel, err)
			failed = true
			continue
		}
		fmt.Fprintf(w, "%s    %s\n", rel, advice.State)
		for _, it := range advice.Items {
			fmt.Fprintf(w, "  %-10s  %-24s  -> %s\n", it.Verb, it.Name, it.To)
			for _, f := range it.Fields {
				switch {
				case f.Required && len(f.Allowed) > 0 && len(f.Allowed) <= 8:
					fmt.Fprintf(w, "                requires <%s>: %s\n", f.Element, strings.Join(f.Allowed, " | "))
				case f.Required:
					fmt.Fprintf(w, "                requires <%s>\n", f.Element)
				default:
					fmt.Fprintf(w, "                optional <%s>\n", f.Element)
				}
			}
		}
		if advice.Note != "" {
			fmt.Fprintf(w, "  note: %s\n", advice.Note)
		}
	}
	if failed {
		return &ExitError{Code: 2}
	}
	return nil
}
