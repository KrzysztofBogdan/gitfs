package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func siteServer(t *testing.T) *cftest.Server {
	t.Helper()
	srv := cftest.New()
	t.Cleanup(srv.Close)
	srv.AddSpace("ENG", "100")
	srv.AddSpace("OPS", "200")
	srv.PutSpace(cftest.Space{Key: "~jan", ID: "300", Type: "personal"})
	srv.PutSpace(cftest.Space{Key: "OLD", ID: "400", Status: "archived"})
	srv.AddPage(cftest.Page{ID: "81001", Title: "Home", SpaceID: "100", Storage: "<p>eng</p>"})
	srv.AddPage(cftest.Page{ID: "81002", Title: "Architecture", ParentID: "81001", SpaceID: "100", Storage: "<p>a</p>"})
	srv.AddPage(cftest.Page{ID: "82001", Title: "Home", SpaceID: "200", Storage: "<p>ops</p>"})
	srv.AddPage(cftest.Page{ID: "83001", Title: "Jan", SpaceID: "300", Storage: "<p>jan</p>"})
	srv.AddPage(cftest.Page{ID: "84001", Title: "Old", SpaceID: "400", Storage: "<p>old</p>"})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	return srv
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

var urlLine = regexp.MustCompile(`(?m)^\s*url = (.*)$`)

func remoteURL(t *testing.T, tree string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tree, ".gfs", "config"))
	if err != nil {
		t.Fatal(err)
	}
	m := urlLine.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("no url in config:\n%s", b)
	}
	return m[1]
}

var pageBodyGet = regexp.MustCompile(`^GET /wiki/api/v2/pages/[^/]+$`)

func TestConfluenceSiteEndToEnd(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)

	// whole site: global, current spaces only
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?base="+srv.URL)
	t.Chdir(filepath.Join(dir, "acme"))
	for _, p := range []string{"eng/Home.xml", "eng/Home/Architecture.xml", "ops/Home.xml"} {
		if !exists(p) {
			t.Fatalf("missing %s", p)
		}
	}
	if exists("~jan") || exists("old") {
		t.Fatal("personal and archived spaces must be skipped")
	}

	// a quiet pull fetches no page bodies
	srv.TakeRequests()
	mustContain(t, mustRun(t, 0, "pull"), "Already up to date.")
	for _, r := range srv.TakeRequests() {
		if pageBodyGet.MatchString(r) {
			t.Fatalf("quiet pull fetched a page: %s", r)
		}
	}

	// comment-only change is picked up; label-only change waits for --full
	srv.AddComment(cftest.Comment{PageID: "82001", Storage: "<p>on call</p>", CreatedAt: srv.Stamp()})
	srv.SetLabels("81002", "design")
	out := mustRun(t, 0, "pull")
	mustContain(t, out, "~  ops/Home.xml")
	if strings.Contains(out, "Architecture") {
		t.Fatalf("label-only change must wait for --full:\n%s", out)
	}
	mustContain(t, mustRun(t, 0, "pull", "--full"), "~  eng/Home/Architecture.xml")

	// one commit, two spaces
	replaceIn(t, "eng/Home.xml", "<p>eng</p>", "<p>eng v2</p>")
	os.MkdirAll("ops/Home", 0o755)
	os.WriteFile("ops/Home/Deploy.xml", []byte(`<page><title>Deploy</title><body type="application/xhtml+xml"><p>steps</p></body></page>`), 0o644)
	mustRun(t, 0, "commit")
	b, _ := os.ReadFile("ops/Home/Deploy.xml")
	m := regexp.MustCompile(`<page id="(\d+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("write-back:\n%s", b)
	}
	if p, _ := srv.Page(string(m[1])); p.SpaceID != "200" || p.ParentID != "82001" {
		t.Fatalf("%+v", p)
	}
	if p, _ := srv.Page("81001"); !strings.Contains(p.Storage, "eng v2") {
		t.Fatalf("%+v", p)
	}

	// a new top-level folder is refused
	os.MkdirAll("new", 0o755)
	os.WriteFile("new/Page.xml", []byte(`<page><title>Page</title><body type="application/xhtml+xml"/></page>`), 0o644)
	out, code := gfs(t, "commit", "--dry-run")
	if code == 0 || !strings.Contains(out, `no space "new" in this tree; gfs does not create spaces`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	os.RemoveAll("new")

	// a move between spaces is refused
	os.Rename("eng/Home/Architecture.xml", "ops/Home/Architecture.xml")
	out, code = gfs(t, "commit", "--dry-run")
	if code == 0 || !strings.Contains(out, "moving pages between spaces is not supported") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	os.Rename("ops/Home/Architecture.xml", "eng/Home/Architecture.xml")

	// a space created on Confluence arrives with the next pull
	srv.AddSpace("DOC", "500")
	srv.AddPage(cftest.Page{ID: "85001", Title: "Docs", SpaceID: "500", Storage: "<p>d</p>"})
	mustContain(t, mustRun(t, 0, "pull"), "+  doc/Docs.xml")

	// excluding a space in the config removes its files
	replaceIn(t, ".gfs/config", "acme.atlassian.net?", "acme.atlassian.net?exclude=OPS&")
	out = mustRun(t, 0, "pull")
	mustContain(t, out, "-  ops/Home.xml", "-  ops/Home/Deploy.xml")
	if exists("ops/Home.xml") {
		t.Fatal("excluded space still on disk")
	}
}

func TestConfluenceCloneForms(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	base := "base=" + srv.URL
	same := map[string]string{
		"path":    "confluence://acme.atlassian.net/ENG?" + base,
		"browser": "confluence:https://acme.atlassian.net/wiki/spaces/ENG/pages/81002/Architecture?" + base,
		"filter":  "confluence://acme.atlassian.net?filter=ENG&" + base,
	}
	want := ""
	for name, raw := range same {
		mustRun(t, 0, "clone", raw, name)
		if !exists(filepath.Join(name, "eng/Home.xml")) || exists(filepath.Join(name, "ops")) {
			t.Fatalf("%s: wrong spaces", name)
		}
		got := remoteURL(t, name)
		if want == "" {
			want = got
		}
		if got != want || !strings.Contains(got, "filter=ENG") {
			t.Fatalf("%s: url %q, want %q", name, got, want)
		}
	}

	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?type=personal&"+base, "personal")
	if !exists("personal/~jan/Jan.xml") || exists("personal/eng") {
		t.Fatal("type=personal")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?exclude=OPS&"+base, "noops")
	if !exists("noops/eng/Home.xml") || exists("noops/ops") {
		t.Fatal("exclude=OPS")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?filter=OLD,~jan&"+base, "named")
	if !exists("named/old/Old.xml") || !exists("named/~jan/Jan.xml") {
		t.Fatal("filter=OLD,~jan")
	}
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net/ENG?"+base) // default dir from the key
	if !exists("eng/eng/Home.xml") {
		t.Fatal("default dir")
	}

	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net?filter=NOPE&"+base, "x"); code == 0 || !strings.Contains(out, "space NOPE not found") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net?filter=ENG&type=all&"+base, "y"); code == 0 || !strings.Contains(out, "cannot be combined") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out, code := gfs(t, "clone", "confluence://acme.atlassian.net/wiki/ENG?"+base, "z"); code == 0 || !strings.Contains(out, "want confluence://<site>") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// backdate pretends the last pull ran 20 minutes ago, so the search window no
// longer reaches content from before the next pull's own cursor.
func backdate(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(".gfs/index")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339)
	b = regexp.MustCompile(`(?m)^# cursor .*$`).ReplaceAll(b, []byte("# cursor "+old))
	os.WriteFile(".gfs/index", b, 0o644)
}

