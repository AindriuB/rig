package proc

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildEnvNilWithoutOptions(t *testing.T) {
	if got := buildEnv(Options{}, []string{"A=1"}); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestBuildEnvAllowlist(t *testing.T) {
	parent := []string{"PATH=/bin", "GH_TOKEN=x", "LC_ALL=C", "LC_CTYPE=y", "HOME=/h"}
	o := Options{EnvAllow: []string{"PATH", "LC_*"}, Env: []string{"GH_TOKEN=given"}}
	got := strings.Join(buildEnv(o, parent), " ")
	if got != "PATH=/bin LC_ALL=C LC_CTYPE=y GH_TOKEN=given" {
		t.Fatalf("got %q", got)
	}
	empty := buildEnv(Options{EnvAllow: []string{}}, parent)
	if len(empty) != 0 {
		t.Fatalf("empty allowlist kept %v", empty)
	}
}

func TestBuildEnvAllowlistCase(t *testing.T) {
	got := buildEnv(Options{EnvAllow: []string{"path"}}, []string{"PATH=/bin", "HOME=/h"})
	want := runtime.GOOS == "windows"
	if (len(got) == 1) != want {
		t.Fatalf("got %v, windows=%v", got, want)
	}
}
