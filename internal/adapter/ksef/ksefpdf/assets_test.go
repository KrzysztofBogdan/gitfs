package ksefpdf

import (
	"bytes"
	"regexp"
	"testing"
)

func TestAssets(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+ [0-9a-f]{7}$`).MatchString(Version()) || Release() != "1.1.40" {
		t.Fatalf("VERSION is release and commit: %q %q", Version(), Release())
	}
	for _, f := range fontNames {
		b, err := assets.ReadFile("assets/" + f)
		if err != nil || !bytes.HasPrefix(b, []byte{0, 1, 0, 0}) {
			t.Fatalf("%s is a TrueType font: %v", f, err)
		}
	}
	js, _ := assets.ReadFile("assets/generator.js")
	if !bytes.Contains(js, []byte("gfsAddFont")) || bytes.Contains(js, []byte(`"Roboto-Italic.ttf":"AAEAAA`)) {
		t.Fatal("the gfs build: gfsAddFont exported, no base64 fonts inside")
	}
}