// quarterHourAgo stamps content as made 15 minutes ago: after a backdated
// cursor, outside a fresh cursor's window.
func quarterHourAgo() string {
	return time.Now().UTC().Add(-15 * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

// A pull limited to some paths must not move the cursor past changes it skipped.
func TestConfluencePathPullKeepsCursor(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?base="+srv.URL, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	backdate(t)
	srv.AddComment(cftest.Comment{PageID: "81002", Storage: "<p>late</p>", CreatedAt: quarterHourAgo()})
	mustRun(t, 0, "pull", "ops")
	mustContain(t, mustRun(t, 0, "pull"), "~  eng/Home/Architecture.xml")
}

// A page skipped as conflicted must get its comment once the conflict is resolved.
func TestConfluenceConflictedPullKeepsCursor(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", "confluence://acme.atlassian.net?base="+srv.URL, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	page := "eng/Home/Architecture.xml"
	replaceIn(t, page, "<p>a</p>", "<p>local</p>")
	srv.EditPage("81002", func(p *cftest.Page) { p.Storage = "<p>remote</p>" })
	mustRun(t, 1, "pull")
	backdate(t)
	srv.AddComment(cftest.Comment{PageID: "81002", Storage: "<p>late</p>", CreatedAt: quarterHourAgo()})
	mustRun(t, 1, "pull") // still conflicted: the commented page is skipped
	mustRun(t, 0, "resolve", "--theirs", page)
	mustRun(t, 0, "pull")
	b, _ := os.ReadFile(page)
	if !strings.Contains(string(b), "<p>late</p>") {
		t.Fatalf("comment lost after resolve:\n%s", b)
	}
}

// Not a terminal: no bar, and --quiet is accepted by clone and pull.
func TestProgressOffWithoutTerminal(t *testing.T) {
	srv := siteServer(t)
	dir := t.TempDir()
	t.Chdir(dir)
	out := mustRun(t, 0, "clone", "--quiet", "confluence://acme.atlassian.net?base="+srv.URL, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	out += mustRun(t, 0, "pull", "-q")
	out += mustRun(t, 0, "pull", "--full")
	if strings.ContainsAny(out, "\r\x1b") {
		t.Fatalf("progress drawn without a terminal: %q", out)
	}
}
