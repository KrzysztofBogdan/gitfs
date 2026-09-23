package changes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/attach"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const noteA = `<note id="1" version="2"><title>T</title><attachment id="a1" name="x.png" size="3" version="1" created="2026-01-01"/></note>`
const noteNoAtt = `<note id="1" version="2"><title>T</title></note>`
const xPath = "a/n.files/x.png"

var noteV2 = strings.Replace(noteA, `size="3" version="1"`, `size="3" version="2"`, 1)

// attTree is setup with chosen working and base contents for a/n.xml.
func attTree(t *testing.T, working, base string) (*workdir.Tree, *workdir.Index) {
	t.Helper()
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("a/n.xml", file(t, working))
	tr.WriteBase("a/n.xml", file(t, base))
	ix, _ := tr.LoadIndex()
	ix.Put(workdir.Entry{ID: "1", Version: "2", Path: "a/n.xml"})
	return tr, ix
}

// fetch writes data as the bytes of attachment a1 and tracks them at version.
func fetch(t *testing.T, tr *workdir.Tree, rel, data, version string) {
	t.Helper()
	if err := tr.WriteFile(rel, []byte(data)); err != nil {
		t.Fatal(err)
	}
	line, err := attach.Entry(tr, "1", "a1", version, rel)
	if err != nil {
		t.Fatal(err)
	}
	atts, _ := tr.LoadAttachments()
	atts.Put(line)
	if err := tr.SaveAttachments(atts); err != nil {
		t.Fatal(err)
	}
}

func attSummary(cs []FileChange) []string {
	var out []string
	for _, c := range cs {
		if !c.Quiet {
			out = append(out, fmt.Sprintf("%c %s", c.Status, c.Path))
		}
		for _, a := range c.Attachments {
			s := fmt.Sprintf("%c %s", a.Status, a.Path)
			if a.Action != nil {
				s += " " + a.Action.Verb + ":" + a.Action.Target
			}
			out = append(out, s)
		}
	}
	return out
}

