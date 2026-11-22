// Package tty reads a secret from the user without echoing it.
package tty

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrEmpty reports that no secret was entered.
var ErrEmpty = errors.New("empty input")

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// ReadSecret reads one secret from in. When in is a terminal it prints prompt
// to out and reads without echo. Otherwise it reads one line, strips the line
// ending and prints nothing, so a script can pipe the secret in. Empty input
// is an error.
func ReadSecret(prompt string, in *os.File, out io.Writer) (string, error) {
	if in == nil {
		return "", errors.New("read secret: no input")
	}
	var s string
	if IsTerminal(in) {
		if _, err := fmt.Fprint(out, prompt); err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		b, err := term.ReadPassword(int(in.Fd()))
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		s = string(b)
	} else {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("read secret: %w", ErrEmpty)
			}
			return "", fmt.Errorf("read secret: %w", err)
		}
		s = strings.TrimRight(line, "\r\n")
	}
	if s == "" {
		return "", fmt.Errorf("read secret: %w", ErrEmpty)
	}
	return s, nil
}
