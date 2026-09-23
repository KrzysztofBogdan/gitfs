package workdir

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

type LogEntry struct {
	At                                   time.Time
	Verb, Path, NewPath, Outcome, Detail string
}

func (e LogEntry) paths() string {
	if e.NewPath != "" {
		return e.Path + " -> " + e.NewPath
	}
	return e.Path
}

func (e LogEntry) String() string {
	s := strings.Join([]string{e.At.Format(time.RFC3339), e.Verb, e.paths(), e.Outcome}, "  ")
	if e.Detail != "" {
		s += "  " + e.Detail
	}
	return s
}

func (t *Tree) AppendLog(e LogEntry) error {
	f, err := os.OpenFile(t.gfs("log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	clean := func(s string) string { return strings.NewReplacer("\t", " ", "\n", " ").Replace(s) }
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\n", e.At.Format(time.RFC3339), e.Verb, clean(e.paths()), e.Outcome, clean(e.Detail))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (t *Tree) ReadLog() ([]LogEntry, error) {
	f, err := os.Open(t.gfs("log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "\t", 5)
		if len(p) != 5 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, p[0])
		path, np, _ := strings.Cut(p[2], " -> ")
		out = append(out, LogEntry{At: at, Verb: p[1], Path: path, NewPath: np, Outcome: p[3], Detail: p[4]})
	}
	return out, sc.Err()
}
