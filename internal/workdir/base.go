package workdir

import (
	"os"
	"path/filepath"
)

func (t *Tree) baseRoot() string { return t.gfs("base") }
func (t *Tree) basePath(rel string) string {
	return filepath.Join(t.baseRoot(), filepath.FromSlash(rel))
}

func (t *Tree) ReadBase(rel string) ([]byte, error)     { return os.ReadFile(t.basePath(rel)) }
func (t *Tree) WriteBase(rel string, data []byte) error { return writeAtomic(t.basePath(rel), data) }
func (t *Tree) RenameBase(from, to string) error {
	return renameIn(t.basePath(from), t.basePath(to), t.baseRoot())
}
func (t *Tree) RemoveBase(rel string) error {
	err := os.Remove(t.basePath(rel))
	if err == nil {
		prune(filepath.Dir(t.basePath(rel)), t.baseRoot())
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
