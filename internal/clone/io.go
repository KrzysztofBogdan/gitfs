package clone

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"golang.org/x/term"
)

type stdIO struct {
	in  *os.File
	out *os.File
	err *os.File
	br  *bufio.Reader
}

func defaultIO() adapter.IO {
	return &stdIO{
		in:  os.Stdin,
		out: os.Stdout,
		err: os.Stderr,
		br:  bufio.NewReader(os.Stdin),
	}
}

func (s *stdIO) Stdout() io.Writer { return s.out }
func (s *stdIO) Stderr() io.Writer { return s.err }

func (s *stdIO) IsTTY() bool {
	return term.IsTerminal(int(s.in.Fd()))
}

func (s *stdIO) ReadLine(prompt string) (string, error) {
	if !s.IsTTY() {
		return "", adapter.ErrNeedsInteractive
	}
	fmt.Fprint(s.err, prompt)
	line, err := s.br.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return trimNewline(line), nil
}

func (s *stdIO) ReadSecret(prompt string) (string, error) {
	if !s.IsTTY() {
		return "", adapter.ErrNeedsInteractive
	}
	fmt.Fprint(s.err, prompt)
	b, err := term.ReadPassword(int(s.in.Fd()))
	fmt.Fprintln(s.err)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
