// Package tty reads a secret from the user without echoing it.
package tty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// ErrEmpty reports that no secret was entered.
var ErrEmpty = errors.New("empty input")

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// ErrInterrupted reports that the user interrupted the prompt (Ctrl-C or
// SIGTERM); the terminal has been restored and the caller should exit.
var ErrInterrupted = errors.New("interrupted")

// Hooks for the terminal calls, replaced in tests.
var (
	getState     = term.GetState
	restoreState = term.Restore
	readPassword = term.ReadPassword
)

// readGuarded reads a password from fd with echo off. If a signal arrives on
// sigs first, it restores the saved terminal state and returns ErrInterrupted.
func readGuarded(fd int, sigs <-chan os.Signal) ([]byte, error) {
	st, err := getState(fd)
	if err != nil {
		return nil, err
	}
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := readPassword(fd)
		done <- result{b, err}
	}()
	select {
	case r := <-done:
		_ = restoreState(fd, st)
		return r.b, r.err
	case <-sigs:
		_ = restoreState(fd, st)
		return nil, ErrInterrupted
	}
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
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		b, err := readGuarded(int(in.Fd()), sigs)
		signal.Stop(sigs)
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		s = string(b)
	} else {
		line, err := readLine(in)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		s = strings.TrimRight(line, "\r\n")
	}
	if s == "" {
		return "", fmt.Errorf("read secret: %w", ErrEmpty)
	}
	return s, nil
}

// readLine reads up to and including '\n' one byte at a time, so nothing past
// the line is consumed from a shared pipe. EOF before any byte is ErrEmpty.
func readLine(r io.Reader) (string, error) {
	var line []byte
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			line = append(line, b[0])
			if b[0] == '\n' {
				return string(line), nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return "", ErrEmpty
				}
				return string(line), nil
			}
			return "", err
		}
	}
}
