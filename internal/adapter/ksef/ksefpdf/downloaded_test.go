package ksefpdf

// The spike's check (ksef pdf spec §8.4), run by hand:
//
//	GFS_KSEF_PDF_DIR=~/Documents/invoices GFS_KSEF_MIRROR=~/gfs/ksef \
//	  go test ./internal/adapter/ksef/ksefpdf -run TestDownloadedPDFs -v

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	creationRe = regexp.MustCompile(`\(D:(\d{14})Z\)`)
	idRe       = regexp.MustCompile(`/ID \[<([0-9a-f]{32})>`)
	qrRe       = regexp.MustCompile(`/URI \((https://qr[^)/]*)/invoice/`)
)

// downloadTime recovers the ms a web-app PDF was made at from its /ID.
func downloadTime(pdf []byte) (int64, bool) {
	c, id := creationRe.FindSubmatch(pdf), idRe.FindSubmatch(pdf)
	if c == nil || id == nil {
		return 0, false
	}
	t, err := time.Parse("20060102150405", string(c[1]))
	if err != nil {
		return 0, false
	}
	for ms := t.UnixMilli(); ms < t.UnixMilli()+1000; ms++ {
		h := md5.Sum([]byte(fmt.Sprintf("%d\nProducer: pdfmake\nCreator: pdfmake\nCreationDate: %d\n", ms, ms)))
		if hex.EncodeToString(h[:]) == string(id[1]) {
			return ms, true
		}
	}
	return 0, false
}

func TestDownloadedPDFs(t *testing.T) {
	dir, mirror := os.Getenv("GFS_KSEF_PDF_DIR"), os.Getenv("GFS_KSEF_MIRROR")
	if dir == "" || mirror == "" {
		t.Skip("GFS_KSEF_PDF_DIR and GFS_KSEF_MIRROR not set")
	}
	xmls := map[string]string{}
	filepath.WalkDir(mirror, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".xml") && !strings.Contains(p, "/_gfs/") && !strings.Contains(p, "/.gfs/") {
			xmls[strings.TrimSuffix(filepath.Base(p), ".xml")] = p
		}
		return nil
	})
	r := testRenderer(t)
	tried, same := 0, 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		k := strings.TrimSuffix(filepath.Base(p), ".pdf")
		if err != nil || d.IsDir() || xmls[k] == "" {
			return nil
		}
		want, _ := os.ReadFile(p)
		ms, ok := downloadTime(want)
		if !ok {
			return nil
		}
		x, _ := os.ReadFile(xmls[k])
		base := ""
		if m := qrRe.FindSubmatch(want); m != nil {
			base = string(m[1])
		}
		got, err := r.render(t.Context(), Input{XML: x, KsefNumber: k, QRBase: base}, ms)
		tried++
		if err == nil && bytes.Equal(got, want) {
			same++
		} else {
			t.Logf("differs: %s (%v)", k, err)
		}
		return nil
	})
	t.Logf("%d of %d byte-identical with %s", same, tried, Version())
	if tried > 0 && same == 0 {
		t.Fatal("no downloaded PDF matches; were they all made by another release?")
	}
}
