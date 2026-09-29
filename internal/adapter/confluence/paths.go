package confluence

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/attach"
)

type pageRef struct {
	ID, Title, Parent string
	Space             string // space id
	Version           int
}

func sanitize(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "untitled"
	}
	if s[0] == '.' {
		s = "_" + s[1:]
	}
	if strings.HasSuffix(s, attach.SidecarSuffix) {
		s += "_" // a page folder must never look like a sidecar
	}
	return s
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func pagePaths(spaceDir string, pages []pageRef) map[string]string {
	byID := map[string]pageRef{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	// depth-limited parent resolution; cycles and orphans become top-level
	parentOf := func(p pageRef) string {
		seen := 0
		for cur := p; ; {
			par, ok := byID[cur.Parent]
			if !ok {
				break
			}
			if seen++; seen > 64 || par.ID == p.ID {
				return ""
			}
			cur = par
		}
		if _, ok := byID[p.Parent]; ok {
			return p.Parent
		}
		return ""
	}
	children := map[string][]pageRef{}
	for _, p := range pages {
		children[parentOf(p)] = append(children[parentOf(p)], p)
	}
	out := map[string]string{}
	var walk func(parent, dir string, depth int)
	walk = func(parent, dir string, depth int) {
		kids := children[parent]
		sort.Slice(kids, func(i, j int) bool { return idLess(kids[i].ID, kids[j].ID) })
		used := map[string]int{}
		for _, k := range kids {
			name := sanitize(k.Title)
			key := strings.ToLower(name)
			used[key]++
			if n := used[key]; n > 1 {
				name = fmt.Sprintf("%s (%d)", name, n)
			}
			out[k.ID] = dir + "/" + name + ".xml"
			if depth < 64 {
				walk(k.ID, dir+"/"+name, depth+1)
			}
		}
	}
	walk("", spaceDir, 0)
	for _, p := range pages { // cycle members never reached from the top
		if _, ok := out[p.ID]; !ok {
			out[p.ID] = spaceDir + "/" + sanitize(p.Title) + " (" + p.ID + ").xml"
		}
	}
	return out
}

func parentPath(p string) string {
	dir := path.Dir(p)
	if !strings.Contains(dir, "/") {
		return ""
	}
	return dir + ".xml"
}
