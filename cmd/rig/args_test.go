package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 051 R1: only a known rig option is ever named in an unknown-option error.
func TestShownOptionNamesOnlyKnownOptions(t *testing.T) {
	for _, secret := range []string{
		"-NjQ2MzE4ODg5ODk0OkZ2dGVzdA", "-glpat-abc123def", "--ghp_abcdef123456", "--jsn", "--Token",
	} {
		if got := shownOption(secret); strings.Contains(got, secret) || !strings.Contains(got, "not shown") {
			t.Errorf("%s: shown as %q", secret, got)
		}
	}
	if got := shownOption("--json"); got != `"--json"` {
		t.Errorf("known option: %q", got)
	}
}

// A known flag used on a command that lacks it is named in the error.
func TestKnownFlagOnWrongCommandIsNamed(t *testing.T) {
	fs := newFlags()
	_, err := fs.parse([]string{"--json"})
	if err == nil || !strings.Contains(err.Error(), `"--json"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := fs.parse([]string{"--jsn"}); err == nil || strings.Contains(err.Error(), "jsn") {
		t.Errorf("typo echoed: %v", err)
	}
}

// Every flag a command registers is in knownOptions, so a new one cannot be
// forgotten silently.
func TestKnownOptionsCoverEveryRegisteredFlag(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?:\.(?:str|boolean)\(|\.(?:strs|bools)\[)"(-[^"]+)"`)
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			seen++
			if !knownOptions[m[1]] {
				t.Errorf("%s registers %s, which is not in knownOptions", f, m[1])
			}
		}
	}
	if seen < 40 {
		t.Errorf("found only %d registrations: the scan is broken", seen)
	}
}
