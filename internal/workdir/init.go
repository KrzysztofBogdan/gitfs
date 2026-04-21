package workdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

// Init creates <dir>/.gitfs/ and writes config.toml with the remote URL.
// No HEAD file is written here — the caller writes HEAD after a
// successful fetch (spec §8.1 step 6).
func Init(dir string, url adapter.URL) error {
	l := Layout{Root: dir}
	for _, d := range []string{l.Gitfs(), l.Shadow(), l.Log(), l.Trash()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	cfg := fmt.Sprintf("[remote]\nurl = %q\n", url.Raw)
	return writeFileAtomic(l.Config(), []byte(cfg), 0o644)
}

// WriteHEAD writes .gitfs/HEAD with the adapter's opaque token.
func WriteHEAD(dir string, head []byte) error {
	return writeFileAtomic(Layout{Root: dir}.Head(), head, 0o644)
}

// writeFileAtomic writes via temp file + rename so a crash cannot leave a
// half-written file.
func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gitfs-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// DirEmpty reports whether dir does not exist, or exists and has no
// entries. A non-directory entry at dir returns an error.
func DirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return len(entries) == 0, nil
}

// Exists reports whether the path exists (file or dir).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir reports whether the path is a directory.
func IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// FindRoot walks up from start looking for a directory containing an
// .gitfs/ subdirectory. Returns that directory, or an error if none is
// found before the filesystem root.
func FindRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	dir := abs
	for {
		if IsDir(filepath.Join(dir, ".gitfs")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in an gitfs workdir (no .gitfs/ found in . or parents)")
		}
		dir = parent
	}
}

// ValidatePath rejects adapter-supplied paths that are absolute, contain
// ".." segments, have a .gitfs prefix, or are empty.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("absolute path not allowed: %q", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path escapes workdir: %q", p)
	}
	if clean == ".gitfs" || strings.HasPrefix(clean, ".gitfs/") {
		return fmt.Errorf("path in reserved .gitfs/: %q", p)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return fmt.Errorf("path escapes workdir: %q", p)
		}
	}
	return nil
}
