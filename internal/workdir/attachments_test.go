package workdir

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestAttachmentsRoundTrip(t *testing.T) {
	tr := newTree(t)
	a, err := tr.LoadAttachments()
	if err != nil || len(a.All()) != 0 {
		t.Fatalf("missing file must load empty: %v %v", a, err)
	}
	a.Put(AttEntry{ResID: "98130", AttID: "att2", Version: "3", SHA: "bb", Size: 7, MTime: 11, Path: "eng/R.files/b b.png"})
	a.Put(AttEntry{ResID: "98130", AttID: "att1", Version: "-", SHA: "aa", Size: 5, MTime: 0, Path: "eng/R.files/a.pdf"})
	a.Put(AttEntry{ResID: "7", AttID: "x", Version: "1", SHA: "cc", Size: 1, MTime: 1, Path: "z.files/x"})
	if err := tr.SaveAttachments(a); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(tr.Root, ".gfs", "attachments"))
	want := "98130\tatt1\t-\taa\t5\t0\teng/R.files/a.pdf\n98130\tatt2\t3\tbb\t7\t11\teng/R.files/b b.png\n7\tx\t1\tcc\t1\t1\tz.files/x\n"
	if string(raw) != want {
		t.Fatalf("got\n%q\nwant\n%q", raw, want)
	}
	b, err := tr.LoadAttachments()
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := b.Get("98130", "att2"); !ok || e.Path != "eng/R.files/b b.png" || e.Size != 7 || e.MTime != 11 {
		t.Fatalf("%+v", e)
	}
	if e, ok := b.ByPath("eng/R.files/a.pdf"); !ok || e.AttID != "att1" {
		t.Fatalf("%+v", e)
	}
	if got := b.ForResource("98130"); len(got) != 2 || got[0].AttID != "att1" {
		t.Fatalf("%+v", got)
	}
	b.Delete("98130", "att1")
	if _, ok := b.Get("98130", "att1"); ok {
		t.Fatal("Delete")
	}
	os.WriteFile(filepath.Join(tr.Root, ".gfs", "attachments"), []byte("bad line\n"), 0o644)
	if _, err := tr.LoadAttachments(); err == nil {
		t.Fatal("malformed line must fail")
	}
}

func TestSidecarsAndScan(t *testing.T) {
	tr := newTree(t)
	must(t, tr.WriteFile("eng/R.xml", []byte("x")))
	must(t, tr.WriteFile("eng/R.files/a.png", []byte("a")))
	must(t, tr.WriteFile("eng/R.files/sub/b.png", []byte("b")))
	must(t, tr.WriteFile("eng/R.files/.gfs-tmp-123", []byte("t")))
	must(t, tr.WriteFile("eng/R/child.xml", []byte("c")))
	files, err := tr.Scan()
	if err != nil || !slices.Equal(files, []string{"eng/R.xml", "eng/R/child.xml"}) {
		t.Fatalf("Scan must skip sidecars: %v %v", files, err)
	}
	fs, ds, err := tr.ListSidecar("eng/R.files")
	if err != nil || !slices.Equal(fs, []string{"eng/R.files/a.png"}) || !slices.Equal(ds, []string{"eng/R.files/sub"}) {
		t.Fatalf("%v %v %v", fs, ds, err)
	}
	if fs, ds, err := tr.ListSidecar("nope.files"); err != nil || fs != nil || ds != nil {
		t.Fatalf("missing dir must be empty: %v %v %v", fs, ds, err)
	}
	if sc, err := tr.Sidecars(); err != nil || !slices.Equal(sc, []string{"eng/R.files"}) {
		t.Fatalf("%v %v", sc, err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestWriteStreamDoesNotBuffer(t *testing.T) {
	tr := newTree(t)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := tr.WriteStream("big.files/blob.bin", func(w io.Writer) error {
		_, err := io.CopyN(w, zeros{}, 64<<20)
		return err
	})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 8<<20 {
		t.Fatalf("WriteStream allocated %d MB for a 64 MB file", d>>20)
	}
	st, err := tr.Stat("big.files/blob.bin")
	if err != nil || st.Size() != 64<<20 {
		t.Fatalf("%v %v", st, err)
	}
	f, err := tr.Open("big.files/blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
