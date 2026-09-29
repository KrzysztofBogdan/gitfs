package confluence

import "testing"

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Architecture":    "Architecture",
		"CI/CD \\ notes":  "CI-CD - notes",
		".hidden":         "_hidden",
		"  ":              "untitled",
		"tab\there\x7f":   "tab here",
		"Login page: 500": "Login page: 500",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPagePaths(t *testing.T) {
	got := pagePaths("eng", []pageRef{
		{ID: "98001", Title: "Home", Parent: ""},
		{ID: "98120", Title: "Architecture", Parent: "98001"},
		{ID: "98130", Title: "Runbooks", Parent: "98001"},
		{ID: "98871", Title: "Rollback", Parent: "98130"},
		{ID: "99", Title: "Orphan", Parent: "55555"},
		{ID: "100", Title: "Dup", Parent: "98001"},
		{ID: "1000", Title: "dup", Parent: "98001"},
		{ID: "7", Title: "a", Parent: "8"},
		{ID: "8", Title: "b", Parent: "7"},
	})
	want := map[string]string{
		"98001": "eng/Home.xml",
		"98120": "eng/Home/Architecture.xml",
		"98130": "eng/Home/Runbooks.xml",
		"98871": "eng/Home/Runbooks/Rollback.xml",
		"99":    "eng/Orphan.xml",
		"100":   "eng/Home/Dup.xml",
		"1000":  "eng/Home/dup (2).xml",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got %q, want %q", id, got[id], w)
		}
	}
	if got["7"] == "" || got["8"] == "" {
		t.Error("cycle members must still get a path")
	}
}

func TestParentPath(t *testing.T) {
	if parentPath("eng/Home/Runbooks/Rollback.xml") != "eng/Home/Runbooks.xml" || parentPath("eng/Home.xml") != "" {
		t.Fatal("parentPath")
	}
}
