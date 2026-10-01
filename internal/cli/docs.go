package cli

import (
	"encoding/json"
	"fmt"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/cloudns"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/jira"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ovh"
	"io/fs"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence"
)

func doc(name string) string {
	b, err := gitfs.Docs.ReadFile(name)
	if err != nil {
		return "missing built-in document " + name
	}
	return string(b)
}

// helpTopics are commands without Run: gfs help <topic> prints their Long text.
func helpTopics() []*cobra.Command {
	return []*cobra.Command{
		{Use: "start", Short: "Overview of gfs commands, files, policy and credentials", Long: doc("start.md")},
		{Use: "confluence", Short: "Confluence Cloud: URL, layout, page files, what each change does", Long: doc("docs/confluence.md")},
		{Use: "jira", Short: "Jira Cloud and service desk portals: URLs, files, transitions, what each change does", Long: doc("docs/jira.md")},
		{Use: "dns", Short: "DNS zones at OVH and ClouDNS: URLs, credentials, zone files, what each change does", Long: doc("docs/dns.md")},
		{Use: "confluence-storage", Short: "Verified Confluence storage-format examples for page bodies", Long: doc("docs/confluence/storage.md")},
	}
}

func onlyConfluence(name string) error {
	if name != "confluence" {
		return usage("unknown adapter %q: want confluence", name)
	}
	return nil
}

func newSchema() *cobra.Command {
	return &cobra.Command{
		Use:   "schema <adapter>",
		Short: "Print a RELAX NG schema for the adapter's files (xmllint --relaxng)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "confluence":
				fmt.Fprint(cmd.OutOrStdout(), confluence.EmbeddedVocabulary().RelaxNG())
			case "jira":
				fmt.Fprint(cmd.OutOrStdout(), jira.RelaxNG())
			case "ovh":
				fmt.Fprint(cmd.OutOrStdout(), ovh.RelaxNG())
			case "cloudns":
				fmt.Fprint(cmd.OutOrStdout(), cloudns.RelaxNG())
			default:
				return usage("unknown adapter %q: want confluence, jira, ovh or cloudns", args[0])
			}
			return nil
		},
	}
}

func newExample() *cobra.Command {
	return &cobra.Command{
		Use:   "example <adapter> [<node>[/<variant>]]",
		Short: "List or print verified body examples",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := onlyConfluence(args[0]); err != nil {
				return err
			}
			sub, err := fs.Sub(gitfs.Docs, "docs/confluence/examples")
			if err != nil {
				return err
			}
			ex, err := confluence.LoadExamples(sub)
			if err != nil {
				return err
			}
			status := map[string]string{}
			json.Unmarshal([]byte(doc("docs/confluence/roundtrip.json")), &status)
			w := cmd.OutOrStdout()
			nodes := confluence.Nodes(ex)
			if len(args) == 1 {
				for _, n := range nodes {
					var vs []string
					for _, e := range ex {
						if e.Node == n {
							vs = append(vs, e.Variant)
						}
					}
					fmt.Fprintf(w, "%-24s %s\n", n, strings.Join(vs, " "))
				}
				return nil
			}
			node, variant, _ := strings.Cut(args[1], "/")
			var matched []confluence.Example
			var variants []string
			for _, e := range ex {
				if e.Node == node {
					variants = append(variants, e.Variant)
					if variant == "" || e.Variant == variant {
						matched = append(matched, e)
					}
				}
			}
			switch {
			case len(variants) == 0:
				return usage("unknown node %q; nodes: %s", node, strings.Join(nodes, ", "))
			case len(matched) == 0:
				sort.Strings(variants)
				return usage("unknown variant %q of %s; variants: %s", variant, node, strings.Join(variants, ", "))
			case variant != "":
				fmt.Fprintln(w, matched[0].Body)
				return nil
			}
			for i, e := range matched {
				if i > 0 {
					fmt.Fprintln(w)
				}
				st := status[e.Key()]
				if st == "" {
					st = "untested"
				}
				fmt.Fprintf(w, "<!-- %s (%s): %s -->\n%s\n", e.Key(), st, e.Comment, e.Body)
			}
			return nil
		},
	}
}
