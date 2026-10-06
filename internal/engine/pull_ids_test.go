package engine

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestPullIDs(t *testing.T) {
	env, ad, _ := cloned(t)
	cursor := env.Index.Cursor
	retitle := func(title string) func(*xmltree.Node) {
		return func(root *xmltree.Node) { root.Child("title").Children[0].Text = title }
	}
	ad.Remote.Edit("1", retitle("One v2"))
	ad.Remote.Edit("2", retitle("Two v2"))
	ad.Remote.Put("3", "a/three.xml", `<note><title>Three</title></note>`)
	if _, err := Pull(ctx, env, PullOpts{IDs: []string{"1", "3", "missing"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), "One v2") || !strings.Contains(read(t, env, "a/three.xml"), "Three") {
		t.Fatal("named resources are pulled")
	}
	if strings.Contains(read(t, env, "a/b/two.xml"), "Two v2") {
		t.Fatal("other resources are left alone")
	}
	if env.Index.Cursor != cursor {
		t.Fatal("the cursor is kept")
	}
}
