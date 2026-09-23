package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q:\n%s", path, old, b)
	}
	os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644)
}

func mustRun(t *testing.T, want int, args ...string) string {
	t.Helper()
	out, code := gfs(t, args...)
	if code != want {
		t.Fatalf("gfs %v: exit %d, want %d\n%s", args, code, want, out)
	}
	return out
}

func TestConfluenceEndToEnd(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Engineering space.</p>"})
	srv.AddPage(cftest.Page{ID: "98120", Title: "Architecture", ParentID: "98001", SpaceID: "100", Storage: "<p>a</p><p>b</p><p>c</p>"})
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	remote := "confluence://acme.atlassian.net/ENG?base=" + srv.URL

	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", remote, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	arch := "eng/Home/Architecture.xml"
	if _, err := os.Stat(arch); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	// 1. update a page body
	replaceIn(t, arch, "<p>a</p>", "<p>alpha</p>")
	if out := mustRun(t, 0, "commit"); !strings.Contains(out, "v1 -> v2") {
		t.Fatal(out)
	}
	if p, _ := srv.Page("98120"); !strings.Contains(p.Storage, "<p>alpha</p>") || p.Version != 2 {
		t.Fatalf("%+v", p)
	}

	// 2. create a child page from a bare root
	os.MkdirAll("eng/Home/Runbooks", 0o755)
	os.WriteFile("eng/Home/Runbooks/Rollback.xml", []byte(`<page><title>Rollback</title><labels><label>runbook</label></labels>
<body type="application/xhtml+xml"><ol><li>Redeploy previous tag.</li></ol></body></page>`), 0o644)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, `create page under "Runbooks"`) {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit")
	b, _ := os.ReadFile("eng/Home/Runbooks/Rollback.xml")
	if !strings.Contains(string(b), `<page id="`) || !strings.Contains(string(b), `parent="98130"`) {
		t.Fatalf("write-back:\n%s", b)
	}

	// 3. conflict, resolve --theirs, commit is a no-op
	srv.EditPage("98120", func(p *cftest.Page) { p.Storage = strings.Replace(p.Storage, "<p>b</p>", "<p>B remote</p>", 1) })
	replaceIn(t, arch, "<p>b</p>", "<p>B local</p>")
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "conflict with remote v3") {
		t.Fatal(out)
	}
	b, _ = os.ReadFile(arch)
	if !strings.Contains(string(b), "<<<<<<< local") || !strings.Contains(string(b), `<conflict remote-version="3" by="bob"`) {
		t.Fatalf("%s", b)
	}
	mustRun(t, 0, "resolve", "--theirs", arch)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	// 4. pull a remote comment
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>Nice.</p>"})
	if out := mustRun(t, 0, "pull"); !strings.Contains(out, "~  "+arch) {
		t.Fatal(out)
	}
	b, _ = os.ReadFile(arch)
	if !strings.Contains(string(b), "<p>Nice.</p>") {
		t.Fatalf("%s", b)
	}

	// 5. delete needs --allow on a non-TTY
	os.Remove("eng/Home/Runbooks/Rollback.xml")
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "--allow delete") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit", "--allow", "delete")

	// 6. a fresh clone is byte-identical to the working tree (canonical form is stable)
	t.Chdir(dir)
	mustRun(t, 0, "clone", remote, "wt2")
	for _, p := range []string{"eng/Home.xml", arch, "eng/Home/Runbooks.xml"} {
		a, _ := os.ReadFile(filepath.Join(dir, "wt", p))
		c, _ := os.ReadFile(filepath.Join(dir, "wt2", p))
		if string(a) != string(c) {
			t.Fatalf("%s differs between working tree and fresh clone:\n%s\n---\n%s", p, a, c)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wt2", "eng/Home/Runbooks/Rollback.xml")); err == nil {
		t.Fatal("deleted page came back")
	}
}

// No env vars: the host default picks the email, the keyring gives the token,
// clone pins the email, and pull keeps using it after the host default changes.
func TestConfluenceStoredToken(t *testing.T) {
	home := authEnv(t)
	srv := cftest.New()
	defer srv.Close()
	srv.Accounts = map[string]string{"me@x.com": "good"}
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>x</p>"})

	if out, code := gfsIn(t, "good\n", "auth", "set", "me@x.com", "--host", "acme.atlassian.net", "--base", srv.URL); code != 0 {
		t.Fatal(out)
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?base="+srv.URL, "wt")
	cfg, _ := os.ReadFile(filepath.Join(home, "wt", ".gfs", "config"))
	mustContain(t, string(cfg), "email = me@x.com")

	g, _ := creds.LoadGlobal(creds.DefaultDirs())
	g.SetHostEmail("acme.atlassian.net", "other@x.com") // has no token: would fail if used
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(home, "wt"))
	mustRun(t, 0, "pull")
}
