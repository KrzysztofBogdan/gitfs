// Package workdir owns the working tree on disk and the .gfs/ directory.
package workdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const Dir = ".gfs"

var ErrNotInTree = errors.New("not a gfs working tree (or any parent): .gfs not found")

type Tree struct{ Root string }

func Find(start string) (*Tree, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, Dir)); err == nil && st.IsDir() {
			return &Tree{Root: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotInTree
		}
		dir = parent
	}
}

func Init(dir string, cfg *Config) (*Tree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if ents, err := os.ReadDir(abs); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("%s exists and is not empty", dir)
	}
	t := &Tree{Root: abs}
	if err := os.MkdirAll(filepath.Join(abs, Dir, "base"), 0o755); err != nil {
		return nil, err
	}
	for _, f := range []string{"index", "log"} {
		if err := os.WriteFile(filepath.Join(abs, Dir, f), nil, 0o644); err != nil {
			return nil, err
		}
	}
	return t, t.SaveConfig(cfg)
}

func (t *Tree) gfs(name string) string { return filepath.Join(t.Root, Dir, name) }

func (t *Tree) Abs(rel string) string { return filepath.Join(t.Root, filepath.FromSlash(rel)) }

func (t *Tree) Rel(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(t.Root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the working tree", path)
	}
	r = filepath.ToSlash(r)
	if r == Dir || strings.HasPrefix(r, Dir+"/") {
		return "", fmt.Errorf("%s is inside %s/", path, Dir)
	}
	if r == "." {
		r = ""
	}
	return r, nil
}

func (t *Tree) ReadFile(rel string) ([]byte, error)     { return os.ReadFile(t.Abs(rel)) }
func (t *Tree) WriteFile(rel string, data []byte) error { return writeAtomic(t.Abs(rel), data) }
func (t *Tree) Exists(rel string) bool {
	_, err := os.Stat(t.Abs(rel))
	return err == nil
}

func (t *Tree) Remove(rel string) error {
	if err := os.Remove(t.Abs(rel)); err != nil {
		return err
	}
	prune(filepath.Dir(t.Abs(rel)), t.Root)
	return nil
}

func (t *Tree) Rename(from, to string) error { return renameIn(t.Abs(from), t.Abs(to), t.Root) }

func (t *Tree) Scan() ([]string, error) {
	var out []string
	err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == filepath.Join(t.Root, Dir) {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			r, _ := filepath.Rel(t.Root, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gfs-tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func renameIn(from, to, root string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	prune(filepath.Dir(from), root)
	return nil
}

// prune removes empty directories from dir upwards, stopping before root.
func prune(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
