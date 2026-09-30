package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/atlassian"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func newAuth() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Manage API tokens stored in the system keyring"}
	auth.AddCommand(newAuthSet(), newAuthRm(), newAuthClear(), newAuthList())
	return auth
}

func tokenStore() creds.Store { return creds.Store{Dirs: creds.DefaultDirs()} }

// readToken reads without echo from a terminal, else the first line of input.
func readToken(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "API token: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func newAuthSet() *cobra.Command {
	var host, base string
	cmd := &cobra.Command{
		Use:   "set <email>",
		Short: "Store or replace the API token for an email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(args[0])
			if base != "" && host == "" {
				return usage("--base needs --host")
			}
			token, err := readToken(cmd)
			if err != nil {
				return err
			}
			if token == "" {
				return usage("empty token")
			}
			out := cmd.OutOrStdout()
			if host != "" {
				if base == "" {
					base = "https://" + host
				}
				name, err := atlassian.VerifyToken(cmd.Context(), base, email, token)
				if err != nil {
					return &ExitError{Code: 1, Err: fmt.Errorf("verify on %s: %w", host, err)}
				}
				fmt.Fprintf(out, "verified: %s on %s\n", name, host)
			}
			if err := tokenStore().Set(email, token); err != nil {
				return err
			}
			fmt.Fprintf(out, "stored: %s:%s\n", creds.Realm, email)
			if host != "" {
				g, err := creds.LoadGlobal(creds.DefaultDirs())
				if err != nil {
					return err
				}
				g.SetHostEmail(host, email)
				if err := g.Save(); err != nil {
					return err
				}
				fmt.Fprintf(out, "default for %s: %s\n", host, email)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "verify the token on this site and make the email its default")
	cmd.Flags().StringVar(&base, "base", "", "base URL instead of https://<host> (tests)")
	cmd.Flags().MarkHidden("base")
	return cmd
}

func newAuthRm() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <email>",
		Short: "Delete the stored API token for an email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := strings.ToLower(args[0])
			s := tokenStore()
			err := s.Delete(email)
			if errors.Is(err, creds.ErrNotFound) {
				if _, src, gerr := s.Get(email); gerr == nil && src == "alogin" {
					return &ExitError{Code: 1, Err: fmt.Errorf("%s is stored by alogin, not gfs; nothing removed", email)}
				}
				return &ExitError{Code: 1, Err: fmt.Errorf("no stored token for %s", email)}
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "removed: %s:%s\n", creds.Realm, email)
			g, err := creds.LoadGlobal(creds.DefaultDirs())
			if err != nil {
				return err
			}
			for _, h := range g.HostsFor(email) {
				fmt.Fprintf(out, "note: still the default for %s in %s\n", h, g.Path())
			}
			return nil
		},
	}
}

func newAuthClear() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete every API token gfs has stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			s := tokenStore()
			ids, _, err := s.Identities()
			if err != nil {
				return err
			}
			n := 0
			for _, id := range ids {
				if id.Source == "gfs" {
					n++
				}
			}
			if n == 0 {
				fmt.Fprintln(out, "no stored tokens")
				return nil
			}
			if !yes {
				ask := prompter()
				if ask == nil {
					return usage("refusing to delete %d stored tokens without a terminal; pass --yes", n)
				}
				if !ask(fmt.Sprintf("delete %d stored tokens?", n)) {
					fmt.Fprintln(out, "nothing deleted")
					return nil
				}
			}
			if n, err = s.Clear(); err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted %d stored tokens\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func newAuthList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored identities and the hosts that default to them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, warnings, err := tokenStore().Identities()
			if err != nil {
				return err
			}
			g, err := creds.LoadGlobal(creds.DefaultDirs())
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			out := cmd.OutOrStdout()
			if len(ids) == 0 {
				fmt.Fprintln(out, "no stored tokens")
				return nil
			}
			width := 0
			for _, id := range ids {
				width = max(width, len(creds.Realm)+1+len(id.Email))
			}
			for _, id := range ids {
				src := id.Source
				if id.Missing {
					src += " (missing)"
				}
				line := fmt.Sprintf("%-*s  %-14s", width, creds.Realm+":"+id.Email, src)
				if hosts := g.HostsFor(id.Email); len(hosts) > 0 {
					line += " default for " + strings.Join(hosts, ", ")
				}
				fmt.Fprintln(out, strings.TrimRight(line, " "))
			}
			return nil
		},
	}
}
