package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

func testBar(width int) (*bar, *bytes.Buffer, func(time.Duration)) {
	var out bytes.Buffer
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	b := &bar{w: &out, width: func() int { return width }, now: func() time.Time { return now }}
	return b, &out, func(d time.Duration) { now = now.Add(d) }
}

// frame is the last line drawn: text after the last carriage return, minus the erase code.
func frame(out *bytes.Buffer) string {
	s := out.String()
	s = s[strings.LastIndex(s, "\r")+1:]
	return strings.TrimSuffix(s, "\x1b[K")
}

func TestBarFetchLine(t *testing.T) {
	b, out, advance := testBar(100)
	b.update(adapter.Progress{Phase: "fetch", Done: 0, Total: 5870, Item: "eng/Home.xml"})
	advance(70 * time.Second)
	b.update(adapter.Progress{Phase: "fetch", Done: 2641, Total: 5870, Item: "eng/Home/Architecture.xml"})
	got := frame(out)
	for _, want := range []string{"Fetching", "[", "2,641/5,870", "45%", "38/s", "ETA 1m25s", "eng/Home/Architecture.xml"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if len([]rune(got)) > 99 {
		t.Fatalf("line wider than the terminal: %d runes", len([]rune(got)))
	}
}

func TestBarTruncatesLongPaths(t *testing.T) {
	b, out, _ := testBar(60)
	b.update(adapter.Progress{Phase: "fetch", Done: 1, Total: 10, Item: "eng/" + strings.Repeat("Very long title/", 10) + "Page.xml"})
	got := frame(out)
	if n := len([]rune(got)); n > 59 {
		t.Fatalf("%d runes: %q", n, got)
	}
	if !strings.HasSuffix(got, "Page.xml") || !strings.Contains(got, "…") {
		t.Fatalf("a long path keeps its end: %q", got)
	}
}

func TestBarThrottles(t *testing.T) {
	b, out, advance := testBar(80)
	b.update(adapter.Progress{Phase: "fetch", Done: 1, Total: 100})
	b.update(adapter.Progress{Phase: "fetch", Done: 2, Total: 100})
	if n := strings.Count(out.String(), "\r"); n != 1 {
		t.Fatalf("%d draws within 100ms, want 1", n)
	}
	advance(150 * time.Millisecond)
	b.update(adapter.Progress{Phase: "fetch", Done: 3, Total: 100})
	if n := strings.Count(out.String(), "\r"); n != 2 {
		t.Fatalf("%d draws, want 2", n)
	}
}

func TestBarListingAndDone(t *testing.T) {
	b, out, _ := testBar(80)
	b.update(adapter.Progress{Phase: "list"})
	if !strings.Contains(frame(out), "Listing") {
		t.Fatalf("%q", frame(out))
	}
	b.update(adapter.Progress{Phase: "pages", Done: 3, Total: 14, Item: "HF (312 pages)"})
	if got := frame(out); !strings.Contains(got, "3/14 spaces") || !strings.Contains(got, "HF (312 pages)") {
		t.Fatalf("%q", got)
	}
	b.update(adapter.Progress{Phase: "done"})
	if !strings.HasSuffix(out.String(), "\r\x1b[K") {
		t.Fatalf("done must erase the line: %q", out.String())
	}
}

// Output written while the bar is up erases the bar first, so lines never mix.
func TestBarWriterClearsLine(t *testing.T) {
	b, out, _ := testBar(80)
	b.update(adapter.Progress{Phase: "fetch", Done: 1, Total: 2})
	var stdout bytes.Buffer
	w := b.writer(&stdout)
	w.Write([]byte("  +  eng/Home.xml\n"))
	if !strings.HasSuffix(out.String(), "\r\x1b[K") || stdout.String() != "  +  eng/Home.xml\n" {
		t.Fatalf("bar %q stdout %q", out.String(), stdout.String())
	}
}
