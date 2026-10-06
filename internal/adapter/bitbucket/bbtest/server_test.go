package bbtest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, s *Server, path string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", s.URL+path, nil)
	req.SetBasicAuth("me@x.com", "tok")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestPagingAndFilter(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddRepo("api")
	for _, title := range []string{"a", "b", "c"} {
		s.AddPR("api", PR{Title: title, Source: "f-" + title})
	}
	_, body := get(t, s, "/repositories/acme/api/pullrequests?pagelen=2&sort=-updated_on")
	var page struct {
		Next   string
		Values []struct{ Title string }
	}
	json.Unmarshal([]byte(body), &page)
	if len(page.Values) != 2 || page.Values[0].Title != "c" || !strings.HasPrefix(page.Next, s.URL) {
		t.Fatalf("%s", body)
	}
	updated := s.PR("api", 2).Updated.Format("2006-01-02T15:04:05Z")
	_, body = get(t, s, `/repositories/acme/api/pullrequests?q=updated_on+%3E+%22`+updated+`%22`)
	if !strings.Contains(body, `"title":"c"`) || strings.Contains(body, `"title":"b"`) {
		t.Fatalf("%s", body)
	}
}

func TestLogRangeAndAdvance(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddRepo("api")
	uuid := s.AddRun("api", Run{State: "PENDING", Steps: []*Step{{Name: "Build"}}})
	s.Queue("api", 1, 0, "hello ", "world\n")
	step := strings.Trim(s.Run("api", 1).Steps[0].UUID, "{}")
	logPath := "/repositories/acme/api/pipelines/" + uuid + "/steps/" + step + "/log"
	if resp, _ := get(t, s, logPath); resp.StatusCode != 404 {
		t.Fatal("no log before the step starts", resp.StatusCode)
	}
	s.Advance("api", 1) // starts
	s.Advance("api", 1) // "hello "
	resp, body := get(t, s, logPath, "Range", "bytes=0-")
	if resp.StatusCode != 206 || body != "hello " || resp.Header.Get("Content-Range") != "bytes 0-5/*" {
		t.Fatal(resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	s.Advance("api", 1) // "world\n"
	s.Advance("api", 1) // step done, run done
	resp, body = get(t, s, logPath, "Range", "bytes=6-")
	if body != "world\n" || resp.Header.Get("Content-Range") != "bytes 6-11/12" {
		t.Fatal(body, resp.Header.Get("Content-Range"))
	}
	if resp, _ = get(t, s, logPath, "Range", "bytes=12-"); resp.StatusCode != 416 {
		t.Fatal(resp.StatusCode)
	}
	if r := s.Run("api", 1); r.State != "COMPLETED" || r.Result != "SUCCESSFUL" {
		t.Fatalf("%+v", r)
	}
}

func TestRateLimitAndAuth(t *testing.T) {
	s := New()
	defer s.Close()
	s.RateLimit(1)
	if resp, _ := get(t, s, "/user"); resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Fatal(resp.StatusCode)
	}
	if _, body := get(t, s, "/user"); !strings.Contains(body, Me) {
		t.Fatal(body)
	}
	resp, _ := http.Get(s.URL + "/user")
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
}
