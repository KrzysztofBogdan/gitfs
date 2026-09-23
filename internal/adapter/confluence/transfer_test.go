package confluence

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func TestAttachmentEndpoints(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", SpaceID: "100", Storage: "<p/>"})
	c := newClient(target{base: srv.URL, space: "ENG", email: "me@x.com", token: "t"})

	var created struct {
		Results []struct {
			ID      string               `json:"id"`
			Version struct{ Number int } `json:"version"`
		} `json:"results"`
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment", "rollback flow.png", strings.NewReader("png1"), &created); err != nil || len(created.Results) != 1 {
		t.Fatalf("%+v %v", created, err)
	}
	id := created.Results[0].ID
	a, ok := srv.Attachment(id)
	if !ok || a.Title != "rollback flow.png" || string(a.Data) != "png1" || a.MediaType != "image/png" || a.Version != 1 {
		t.Fatalf("%+v", a)
	}
	var meta struct {
		FileSize     int64  `json:"fileSize"`
		DownloadLink string `json:"downloadLink"`
	}
	if err := c.do(bg, http.MethodGet, "/wiki/api/v2/attachments/"+id, nil, &meta); err != nil || meta.FileSize != 4 || meta.DownloadLink == "" {
		t.Fatalf("%+v %v", meta, err)
	}
	var buf bytes.Buffer
	if n, err := c.download(bg, "/wiki"+meta.DownloadLink, &buf); err != nil || n != 4 || buf.String() != "png1" {
		t.Fatalf("%d %v %q", n, err, buf.String())
	}
	if !slices.Contains(srv.Requests, "GET /media/"+id) {
		t.Fatalf("download must follow the redirect: %v", srv.Requests)
	}
	var updated struct {
		Version struct{ Number int } `json:"version"`
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment/"+id+"/data", "rollback flow.png", strings.NewReader("png2"), &updated); err != nil || updated.Version.Number != 2 {
		t.Fatalf("%+v %v", updated, err)
	}
	if err := c.upload(bg, "/wiki/rest/api/content/98130/child/attachment", "rollback flow.png", strings.NewReader("dup"), nil); codeOf(err) != "400" {
		t.Fatalf("duplicate title must be refused: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/wiki/rest/api/content/98130/child/attachment", strings.NewReader(""))
	req.SetBasicAuth("me@x.com", "t")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("missing XSRF header must be 403: %v %v", resp, err)
	}
	if err := c.do(bg, http.MethodDelete, "/wiki/api/v2/attachments/"+id, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Attachment(id); ok {
		t.Fatal("not deleted")
	}
	srv.Fail = map[string]int{"GET /wiki/api/v2/pages/98130/attachments": 500}
	err := c.paginate(bg, "/wiki/api/v2/pages/98130/attachments?limit=250", func(json.RawMessage) error { return nil })
	if codeOf(err) != "500" {
		t.Fatalf("fault injection: %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestTransfersStream(t *testing.T) {
	const size = 64 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				io.Copy(io.Discard, p)
			}
			w.Write([]byte(`{}`))
			return
		}
		io.CopyN(w, zeros{}, size)
	}))
	defer srv.Close()
	c := newClient(target{base: srv.URL, email: "e", token: "t"})
	measure := func(name string, f func() error) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runtime.ReadMemStats(&after)
		if d := after.TotalAlloc - before.TotalAlloc; d > 16<<20 {
			t.Errorf("%s allocated %d MB for %d MB", name, d>>20, size>>20)
		}
	}
	measure("download", func() error {
		n, err := c.download(bg, "/blob", io.Discard)
		if err == nil && n != size {
			t.Errorf("downloaded %d bytes", n)
		}
		return err
	})
	measure("upload", func() error { return c.upload(bg, "/blob", "big.bin", io.LimitReader(zeros{}, size), nil) })
}
