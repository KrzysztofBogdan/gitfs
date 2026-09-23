package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Entry struct{ ID, Version, Path string }

type Index struct {
	Cursor string
	byID   map[string]Entry
}

func (ix *Index) ByID(id string) (Entry, bool) {
	e, ok := ix.byID[id]
	return e, ok
}

func (ix *Index) ByPath(path string) (Entry, bool) {
	for _, e := range ix.byID {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func (ix *Index) Put(e Entry)      { ix.byID[e.ID] = e }
func (ix *Index) Delete(id string) { delete(ix.byID, id) }

func (ix *Index) All() []Entry {
	out := make([]Entry, 0, len(ix.byID))
	for _, e := range ix.byID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (t *Tree) LoadIndex() (*Index, error) {
	data, err := os.ReadFile(t.gfs("index"))
	if err != nil {
		return nil, err
	}
	ix := &Index{byID: map[string]Entry{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if c, ok := strings.CutPrefix(line, "# cursor "); ok && n == 1 {
			ix.Cursor = c
			continue
		}
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf(".gfs/index line %d: want id<TAB>version<TAB>path", n)
		}
		ix.Put(Entry{f[0], f[1], f[2]})
	}
	return ix, sc.Err()
}

func (t *Tree) SaveIndex(ix *Index) error {
	var b bytes.Buffer
	if ix.Cursor != "" {
		fmt.Fprintf(&b, "# cursor %s\n", ix.Cursor)
	}
	for _, e := range ix.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.ID, e.Version, e.Path)
	}
	return writeAtomic(t.gfs("index"), b.Bytes())
}
