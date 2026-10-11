// Package ksefpdf renders KSeF invoice PDFs with the Ministry of Finance's
// ksef-pdf-generator, embedded and run in goja (ksef pdf spec).
package ksefpdf

import (
	"embed"
	"strings"
)

// assets is the gfs build of the generator (ksef pdf spec §3), made by build.sh.
//
//go:embed assets
var assets embed.FS

// fontNames are the TTFs the generator's pdfmake knows, added to every runtime.
var fontNames = []string{"Roboto-Regular.ttf", "Roboto-Medium.ttf", "Roboto-Italic.ttf", "Roboto-MediumItalic.ttf"}

// Version is the embedded generator's release and commit, "1.1.40 f59fc4e".
func Version() string {
	b, err := assets.ReadFile("assets/VERSION")
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(b))
}

// Release is the embedded generator's release, "1.1.40".
func Release() string {
	r, _, _ := strings.Cut(Version(), " ")
	return r
}
