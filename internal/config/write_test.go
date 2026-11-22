package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// git runs git in dir with no user or system config and a fixed identity.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// repoEnv is an env whose root is a git repository with one commit.
func repoEnv(t *testing.T) env {
	t.Helper()
	e := newEnv(t)
	git(t, e.root, "init", "-q", "-b", "main")
	put(t, filepath.Join(e.root, "README"), "x\n")
	git(t, e.root, "add", "README")
	git(t, e.root, "commit", "-q", "-m", "first")
	return e
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}

// AC3: writes go through a temp file, store nested objects and leave no
// temp file; the layers see the new value at once and after a reopen.
func TestSetWritesNestedAtomically(t *testing.T) {
	e := repoEnv(t)
	l := open(t, e)
	steps := []struct{ scope, key, value string }{
		{ScopeGlobal, KeyTrackerKind, "jira"},
		{ScopeGlobal, KeyTrackerURL, "https://acme.atlassian.net"},
		{ScopeRepo, KeyTrackerProject, "RIG"},
		{ScopeLocal, KeyForgeKind, "gitlab"},
	}
	for _, s := range steps {
		if err := l.Set(s.scope, s.key, s.value); err != nil {
			t.Fatalf("Set(%s, %s): %v", s.scope, s.key, err)
		}
		if v, _ := l.Get(s.key); v.Value != s.value || v.Scope != s.scope {
			t.Errorf("Get(%s) after Set = %+v", s.key, v)
		}
	}
	global := filepath.Join(e.home, ".rig", "config.json")
	want := map[string]any{
		"tracker": map[string]any{"kind": "jira", "url": "https://acme.atlassian.net"},
		"repos":   map[string]any{"github.com/acme/rig": map[string]any{"tracker": map[string]any{"project": "RIG"}}},
	}
	if got := readJSON(t, global); !reflect.DeepEqual(got, want) {
		t.Errorf("global file = %v", got)
	}
	if got := readJSON(t, filepath.Join(e.root, ".rig", "config.json")); !reflect.DeepEqual(got, map[string]any{"forge": map[string]any{"kind": "gitlab"}}) {
		t.Errorf("local file = %v", got)
	}
	noTemps(t, filepath.Dir(global))
	noTemps(t, filepath.Join(e.root, ".rig"))
	tr, err := open(t, e).Tracker()
	if err != nil || tr.Project != "RIG" {
		t.Errorf("Tracker after reopen = %+v, %v", tr, err)
	}
}

func TestSetKeepsOtherContent(t *testing.T) {
	e := newEnv(t)
	e.global(t, `{"workflow":"two-phase","push":{"prefixes":["task"]},"repos":{"github.com/acme/other":{"tracker":{"project":"OTHER"}}}}`)
	l := open(t, e)
	if err := l.Set(ScopeGlobal, KeyForgeHost, "github.com"); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, filepath.Join(e.home, ".rig", "config.json"))
	if got["workflow"] != "two-phase" || got["push"] == nil || got["repos"] == nil {
		t.Errorf("global file lost content: %v", got)
	}
}

func TestUnset(t *testing.T) {
	e := newEnv(t)
	e.global(t, `{"forge":{"kind":"gitlab"},"tracker":{"kind":"github"},"repos":{"github.com/acme/rig":{"tracker":{"kind":"jira"}}}}`)
	l := open(t, e)
	for _, s := range []string{ScopeRepo, ScopeGlobal} {
		if err := l.Unset(s, KeyTrackerKind); err != nil {
			t.Fatal(err)
		}
	}
	got := readJSON(t, filepath.Join(e.home, ".rig", "config.json"))
	if !reflect.DeepEqual(got, map[string]any{"forge": map[string]any{"kind": "gitlab"}}) {
		t.Errorf("after Unset = %v", got)
	}
	if v, _ := l.Get(KeyTrackerKind); v.Scope != ScopeDefault {
		t.Errorf("tracker.kind = %+v", v)
	}
	// Not set: no error, and no local directory is created.
	if err := l.Unset(ScopeLocal, KeyForgeHost); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.root, ".rig")); !os.IsNotExist(err) {
		t.Errorf(".rig created by an Unset: %v", err)
	}
}

