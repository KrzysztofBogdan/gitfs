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
		{"98001", "Home", ""},
		{"98120", "Architecture", "98001"},
		{"98130", "Runbooks", "98001"},
		{"98871", "Rollback", "98130"},
		{"99", "Orphan", "55555"},
		{"100", "Dup", "98001"},
		{"1000", "dup", "98001"},
		{"7", "a", "8"},
		{"8", "b", "7"},
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
