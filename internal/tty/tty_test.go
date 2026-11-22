package tty

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/term"
)

func fileWith(t *testing.T, content string) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestReadSecretNonTerminalReadsOneLine(t *testing.T) {
	for _, in := range []string{"s3cret\nmore\n", "s3cret\r\nmore\r\n", "s3cret"} {
		var out bytes.Buffer
		got, err := ReadSecret("Token: ", fileWith(t, in), &out)
		if err != nil || got != "s3cret" {
			t.Errorf("input %q: got %q, %v", in, got, err)
		}
		if out.Len() != 0 {
			t.Errorf("input %q: prompt printed to non-terminal: %q", in, out.String())
		}
	}
}

func TestReadSecretEmptyIsError(t *testing.T) {
	for _, in := range []string{"", "\n", "\r\n"} {
		if got, err := ReadSecret("Token: ", fileWith(t, in), &bytes.Buffer{}); err == nil {
			t.Errorf("input %q: got %q, want error", in, got)
		}
	}
}

func TestIsTerminalFalseForFile(t *testing.T) {
	if IsTerminal(fileWith(t, "x")) {
		t.Fatal("regular file reported as terminal")
	}
}

func TestReadSecretTwoLinesFromOnePipe(t *testing.T) {
	f := fileWith(t, "me@example.com\ntok123\n")
	var out bytes.Buffer
	a, err := ReadSecret("", f, &out)
	if err != nil || a != "me@example.com" {
		t.Fatalf("first: %q, %v", a, err)
	}
	b, err := ReadSecret("", f, &out)
	if err != nil || b != "tok123" {
		t.Fatalf("second: %q, %v", b, err)
	}
}

func TestReadGuardedRestoresOnSignal(t *testing.T) {
	oldG, oldR, oldP := getState, restoreState, readPassword
	t.Cleanup(func() { getState, restoreState, readPassword = oldG, oldR, oldP })
	restored := 0
	getState = func(int) (*term.State, error) { return &term.State{}, nil }
	restoreState = func(int, *term.State) error { restored++; return nil }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	readPassword = func(int) ([]byte, error) { <-release; return nil, nil }
	sigs := make(chan os.Signal, 1)
	sigs <- os.Interrupt
	if _, err := readGuarded(0, sigs); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored %d times", restored)
	}
}

func TestReadGuardedRestoresOnNormalReturn(t *testing.T) {
	oldG, oldR, oldP := getState, restoreState, readPassword
	t.Cleanup(func() { getState, restoreState, readPassword = oldG, oldR, oldP })
	restored := 0
	getState = func(int) (*term.State, error) { return &term.State{}, nil }
	restoreState = func(int, *term.State) error { restored++; return nil }
	readPassword = func(int) ([]byte, error) { return []byte("pw"), nil }
	b, err := readGuarded(0, make(chan os.Signal))
	if err != nil || string(b) != "pw" || restored != 1 {
		t.Fatalf("%q %v restored %d", b, err, restored)
	}
}
