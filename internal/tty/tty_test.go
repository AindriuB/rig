package tty

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
