package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func usage(format string, a ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, a...)}
}

func prompter() func(string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	return func(q string) bool {
		fmt.Fprintf(os.Stdout, "%s [y/N] ", q)
		ans, _ := in.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes"
	}
}

// openEnv opens the working tree containing the cwd. Call the returned func when done.
func openEnv(cmd *cobra.Command, lock bool) (*engine.Env, string, func(), error) {
	t, err := workdir.Find(".")
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := t.LoadConfig()
	if err != nil {
		return nil, "", nil, err
	}
	raw := cfg.Get("remote", "url")
	ad, u, err := adapter.ForURL(raw)
	if err != nil {
		return nil, "", nil, err
	}
	ix, err := t.LoadIndex()
	if err != nil {
		return nil, "", nil, err
	}
	atts, err := t.LoadAttachments()
	if err != nil {
		return nil, "", nil, err
	}
	unlock := func() {}
	if lock {
		if unlock, err = t.Lock(); err != nil {
			return nil, "", nil, err
		}
	}
	sess, err := ad.Open(context.Background(), u, cfg.Section("remote"))
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	if c, ok := sess.(adapter.Cacher); ok {
		c.UseCache(filepath.Join(t.Root, workdir.Dir, "cache"))
	}
	if r, ok := sess.(adapter.Reporter); ok {
		r.SetProgress(waitNotice(cmd.ErrOrStderr())) // commit, get, …: retry waits as lines
	}
	env := &engine.Env{Tree: t, Index: ix, Atts: atts, Adapter: ad, Session: sess, Out: cmd.OutOrStdout(), Prompt: prompter(), Now: time.Now}
	return env, raw, func() { sess.Close(); unlock() }, nil
}

func exitFor(code int) error {
	if code == 0 {
		return nil
	}
	return &ExitError{Code: code}
}