// AC2 and AC10: Set and Unset refuse the same keys in every scope, and
// write nothing.
func TestSetRefuses(t *testing.T) {
	e := repoEnv(t)
	l := open(t, e)
	for _, scope := range []string{ScopeLocal, ScopeRepo, ScopeGlobal} {
		for _, key := range []string{"tracker.token", "Jira.APIKEY", "x.Password", "forge.auth", "tracker.credential", "a.secret", "forge.key"} {
			if err := l.Set(scope, key, "v"); err == nil || !strings.Contains(err.Error(), "rig login") {
				t.Errorf("Set(%s, %s) = %v", scope, key, err)
			}
		}
		for _, key := range []string{"push.prefixes", "pushPrefixes", "push_prefixes", "workflow", "nope", "forge"} {
			if err := l.Set(scope, key, "basic"); err == nil {
				t.Errorf("Set(%s, %s) = nil", scope, key)
			}
			if err := l.Unset(scope, key); err == nil {
				t.Errorf("Unset(%s, %s) = nil", scope, key)
			}
		}
		for key, v := range map[string]string{
			KeyForgeKind: "bitbucket", KeyTrackerKind: "plain",
			KeyTrackerURL: "http://x.example", KeyTrackerProject: "lower",
		} {
			if err := l.Set(scope, key, v); err == nil {
				t.Errorf("Set(%s, %s, %s) = nil", scope, key, v)
			}
		}
	}
	for _, scope := range []string{ScopeDefault, ScopeRigJSON, ""} {
		if err := l.Set(scope, KeyForgeKind, "github"); err == nil {
			t.Errorf("Set in scope %q = nil", scope)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, ".rig")); !os.IsNotExist(err) {
		t.Error("a refused Set wrote the global file")
	}
	if _, err := os.Stat(filepath.Join(e.root, ".rig")); !os.IsNotExist(err) {
		t.Error("a refused Set created .rig")
	}
}

func TestSetProjectCheckedAgainstKind(t *testing.T) {
	e := newEnv(t)
	l := open(t, e)
	if err := l.Set(ScopeRepo, KeyTrackerProject, "acme/rig"); err != nil {
		t.Fatalf("project under local tracker: %v", err)
	}
	if err := l.Set(ScopeGlobal, KeyTrackerKind, "jira"); err != nil {
		t.Fatal(err)
	}
	if err := l.Set(ScopeRepo, KeyTrackerProject, "acme/rig"); err == nil {
		t.Error("owner/name accepted for a jira tracker")
	}
	if err := l.Set(ScopeRepo, KeyTrackerProject, "RIG"); err != nil {
		t.Error(err)
	}
}

// AC8: no origin, or one with no repo key, makes the repo scope an error.
func TestRepoScopeNeedsRemote(t *testing.T) {
	for _, remote := range []string{"", "/srv/git/rig.git"} {
		e := newEnv(t)
		o := e.opts()
		o.Remote = remote
		l, err := Open(o)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Set(ScopeRepo, KeyTrackerProject, "RIG"); err == nil {
			t.Errorf("remote %q: Set --repo = nil", remote)
		}
	}
}

func TestLocalScopeNeedsRoot(t *testing.T) {
	e := newEnv(t)
	o := e.opts()
	o.Root = ""
	l, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Set(ScopeLocal, KeyForgeKind, "github"); err == nil {
		t.Error("Set --local outside a repository = nil")
	}
}

func status(t *testing.T, root string) string {
	return git(t, root, "status", "--porcelain", "--untracked-files=all")
}

