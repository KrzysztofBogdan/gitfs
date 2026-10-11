package ksefpdf

import (
	"bytes"
	"regexp"
	"strings"
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

func TestThirdPartyNotices(t *testing.T) {
	b, err := assets.ReadFile("assets/THIRD-PARTY.txt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"\n== core-js", "\n== pako@", "\n== i18next@", "\n== pdfkit@", "TERMS AND CONDITIONS FOR USE, REPRODUCTION"} {
		if !strings.Contains(s, want) {
			t.Errorf("THIRD-PARTY.txt lacks %q: notices for what the bundle holds, Roboto's Apache text", want)
		}
	}
	for _, unwanted := range []string{"\n== typescript@", "\n== @rollup/"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("THIRD-PARTY.txt lists %q, which the bundle does not hold", unwanted)
		}
	}
}
