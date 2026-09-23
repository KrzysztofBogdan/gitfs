package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// AttEntry is one fetched attachment (attachments spec 3.3): the version and
// content last synced, and where its bytes are now.
type AttEntry struct {
	ResID, AttID, Version, SHA string
	Size, MTime                int64
	Path                       string
}

// Attachments is the set of fetched attachments, keyed by resource and attachment id.
type Attachments struct{ byKey map[string]AttEntry }

func NewAttachments() *Attachments { return &Attachments{byKey: map[string]AttEntry{}} }

func attKey(resID, attID string) string { return resID + "\x00" + attID }

func (a *Attachments) Get(resID, attID string) (AttEntry, bool) {
	e, ok := a.byKey[attKey(resID, attID)]
	return e, ok
}

func (a *Attachments) ByPath(p string) (AttEntry, bool) {
	for _, e := range a.byKey {
		if e.Path == p {
			return e, true
		}
	}
	return AttEntry{}, false
}

func (a *Attachments) ForResource(resID string) []AttEntry {
	var out []AttEntry
	for _, e := range a.All() {
		if e.ResID == resID {
			out = append(out, e)
		}
	}
	return out
}

func (a *Attachments) Put(e AttEntry)             { a.byKey[attKey(e.ResID, e.AttID)] = e }
func (a *Attachments) Delete(resID, attID string) { delete(a.byKey, attKey(resID, attID)) }

func (a *Attachments) All() []AttEntry {
	out := make([]AttEntry, 0, len(a.byKey))
	for _, e := range a.byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (t *Tree) LoadAttachments() (*Attachments, error) {
	a := NewAttachments()
	data, err := os.ReadFile(t.gfs("attachments"))
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 7)
		if len(f) != 7 {
			return nil, fmt.Errorf(".gfs/attachments line %d: want 7 tab-separated fields", n)
		}
		size, err1 := strconv.ParseInt(f[4], 10, 64)
		mtime, err2 := strconv.ParseInt(f[5], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf(".gfs/attachments line %d: size and mtime must be integers", n)
		}
		a.Put(AttEntry{ResID: f[0], AttID: f[1], Version: f[2], SHA: f[3], Size: size, MTime: mtime, Path: f[6]})
	}
	return a, sc.Err()
}

func (t *Tree) SaveAttachments(a *Attachments) error {
	var b bytes.Buffer
	for _, e := range a.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", e.ResID, e.AttID, e.Version, e.SHA, e.Size, e.MTime, e.Path)
	}
	return writeAtomic(t.gfs("attachments"), b.Bytes())
}
