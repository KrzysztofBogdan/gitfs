// Package clone orchestrates `gitfs clone` per spec §8.1 / §8.2.
package clone

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/aurl"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
	"github.com/KrzysztofBogdan/gitfs/internal/uerr"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Args is the parsed CLI input for a clone call.
type Args struct {
	URL string
	Dir string // "" → derived from URL via slugification
}

// Deps exposes the collaborators used by Run. All fields are optional;
// production defaults are used when a field is nil.
type Deps struct {
	Store  creds.Store
	IO     adapter.IO
	Stdout io.Writer
}

// Run executes the clone flow. Returns *uerr.UserError for exit-2
// conditions, any other error for exit 1.
func Run(ctx context.Context, args Args, deps Deps) error {
	url, err := aurl.Parse(args.URL)
	if err != nil {
		return uerr.Errorf("bad url: %v", err)
	}

	factory, ok := adapter.Lookup(url.Scheme)
	if !ok {
		installed := adapter.Installed()
		return uerr.Errorf(
			"unknown scheme %q. installed adapters: %s",
			url.Scheme, strings.Join(installed, ", "),
		)
	}

	dir := resolveDir(args.Dir, url)
	preExists, err := dirPreCheck(dir)
	if err != nil {
		return err
	}

	ad, err := factory(url)
	if err != nil {
		return err
	}

	store := deps.Store
	if store == nil {
		store = creds.NewKeyringOrNoop()
	}
	ioT := deps.IO
	if ioT == nil {
		ioT = defaultIO()
	}
	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	key := url.Scheme + ":" + url.Account
	prior, _, _ := store.Get(key)

	var blob adapter.Credentials = prior
	if prior == nil && ad.Info().AuthStrategy != adapter.AuthNone {
		newBlob, persist, err := ad.Authenticate(ctx, nil, ioT)
		if err != nil {
			return err
		}
		if persist {
			if err := store.Put(key, newBlob); err != nil {
				fmt.Fprintln(os.Stderr,
					"gitfs: could not save credentials to keyring:", err)
			}
		}
		blob = newBlob
	}

	if err := workdir.Init(dir, url); err != nil {
		return err
	}
	createdDir := !preExists

	emit := workdir.NewEmitter(dir)
	head, err := ad.Fetch(ctx, blob, emit)
	if err != nil {
		rollback(dir, createdDir, emit)
		return err
	}

	if err := workdir.WriteHEAD(dir, head); err != nil {
		rollback(dir, createdDir, emit)
		return err
	}

	fmt.Fprintf(stdout, "Cloned %s into %s (%d files).\n",
		url.Raw, dir, emit.Count())
	return nil
}

// resolveDir returns args.Dir if non-empty, otherwise the slugified
// account+path (CLI UX §5.2).
func resolveDir(argDir string, url adapter.URL) string {
	if argDir != "" {
		return argDir
	}
	slug := aurl.Slugify(url.Account, url.Path)
	if slug == "" {
		slug = url.Scheme
	}
	return slug
}

// dirPreCheck returns (preExisting, error). If the dir exists and is
// non-empty, returns *uerr.UserError (exit 2). Missing dir and empty dir
// are both valid states.
func dirPreCheck(dir string) (bool, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.IsDir() {
		return false, uerr.Errorf("%s exists and is not a directory", dir)
	}
	empty, err := workdir.DirEmpty(dir)
	if err != nil {
		return false, err
	}
	if !empty {
		return false, uerr.Errorf("%s exists and is non-empty", dir)
	}
	return true, nil
}

// rollback reverses filesystem state after a fetch failure (spec §8.2).
func rollback(dir string, createdDir bool, emit *workdir.Emitter) {
	if createdDir {
		_ = os.RemoveAll(dir)
		return
	}
	emit.RemoveWritten()
	_ = os.RemoveAll(workdir.Layout{Root: dir}.Gitfs())
}
