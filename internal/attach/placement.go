package attach

import (
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

// Placement says where a resource's attachment files live: in its X.files/
// sidecar (attachments spec 3.2), or where the adapter's Placer puts them
// (ksef spec §2.1).
type Placement struct{ p adapter.Placer }

// PlacementOf is ad's placement; anything that is not a Placer gets X.files/.
func PlacementOf(ad any) Placement {
	p, _ := ad.(adapter.Placer)
	return Placement{p: p}
}

// Placed reports whether attachments live outside X.files/.
func (pl Placement) Placed() bool { return pl.p != nil }

// IsResource reports whether a scanned file is a resource file.
func (pl Placement) IsResource(p string) bool {
	return pl.p == nil || strings.HasPrefix(p, pl.p.ResourceDir()+"/")
}

// Dir is the folder holding resPath's attachment files. A placed folder may
// be shared by many resources.
func (pl Placement) Dir(resPath string) string {
	if pl.p == nil {
		return SidecarDir(resPath)
	}
	return path.Dir(pl.p.AttachmentPath(resPath, "x"))
}

// Path is where the attachment file called name of resPath lives.
func (pl Placement) Path(resPath, name string) string {
	if pl.p == nil {
		return SidecarDir(resPath) + "/" + name
	}
	return pl.p.AttachmentPath(resPath, name)
}

// ResourceOf is the resource file an attachment path belongs to.
func (pl Placement) ResourceOf(p string) (string, bool) {
	if pl.p == nil {
		return ResourceOf(p)
	}
	return pl.p.ResourceOf(p)
}

// Owns reports whether the file p belongs to resPath.
func (pl Placement) Owns(resPath, p string) bool {
	if pl.p == nil {
		return path.Dir(p) == SidecarDir(resPath)
	}
	r, ok := pl.p.ResourceOf(p)
	return ok && r == resPath
}

// List is ListSidecar of resPath's attachment folder, keeping only resPath's
// files; a placed folder is shared, so it reports no subdirectories.
func (pl Placement) List(t *workdir.Tree, resPath string) (files, dirs []string, err error) {
	files, dirs, err = t.ListSidecar(pl.Dir(resPath))
	if pl.p == nil || err != nil {
		return files, dirs, err
	}
	return pl.own(resPath, files), nil, nil
}

func (pl Placement) own(resPath string, files []string) []string {
	var out []string
	for _, f := range files {
		if pl.Owns(resPath, f) {
			out = append(out, f)
		}
	}
	return out
}
