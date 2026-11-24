package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/forge"
)

func TestForgeKindReadsTheLayers(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@github.com:o/r.git")
	dir := t.TempDir()
	home := func() (string, error) { return dir, nil }
	mustMkdir(t, filepath.Join(dir, ".rig"))
	global := filepath.Join(dir, ".rig", "config.json")

	mustWrite(t, global, `{"forge":{"kind":"gitlab"}}`)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitLab {
		t.Fatalf("global forge.kind: %q, %v", k, err)
	}
	mustWrite(t, global, `{"forge":{"host":"gitlab.com"}}`)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitLab {
		t.Fatalf("global forge.host: %q, %v", k, err)
	}
	mustMkdir(t, filepath.Join(repo, ".claude"))
	mustWrite(t, filepath.Join(repo, ".claude", "rig.json"), `{"forge":"github"}`)
	if k, err := forgeKind(ctx, repo, home); err != nil || k != forge.GitHub {
		t.Fatalf("rig.json should win: %q, %v", k, err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestForgeKindScopePrecedence: a kind from a less specific scope never
// overrides a host from a more specific one; rig.json is the most specific.
func TestForgeKindScopePrecedence(t *testing.T) {
	const origin = "git@github.com:o/r.git"
	cases := []struct {
		name                string
		global, local, rigj string
		want                forge.Kind
	}{
		{"global kind github, rig.json host gitlab", `{"forge":{"kind":"github"}}`, ``, `{"host":"gitlab.example.com"}`, forge.GitLab},
		{"global kind gitlab, rig.json forge github", `{"forge":{"kind":"gitlab"}}`, ``, `{"forge":"github"}`, forge.GitHub},
		{"local kind gitlab, rig.json forge github", ``, `{"forge":{"kind":"gitlab"}}`, `{"forge":"github"}`, forge.GitLab},
		{"repos kind beats global kind", `{"forge":{"kind":"github"},"repos":{"github.com/o/r":{"forge":{"kind":"gitlab"}}}}`, ``, ``, forge.GitLab},
		{"global kind, local host", `{"forge":{"kind":"gitlab"}}`, `{"forge":{"host":"github.com"}}`, ``, forge.GitHub},
		{"global kind, repos host unknown to KindForHost", `{"forge":{"kind":"gitlab"},"repos":{"github.com/o/r":{"forge":{"host":"git.corp.example"}}}}`, ``, ``, forge.GitLab},
		{"same scope: kind wins", `{"forge":{"kind":"github","host":"gitlab.com"}}`, ``, ``, forge.GitHub},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, _ := newRepo(t)
			gitc(t, repo, "remote", "set-url", "origin", origin)
			dir := t.TempDir()
			home := func() (string, error) { return dir, nil }
			mustMkdir(t, filepath.Join(dir, ".rig"))
			if c.global != "" {
				mustWrite(t, filepath.Join(dir, ".rig", "config.json"), c.global)
			}
			if c.local != "" {
				mustMkdir(t, filepath.Join(repo, ".rig"))
				mustWrite(t, filepath.Join(repo, ".rig", "config.json"), c.local)
			}
			if c.rigj != "" {
				mustMkdir(t, filepath.Join(repo, ".claude"))
				mustWrite(t, filepath.Join(repo, ".claude", "rig.json"), c.rigj)
			}
			if k, err := forgeKind(ctx, repo, home); err != nil || k != c.want {
				t.Fatalf("got %q, %v; want %q", k, err, c.want)
			}
			// rig config get reports both keys from the scopes forgeKind used.
			layers, err := openLayersAt(ctx, repo, home)
			if err != nil {
				t.Fatal(err)
			}
			kv, kOK := layers.Get(config.KeyForgeKind)
			hv, hOK := layers.Get(config.KeyForgeHost)
			kOK = kOK && kv.Value.(string) != ""
			hOK = hOK && hv.Value.(string) != ""
			rank := func(scope string) int {
				for i, s := range config.Order(config.KeyForgeKind) {
					if s == scope {
						return i
					}
				}
				return 99
			}
			var agreed forge.Kind
			switch {
			case kOK && (!hOK || rank(kv.Scope) <= rank(hv.Scope)):
				agreed = forge.Kind(kv.Value.(string))
			default:
				agreed = forge.GitHub
				if hOK {
					if k, ok := forge.KindForHost(hv.Value.(string)); ok {
						agreed = k
					} else if kOK {
						agreed = forge.Kind(kv.Value.(string))
					}
				}
			}
			if agreed != c.want {
				t.Fatalf("rig config get: kind=%v host=%v gives %q, forgeKind = %q", kv, hv, agreed, c.want)
			}
		})
	}
}

// A broken global file is an error; only a failing home lookup is forgiven.
func TestForgeKindBrokenGlobalIsAnError(t *testing.T) {
	for name, global := range map[string]string{
		"unknown key":    `{"forge":{"kind":"gitlab"},"bogus":1}`,
		"truncated json": `{"forge":{"kind":"gitl`,
	} {
		t.Run(name, func(t *testing.T) {
			repo, _ := newRepo(t)
			gitc(t, repo, "remote", "set-url", "origin", "git@git.corp.example:o/r.git")
			dir := t.TempDir()
			mustMkdir(t, filepath.Join(dir, ".rig"))
			path := filepath.Join(dir, ".rig", "config.json")
			mustWrite(t, path, global)
			_, err := forgeKind(ctx, repo, func() (string, error) { return dir, nil })
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v; want one naming %s", err, path)
			}
		})
	}
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "https://gitlab.com/o/r.git")
	k, err := forgeKind(ctx, repo, func() (string, error) { return "", errors.New("no home") })
	if err != nil || k != forge.GitLab {
		t.Fatalf("no home: %q, %v", k, err)
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

// TestForgeKindWithoutHome: with no home directory the global layer is
// empty, and the repository's own files and origin still pick the forge.
func TestForgeKindWithoutHome(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@gitlab.com:o/r.git")
	noHome := func() (string, error) { return "", os.ErrNotExist }
	if k, err := forgeKind(ctx, repo, noHome); err != nil || k != forge.GitLab {
		t.Fatalf("got %q, %v; want gitlab", k, err)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if k, err := forgeKind(ctx, repo, nil); err != nil || k != forge.GitLab {
		t.Fatalf("HOME unset: got %q, %v; want gitlab", k, err)
	}
}
