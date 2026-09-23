package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
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
				if !c.Quiet {
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
				for _, a := range c.Attachments {
					if line := binaryDiff(env, c, a); line != "" {
						fmt.Fprintln(w, line)
					}
				}
			}
			return nil
		},
	}
}

// binaryDiff is git's notice for a changed attachment, with sizes and short
// hashes because no base bytes are kept (attachments spec 4.3).
func binaryDiff(env *engine.Env, c changes.FileChange, a changes.AttChange) string {
	now := func() string {
		sha, size, _, err := attach.Hash(env.Tree, a.Path)
		if err != nil {
			return "missing"
		}
		return fmt.Sprintf("%d B, %s", size, short(sha))
	}
	switch a.Status {
	case 'A':
		return fmt.Sprintf("Binary files /dev/null and b/%s (%s) differ", a.Path, now())
	case 'D':
		return fmt.Sprintf("Binary files a/%s and /dev/null differ", a.Path)
	case 'M', 'C':
		l, _ := env.Atts.Get(c.ID, a.AttID)
		return fmt.Sprintf("Binary files a/%s (%d B, %s) and b/%s (%s) differ", a.Path, l.Size, short(l.SHA), a.Path, now())
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
