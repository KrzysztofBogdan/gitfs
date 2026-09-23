package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// racyWindow: a file modified this recently may change again within the same
// mtime tick, so its tracking line does not trust mtime (attachments spec 3.3).
const racyWindow = 2 * time.Second

// Hash returns the sha256 (hex), size and mtime (Unix ns) of a working file.
func Hash(t *workdir.Tree, rel string) (string, int64, int64, error) {
	f, err := t.Open(rel)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), st.ModTime().UnixNano(), nil
}

// LineMTime is the mtime to record for a file: its own, or 0 while it is racy.
func LineMTime(st os.FileInfo) int64 {
	if time.Since(st.ModTime()) < racyWindow {
		return 0
	}
	return st.ModTime().UnixNano()
}

// Changed reports whether the file at rel differs from what line synced.
// Equal size and mtime mean unchanged without reading the file.
func Changed(t *workdir.Tree, rel string, line workdir.AttEntry) (bool, error) {
	st, err := t.Stat(rel)
	if err != nil {
		return false, err
	}
	if line.MTime != 0 && st.Size() == line.Size && st.ModTime().UnixNano() == line.MTime {
		return false, nil
	}
	sha, _, _, err := Hash(t, rel)
	if err != nil {
		return false, err
	}
	return sha != line.SHA, nil
}

// Entry builds the tracking line for the file at rel as it is now.
func Entry(t *workdir.Tree, resID, attID, version, rel string) (workdir.AttEntry, error) {
	sha, size, _, err := Hash(t, rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	st, err := t.Stat(rel)
	if err != nil {
		return workdir.AttEntry{}, err
	}
	return workdir.AttEntry{ResID: resID, AttID: attID, Version: version, SHA: sha, Size: size, MTime: LineMTime(st), Path: rel}, nil
}