func gitignore(t *testing.T, root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// AC9: creating .rig adds /.rig/ to .gitignore once and changes nothing
// else.
func TestLocalSetAddsIgnoreLineOnce(t *testing.T) {
	e := repoEnv(t)
	put(t, filepath.Join(e.root, ".gitignore"), "*.log")
	git(t, e.root, "add", ".gitignore")
	git(t, e.root, "commit", "-q", "-m", "ignore")
	if s := status(t, e.root); s != "" {
		t.Fatalf("status before = %q", s)
	}
	l := open(t, e)
	if err := l.Set(ScopeLocal, KeyForgeKind, "github"); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e.root); got != " M .gitignore\n" {
		t.Errorf("status after = %q, want only .gitignore", got)
	}
	if got := gitignore(t, e.root); got != "*.log\n/.rig/\n" {
		t.Errorf(".gitignore = %q", got)
	}
	// A second local set leaves .gitignore alone.
	if err := l.Set(ScopeLocal, KeyForgeHost, "github.com"); err != nil {
		t.Fatal(err)
	}
	if got := gitignore(t, e.root); got != "*.log\n/.rig/\n" {
		t.Errorf(".gitignore after second set = %q", got)
	}
}

func TestLocalSetCreatesGitignore(t *testing.T) {
	e := repoEnv(t)
	if err := open(t, e).Set(ScopeLocal, KeyForgeKind, "github"); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e.root); got != "?? .gitignore\n" {
		t.Errorf("status = %q", got)
	}
	if got := gitignore(t, e.root); got != "/.rig/\n" {
		t.Errorf(".gitignore = %q", got)
	}
}

func TestLocalSetLeavesGitignore(t *testing.T) {
	cases := map[string]func(t *testing.T, e env){
		"already ignored": func(t *testing.T, e env) {
			put(t, filepath.Join(e.root, ".gitignore"), "bin/\n.rig/\n")
		},
		"existing .rig with the line removed": func(t *testing.T, e env) {
			put(t, filepath.Join(e.root, ".gitignore"), "bin/\n")
			if err := os.Mkdir(filepath.Join(e.root, ".rig"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"tracked .rig/config.json": func(t *testing.T, e env) {
			put(t, filepath.Join(e.root, ".gitignore"), "bin/\n")
			e.local(t, `{"forge":{"kind":"gitlab"}}`)
			git(t, e.root, "add", ".rig/config.json")
			git(t, e.root, "commit", "-q", "-m", "share config")
			if err := os.RemoveAll(filepath.Join(e.root, ".rig")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := repoEnv(t)
			setup(t, e)
			before := gitignore(t, e.root)
			if err := open(t, e).Set(ScopeLocal, KeyForgeKind, "github"); err != nil {
				t.Fatal(err)
			}
			if got := gitignore(t, e.root); got != before {
				t.Errorf(".gitignore = %q, was %q", got, before)
			}
			if v, _ := open(t, e).Get(KeyForgeKind); v.Value != "github" || v.Scope != ScopeLocal {
				t.Errorf("forge.kind = %+v", v)
			}
		})
	}
}

func TestLocalSetRefusesSymlinks(t *testing.T) {
	cases := map[string]func(t *testing.T, e env, outside string){
		"symlinked .gitignore": func(t *testing.T, e env, outside string) {
			if err := os.Symlink(outside, filepath.Join(e.root, ".gitignore")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
		},
		"symlinked .rig directory": func(t *testing.T, e env, outside string) {
			if err := os.Mkdir(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(e.root, ".rig")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
		},
		"symlinked .rig/config.json": func(t *testing.T, e env, outside string) {
			if err := os.Mkdir(filepath.Join(e.root, ".rig"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(e.root, ".rig", "config.json")); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := repoEnv(t)
			outside := filepath.Join(t.TempDir(), "victim")
			setup(t, e, outside)
			err := open(t, e).Set(ScopeLocal, KeyForgeKind, "github")
			if err == nil {
				t.Fatal("Set succeeded through a symlink")
			}
			entries, _ := os.ReadDir(filepath.Dir(outside))
			for _, en := range entries {
				if en.Name() == "victim" && en.IsDir() {
					sub, _ := os.ReadDir(outside)
					if len(sub) != 0 {
						t.Errorf("outside directory changed: %v", sub)
					}
				} else if en.Name() != "victim" {
					t.Errorf("unexpected file outside: %s", en.Name())
				}
			}
			if _, err := os.Lstat(outside); err == nil && name != "symlinked .rig directory" {
				t.Errorf("outside target was created")
			}
		})
	}
}
