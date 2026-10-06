package atlassian

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBitbucketErrorShape(t *testing.T) {
	ae := &APIError{Product: "Bitbucket", Status: 400,
		Body: `{"type":"error","error":{"message":"Bad","detail":"why","fields":{"title":["required","short"]}}}`}
	if got := ae.Message(); got != "Bad: why; title: required, short" {
		t.Fatal(got)
	}
	if got := (&APIError{Body: `{"type":"error","error":{"message":"Only"}}`}).Message(); got != "Only" {
		t.Fatal(got)
	}
}

func TestRange(t *testing.T) {
	data := []byte("0123456789")
	cases := []struct {
		name      string
		h         http.HandlerFunc
		from, to  int64
		want      string
		wantTotal int64
	}{
		{"206", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") != "bytes=0-3" {
				t.Errorf("Range %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(206)
			w.Write(data[:4])
		}, 0, 3, "0123", 10},
		{"206 open end, unknown total", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") != "bytes=6-" {
				t.Errorf("Range %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 6-9/*")
			w.WriteHeader(206)
			w.Write(data[6:])
		}, 6, -1, "6789", -1},
		{"200 ignores Range", func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, 0, 3, "0123", 10},
		{"200 from offset", func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, 8, -1, "89", 10},
		{"416", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes */10")
			w.WriteHeader(416)
		}, 10, -1, "", 10},
		{"404 before start", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, 0, -1, "", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.h)
			defer srv.Close()
			cl := New(Target{Base: srv.URL}, "Bitbucket")
			var b bytes.Buffer
			total, err := cl.Range(bg, "/log", c.from, c.to, &b)
			if err != nil || b.String() != c.want || total != c.wantTotal {
				t.Fatalf("%q %d %v", b.String(), total, err)
			}
		})
	}
}

func TestNearLimit(t *testing.T) {
	near := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if near {
			w.Header().Set("X-RateLimit-NearLimit", "true")
		}
		io.WriteString(w, "{}")
	}))
	defer srv.Close()
	cl := New(Target{Base: srv.URL}, "Bitbucket")
	cl.Do(bg, "GET", "/", nil, nil)
	if cl.NearLimit() {
		t.Fatal("not near")
	}
	near = true
	cl.Do(bg, "GET", "/", nil, nil)
	if !cl.NearLimit() {
		t.Fatal("near")
	}
}
