// Package workdir creates and manages the .gitfs/ workdir tree.
package workdir

import "path/filepath"

// Layout returns filesystem paths for the workdir's internal metadata.
type Layout struct{ Root string }

func (l Layout) Gitfs() string   { return filepath.Join(l.Root, ".gitfs") }
func (l Layout) Config() string { return filepath.Join(l.Gitfs(), "config.toml") }
func (l Layout) Shadow() string { return filepath.Join(l.Gitfs(), "shadow") }
func (l Layout) Log() string    { return filepath.Join(l.Gitfs(), "log") }
func (l Layout) Trash() string  { return filepath.Join(l.Gitfs(), "trash") }
func (l Layout) Head() string   { return filepath.Join(l.Gitfs(), "HEAD") }
