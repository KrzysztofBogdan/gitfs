package workdir

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Emitter writes each emitted path to both the working tree and its
// shadow mirror. One Emitter per clone; not safe for concurrent use
// (adapters call File serially from Fetch).
type Emitter struct {
	root    string
	written []string
	count   int
}

// NewEmitter returns an Emitter rooted at dir.
func NewEmitter(dir string) *Emitter { return &Emitter{root: dir} }

// File writes content to <root>/<path> and <root>/.gitfs/shadow/<path>.
func (e *Emitter) File(path string, content []byte) error {
	return e.FileAt(path, content, time.Time{})
}

// FileAt writes content to <root>/<path> and <root>/.gitfs/shadow/<path>,
// then sets mtime/atime on both. A zero mtime leaves the write-time mtime.
func (e *Emitter) FileAt(path string, content []byte, mtime time.Time) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	l := Layout{Root: e.root}
	workPath := filepath.Join(e.root, filepath.FromSlash(path))
	shadowPath := filepath.Join(l.Shadow(), filepath.FromSlash(path))
	if err := writeFileAtomic(workPath, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", workPath, err)
	}
	if err := writeFileAtomic(shadowPath, content, 0o644); err != nil {
		return fmt.Errorf("write shadow %s: %w", shadowPath, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(workPath, mtime, mtime); err != nil {
			return fmt.Errorf("chtimes %s: %w", workPath, err)
		}
		if err := os.Chtimes(shadowPath, mtime, mtime); err != nil {
			return fmt.Errorf("chtimes shadow %s: %w", shadowPath, err)
		}
	}
	e.written = append(e.written, path)
	e.count++
	return nil
}

// Written returns the relative paths emitted so far, in call order.
func (e *Emitter) Written() []string { return e.written }

// Count returns the number of successfully emitted paths.
func (e *Emitter) Count() int { return e.count }

// Root returns the workdir root this Emitter writes under.
func (e *Emitter) Root() string { return e.root }

// RemoveWritten removes every emitted path (working tree + shadow) and
// prunes now-empty parent directories up to — but not including — the
// workdir root. Used by the rollback path.
func (e *Emitter) RemoveWritten() {
	l := Layout{Root: e.root}
	for _, p := range e.written {
		work := filepath.Join(e.root, filepath.FromSlash(p))
		shadow := filepath.Join(l.Shadow(), filepath.FromSlash(p))
		_ = os.Remove(work)
		_ = os.Remove(shadow)
		pruneUp(filepath.Dir(work), e.root)
		pruneUp(filepath.Dir(shadow), l.Shadow())
	}
}

// pruneUp removes empty directories walking from dir up to (but not
// including) stopAt. Silently ignores errors and non-empty dirs.
func pruneUp(dir, stopAt string) {
	for dir != stopAt && dir != "." && dir != string(filepath.Separator) {
		if err := os.Remove(dir); err != nil {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
