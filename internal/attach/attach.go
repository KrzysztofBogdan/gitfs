// Package attach holds the shared attachment rules (attachments spec 3, 5):
// sidecar paths, derived file names, conflict copies and change detection.
package attach

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// SidecarSuffix names the folder next to a resource file that holds its attachments.
const SidecarSuffix = ".files"

// SidecarDir returns the sidecar of a resource file: a/X.xml -> a/X.files.
func SidecarDir(resPath string) string { return strings.TrimSuffix(resPath, ".xml") + SidecarSuffix }

// ResourceOf returns the resource file owning a path directly inside a sidecar.
func ResourceOf(p string) (string, bool) {
	dir := path.Dir(p)
	if dir == "." || !strings.HasSuffix(dir, SidecarSuffix) {
		return "", false
	}
	return strings.TrimSuffix(dir, SidecarSuffix) + ".xml", true
}

// Sanitize makes a service file name safe as a single path segment.
func Sanitize(name string) string {
	var b strings.Builder
	for _, r := range name {
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
	if s == "" || s == "." || s == ".." {
		return "attachment"
	}
	if s[0] == '.' {
		s = "_" + s[1:] // no hidden files, no clash with .gfs-tmp-*
	}
	return s
}

// Elements returns the attachment elements of root by identity.
func Elements(root *xmltree.Node, e *schema.Elem) map[string]*xmltree.Node {
	out := map[string]*xmltree.Node{}
	if root == nil || e == nil {
		return out
	}
	for _, c := range root.ChildrenNamed(e.Name) {
		if id, ok := c.Attr(e.ID); ok {
			out[id] = c
		}
	}
	return out
}

// Version returns an attachment element's version, or "-" when there is none.
func Version(el *xmltree.Node, e *schema.Elem) string {
	if e.VersionAttr == "" {
		return "-"
	}
	if v, ok := el.Attr(e.VersionAttr); ok && v != "" {
		return v
	}
	return "-"
}

// Derive maps attachment id to sidecar path for every attachment element of
// root (attachments spec 3.2): sanitised name, optional prefix from the parent,
// de-duplicated in id order.
func Derive(root *xmltree.Node, e *schema.Elem, resPath string) map[string]string {
	out := map[string]string{}
	if root == nil || e == nil {
		return out
	}
	type item struct{ id, name string }
	var items []item
	for _, c := range root.ChildrenNamed(e.Name) {
		id, ok := c.Attr(e.ID)
		if !ok {
			continue
		}
		raw, _ := c.Attr(e.NameAttr)
		name := Sanitize(raw)
		if e.PrefixAttr != "" {
			if v, ok := root.Attr(e.PrefixAttr); ok && v != "" {
				name = v + "-" + name
			}
		}
		items = append(items, item{id, name})
	}
	sort.SliceStable(items, func(i, j int) bool { return idLess(items[i].id, items[j].id) })
	dir := SidecarDir(resPath)
	used := map[string]int{}
	for _, it := range items {
		key := strings.ToLower(it.name)
		used[key]++
		name := it.name
		if n := used[key]; n > 1 {
			name = numbered(name, n)
		}
		out[it.id] = dir + "/" + name
	}
	return out
}

func numbered(name string, n int) string {
	ext := path.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}

// idLess orders numeric ids numerically and anything else as strings.
func idLess(a, b string) bool {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// ConflictCopy names the remote copy written next to a conflicted attachment
// (attachments spec 5.3): stem.remote-v<N>.ext, or stem.remote.ext without versions.
func ConflictCopy(p, version string) string {
	ext := path.Ext(p)
	tag := ".remote"
	if version != "" && version != "-" {
		tag += "-v" + version
	}
	return strings.TrimSuffix(p, ext) + tag + ext
}

var copyRe = regexp.MustCompile(`^(.*)\.remote(?:-v[^.]*)?(\.[^.]*)?$`)

// ConflictOriginal returns the file name a conflict copy belongs to.
func ConflictOriginal(name string) (string, bool) {
	m := copyRe.FindStringSubmatch(name)
	if m == nil || m[1] == "" {
		return "", false
	}
	return m[1] + m[2], true
}