func TestAttachmentStates(t *testing.T) {
	put := func(p, data string) func(*testing.T, *workdir.Tree) {
		return func(t *testing.T, tr *workdir.Tree) { tr.WriteFile(p, []byte(data)) }
	}
	fetched := func(data string) func(*testing.T, *workdir.Tree) {
		return func(t *testing.T, tr *workdir.Tree) {
			fetch(t, tr, xPath, "abc", "1")
			tr.WriteFile(xPath, []byte(data))
		}
	}
	cases := []struct {
		name          string
		working, base string
		setup         []func(*testing.T, *workdir.Tree)
		want          []string
	}{
		{"not fetched", noteA, noteA, nil, nil},
		{"fetched clean", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"changed locally", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abd")},
			[]string{"M a/n.files/x.png update:attachment[id=a1]"}},
		{"changed on remote only", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"both changed", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abd")}, []string{"C a/n.files/x.png"}},
		{"conflict copy ignored", noteV2, noteV2, []func(*testing.T, *workdir.Tree){fetched("abd"), put("a/n.files/x.remote-v2.png", "rem")},
			[]string{"C a/n.files/x.png"}},
		{"evicted", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc"),
			func(t *testing.T, tr *workdir.Tree) { os.Remove(tr.Abs(xPath)) }}, nil},
		{"element removed, fetched", noteNoAtt, noteA, []func(*testing.T, *workdir.Tree){fetched("abc")},
			[]string{"M a/n.xml", "D a/n.files/x.png delete:attachment[id=a1]"}},
		{"element removed, not fetched", noteNoAtt, noteA, nil,
			[]string{"M a/n.xml", "D a/n.files/x.png delete:attachment[id=a1]"}},
		{"new file", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/n.files/new.csv", "a,b")},
			[]string{"A a/n.files/new.csv create:attachment[file=new.csv]"}},
		{"untracked collision", noteA, noteA, []func(*testing.T, *workdir.Tree){put(xPath, "abc")}, []string{"! a/n.files/x.png"}},
		{"subdirectory", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/n.files/sub/y", "y")}, []string{"! a/n.files/sub"}},
		{"deleted on remote, changed locally", noteNoAtt, noteNoAtt, []func(*testing.T, *workdir.Tree){fetched("abd")},
			[]string{"C a/n.files/x.png"}},
		{"deleted on remote, unchanged", noteNoAtt, noteNoAtt, []func(*testing.T, *workdir.Tree){fetched("abc")}, nil},
		{"rename", noteA, noteA, []func(*testing.T, *workdir.Tree){fetched("abc"), func(t *testing.T, tr *workdir.Tree) {
			os.Rename(tr.Abs(xPath), tr.Abs("a/n.files/y.png"))
		}}, []string{"! a/n.files/y.png"}},
		{"orphan folder", noteA, noteA, []func(*testing.T, *workdir.Tree){put("a/gone.files/z.bin", "z")}, []string{"! a/gone.files"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, ix := attTree(t, c.working, c.base)
			for _, f := range c.setup {
				f(t, tr)
			}
			got := attSummary(compute(t, tr, ix))
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestAttachmentQuietAndNotes(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	fetch(t, tr, xPath, "abc", "1")
	tr.WriteFile(xPath, []byte("abd"))
	c := one(t, compute(t, tr, ix))
	if c.Status != 'M' || !c.Quiet || len(c.Actions) != 1 || c.Actions[0].File != xPath || !c.Actions[0].IsAttachment() {
		t.Fatalf("%+v", c)
	}
	tr2, ix2 := attTree(t, noteNoAtt, noteA)
	c = one(t, compute(t, tr2, ix2))
	if len(c.Attachments) != 1 || !strings.HasSuffix(c.Attachments[0].Action.Detail, "(not fetched)") {
		t.Fatalf("%+v", c.Attachments)
	}
}

func TestAttachmentReadOnlyKind(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	tr.WriteFile("a/n.files/new.csv", []byte("a,b"))
	ad := fake.New()
	ad.ReadOnlyAttachments()
	cs, err := Compute(tr, ix, ad, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := one(t, cs)
	if len(c.Actions) != 0 || c.Attachments[0].Status != '!' || c.Attachments[0].Note != "attachments of this resource are read-only" {
		t.Fatalf("%+v", c)
	}
}

func TestAttachmentFilterSelectsOneFile(t *testing.T) {
	working := strings.Replace(noteA, "<title>T</title>", "<title>Changed</title>", 1)
	tr, ix := attTree(t, working, noteA)
	fetch(t, tr, xPath, "abc", "1")
	tr.WriteFile(xPath, []byte("abd"))
	filter, _ := PathFilter(tr, []string{tr.Abs(xPath)})
	cs, err := Compute(tr, ix, fake.New(), filter)
	if err != nil {
		t.Fatal(err)
	}
	c := one(t, cs)
	if !c.Quiet || len(c.Actions) != 1 || c.Actions[0].Target != "attachment[id=a1]" {
		t.Fatalf("only the attachment must be selected: %+v", c)
	}
}

func TestAttachmentsOfNewResource(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	tr.WriteFile("a/new.xml", []byte("<note><title>N</title></note>"))
	tr.WriteFile("a/new.files/f.txt", []byte("f"))
	var c FileChange
	for _, x := range compute(t, tr, ix) {
		if x.Path == "a/new.xml" {
			c = x
		}
	}
	if !eq(verbs(c), []string{"create", "create:attachment[file=f.txt]"}) {
		t.Fatalf("%v", verbs(c))
	}
}

func TestSidecarMovedWithResource(t *testing.T) {
	tr, ix := attTree(t, noteA, noteA)
	fetch(t, tr, xPath, "abc", "1")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(tr.Abs("b"), 0o755))
	must(os.Rename(tr.Abs("a/n.xml"), tr.Abs("b/n.xml")))
	must(os.Rename(tr.Abs("a/n.files"), tr.Abs("b/n.files")))
	c := one(t, compute(t, tr, ix))
	if c.Status != 'R' || len(c.Attachments) != 0 || !eq(verbs(c), []string{"move"}) {
		t.Fatalf("%c %v %+v", c.Status, verbs(c), c.Attachments)
	}
}

func TestExplicitVerbIncludesFiles(t *testing.T) {
	env := `<gfs action="publish" channel="news"><content>` + noteA + `</content></gfs>`
	tr, ix := attTree(t, env, noteA)
	tr.WriteFile("a/n.files/new.csv", []byte("a,b"))
	c := one(t, compute(t, tr, ix))
	if !eq(verbs(c), []string{"publish"}) || len(c.Attachments) != 1 || c.Attachments[0].Action != nil || c.Attachments[0].Note != "included in publish" {
		t.Fatalf("%v %+v", verbs(c), c.Attachments)
	}
}
