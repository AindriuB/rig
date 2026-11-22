//go:build darwin || linux

package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stub is a fake keychain program placed first on PATH. It records its
// argument vector and stdin, prints the contents of "out" and exits with the
// number in "code".
type stub struct{ dir string }

func newStub(t *testing.T, name string) *stub {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"d=\"$STUB_DIR\"\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"$d/argv\"; done\n" +
		"cat >> \"$d/stdin\"\n" +
		"[ -f \"$d/out\" ] && cat \"$d/out\"\n" +
		"[ -f \"$d/code\" ] && exit \"$(cat \"$d/code\")\"\n" +
		"exit 0\n"
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_DIR", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &stub{dir: dir}
}

func (s *stub) read(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, file))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func (s *stub) write(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *stub) reset() {
	for _, f := range []string{"argv", "stdin", "out", "code"} {
		os.Remove(filepath.Join(s.dir, f))
	}
}

const testToken = "s3cret-token-value"

func noToken(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, testToken) {
		t.Errorf("token appears in %s: %q", where, text)
	}
}
