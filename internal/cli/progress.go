package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

const (
	barCells    = 24
	redrawEvery = 100 * time.Millisecond
)

// bar draws clone and pull progress on one terminal line, redrawn in place.
type bar struct {
	w     io.Writer
	width func() int
	now   func() time.Time

	phase     string
	last      time.Time // last draw
	shown     bool      // a line is on screen
	fetching  bool      // inside a fetch phase; a retry wait does not end it
	start     time.Time // fetch phase start, for the rate
	startDone int
}

// progressBar returns a bar on stderr when it is a terminal and not quiet, else nil.
func progressBar(cmd *cobra.Command, quiet bool) *bar {
	f, ok := cmd.ErrOrStderr().(*os.File)
	if quiet || !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	return &bar{w: f, now: time.Now, width: func() int {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
		return 80
	}}
}

// attachProgress wires a bar into the session (listing detail, retry waits)
// and returns the progress func for the engine and an output writer that
// keeps lines clean. Without a bar, the session's retry waits are still
// printed, one line each.
func attachProgress(cmd *cobra.Command, quiet bool, sess adapter.Session, out io.Writer) (func(adapter.Progress), io.Writer) {
	b := progressBar(cmd, quiet)
	r, reports := sess.(adapter.Reporter)
	if b == nil {
		if reports {
			r.SetProgress(waitNotice(cmd.ErrOrStderr()))
		}
		return nil, out
	}
	if reports {
		r.SetProgress(b.update)
	}
	return b.update, b.writer(out)
}

// waitNotice prints retry waits as lines, for output that is not a terminal.
func waitNotice(w io.Writer) func(adapter.Progress) {
	return func(p adapter.Progress) {
		if p.Phase == "wait" {
			fmt.Fprintln(w, p.Item)
		}
	}
}

func (b *bar) update(p adapter.Progress) {
	now := b.now()
	if p.Phase == "done" {
		b.clear()
		b.phase, b.fetching = "", false
		return
	}
	changed := p.Phase != b.phase
	switch p.Phase {
	case "fetch":
		if !b.fetching {
			b.fetching, b.start, b.startDone = true, now, p.Done
		}
	case "list", "pages":
		b.fetching = false
	}
	finished := p.Total > 0 && p.Done >= p.Total
	if !changed && !finished && b.shown && now.Sub(b.last) < redrawEvery {
		return
	}
	b.phase, b.last = p.Phase, now
	fmt.Fprintf(b.w, "\r%s\x1b[K", fit(b.line(p, now), b.width()-1))
	b.shown = true
}

func (b *bar) line(p adapter.Progress, now time.Time) string {
	switch p.Phase {
	case "list":
		return "Listing…"
	case "wait":
		return p.Item
	case "pages":
		return fmt.Sprintf("Listing   %d/%d spaces  %s", p.Done, p.Total, p.Item)
	case "fetch":
		frac := 0.0
		if p.Total > 0 {
			frac = float64(p.Done) / float64(p.Total)
		}
		n := int(frac * barCells)
		s := fmt.Sprintf("Fetching  [%s%s]  %s/%s  %d%%", strings.Repeat("█", n), strings.Repeat("░", barCells-n),
			thousands(p.Done), thousands(p.Total), int(math.Round(frac*100)))
		if el := now.Sub(b.start).Seconds(); el >= 1 && p.Done > b.startDone {
			rate := float64(p.Done-b.startDone) / el
			eta := time.Duration(float64(p.Total-p.Done) / rate * float64(time.Second)).Truncate(time.Second)
			s += fmt.Sprintf("  %.0f/s  ETA %s", rate, eta)
		}
		if p.Item != "" {
			if room := b.width() - 1 - len([]rune(s)) - 2; room > 1 {
				s += "  " + tail(p.Item, room)
			}
		}
		return s
	}
	return p.Phase
}

func (b *bar) clear() {
	if b.shown {
		fmt.Fprint(b.w, "\r\x1b[K")
		b.shown = false
	}
}

// writer erases the bar before each write to w; the next update redraws it.
func (b *bar) writer(w io.Writer) io.Writer { return clearingWriter{b, w} }

type clearingWriter struct {
	b *bar
	w io.Writer
}

func (c clearingWriter) Write(p []byte) (int, error) {
	c.b.clear()
	return c.w.Write(p)
}

// fit cuts s to at most n runes.
func fit(s string, n int) string {
	r := []rune(s)
	if n < 0 {
		n = 0
	}
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// tail keeps the end of s within n runes, marking the cut with "…".
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
