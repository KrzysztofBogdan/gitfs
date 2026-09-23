package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func TestConfluenceAttachmentsEndToEnd(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Home</p>"})
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	att := srv.AddAttachment(cftest.Attachment{PageID: "98130", Title: "rollback-flow.png", MediaType: "image/png", Data: []byte("v1")})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?base="+srv.URL, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	img := "eng/Home/Runbooks.files/rollback-flow.png"
	readFile := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
	if _, err := os.Stat(img); !os.IsNotExist(err) {
		t.Fatal("clone must not download attachments")
	}
	mustRun(t, 0, "get", "eng/Home/Runbooks.xml")
	if readFile(img) != "v1" {
		t.Fatal("get")
	}

	os.WriteFile(img, []byte("v2-local"), 0o644)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "  M  "+img) || !strings.Contains(out, "upload new version of attachment") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit")
	if a, _ := srv.Attachment(att.ID); string(a.Data) != "v2-local" || a.Version != 2 {
		t.Fatalf("%+v", a)
	}

	srv.EditAttachment(att.ID, []byte("v3-remote"))
	os.WriteFile(img, []byte("mine"), 0o644)
	if out := mustRun(t, 1, "pull"); !strings.Contains(out, "  C  "+img) {
		t.Fatal(out)
	}
	if readFile("eng/Home/Runbooks.files/rollback-flow.remote-v3.png") != "v3-remote" || readFile(img) != "mine" {
		t.Fatal("pull must keep both copies")
	}
	mustRun(t, 0, "resolve", "--theirs", img)
	if readFile(img) != "v3-remote" {
		t.Fatal("resolve --theirs")
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	os.WriteFile("eng/Home/Runbooks.files/oncall.csv", []byte("week,primary\n"), 0o644)
	if out := mustRun(t, 0, "commit"); !strings.Contains(out, "create  eng/Home/Runbooks.files/oncall.csv   ok  id=att") {
		t.Fatal(out)
	}
	if as := srv.Attachments("98130"); len(as) != 2 {
		t.Fatalf("%+v", as)
	}

	page := "eng/Home/Runbooks.xml"
	re := regexp.MustCompile(`(?m)^\s*<attachment id="` + att.ID + `"[^\n]*\n`)
	os.WriteFile(page, re.ReplaceAll([]byte(readFile(page)), nil), 0o644)
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "--allow delete") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit", "--allow", "delete")
	if _, ok := srv.Attachment(att.ID); ok {
		t.Fatal("not deleted on the server")
	}
	if _, err := os.Stat(img); !os.IsNotExist(err) {
		t.Fatal("the local file must be gone")
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
}
