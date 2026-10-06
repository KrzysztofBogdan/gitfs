package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

// newTail is gfs tail (bitbucket spec §9): stream a running job's output,
// write its final state back, exit with its result.
func newTail() *cobra.Command {
	var wait, interval, timeout time.Duration
	cmd := &cobra.Command{
		Use:   "tail <run-or-pr-file>",
		Short: "Stream a running pipeline's logs and exit with its result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false) // watching takes no lock; writing back does
			if err != nil {
				return err
			}
			defer done()
			rel, err := env.Tree.Rel(args[0])
			if err != nil {
				return usage("%v", err)
			}
			entry, ok := env.Index.ByPath(rel)
			if !ok {
				return usage("%s is not tracked: gfs tail takes a pipeline run or pull request file", rel)
			}
			tl, ok := env.Session.(adapter.Tailer)
			if !ok {
				return usage("%s cannot tail", env.Adapter.Name())
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			res, terr := tl.Tail(ctx, entry.ID, adapter.TailOpts{Wait: wait, Interval: interval}, cmd.OutOrStdout())
			if res.RunID != "" {
				ids := []string{res.RunID}
				if res.RunID != entry.ID {
					ids = append(ids, entry.ID)
				}
				if err := writeBack(cmd, env, ids); err != nil && terr == nil {
					terr = err
				}
			}
			switch {
			case cmd.Context().Err() != nil:
				return &ExitError{Code: 130}
			case terr != nil:
				return &ExitError{Code: 2, Err: terr}
			case res.Result == "successful":
				return nil
			case res.Result == "":
				return &ExitError{Code: 2, Err: errors.New("timed out after " + timeout.String())}
			}
			return &ExitError{Code: 1}
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 2*time.Minute, "pull request file: how long to wait for a run to start on its source commit")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "log poll interval (the step list is polled every 3)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long (0: never)")
	return cmd
}

// writeBack pulls the watched resources into the tree, under the tree lock,
// with pull's lines on stderr so stdout holds only the log.
func writeBack(cmd *cobra.Command, env *engine.Env, ids []string) error {
	unlock, err := env.Tree.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	ix, err := env.Tree.LoadIndex() // another command may have written it while we watched
	if err != nil {
		return err
	}
	atts, err := env.Tree.LoadAttachments()
	if err != nil {
		return err
	}
	env.Index, env.Atts, env.Out = ix, atts, cmd.ErrOrStderr()
	_, err = engine.Pull(context.WithoutCancel(cmd.Context()), env, engine.PullOpts{IDs: ids})
	return err
}
