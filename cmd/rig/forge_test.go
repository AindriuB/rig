package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
)

func TestForgeKindReadsTheLayers(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@github.com:o/r.git")
	dir := t.TempDir()
	home := func() (string, error) { return dir, nil }
	os.MkdirAll(filepath.Join(dir, ".rig"), 0o755)
	global := filepath.Join(dir, ".rig", "config.json")

	os.WriteFile(global, []byte(`{"forge":{"kind":"gitlab"}}`), 0o644)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitLab {
		t.Fatalf("global forge.kind: %q, %v", k, err)
	}
	os.WriteFile(global, []byte(`{"forge":{"host":"gitlab.com"}}`), 0o644)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitLab {
		t.Fatalf("global forge.host: %q, %v", k, err)
	}
	os.MkdirAll(filepath.Join(repo, ".claude"), 0o755)
	os.WriteFile(filepath.Join(repo, ".claude", "rig.json"), []byte(`{"forge":"github"}`), 0o644)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitHub {
		t.Fatalf("rig.json should win: %q, %v", k, err)
	}
}

// TestNoCommandReadsTheLegacyConfig keeps push and forge on the layered config:
// config.Load reads .claude/rig.json alone.
func TestNoCommandReadsTheLegacyConfig(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "config.Load(") {
			t.Errorf("%s calls the legacy config.Load; use config.Open", f)
		}
	}
}
