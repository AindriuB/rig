package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cfgEnv is an environment whose home is a fresh temp directory.
func cfgEnv(t *testing.T) (env map[string]string, home string) {
	t.Helper()
	home = t.TempDir()
	return map[string]string{"HOME": home, "USERPROFILE": home}, home
}

func globalFile(home string) string { return filepath.Join(home, ".rig", "config.json") }

func TestConfigSetGetUnsetEachScope(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@github.com:acme/rig.git")
	env, home := cfgEnv(t)

	for _, c := range []struct{ scope, key, val string }{
		{"--global", "tracker.kind", "jira"},
		{"--global", "tracker.url", "https://acme.atlassian.net"},
		{"--repo", "tracker.project", "RIG"},
		{"--local", "forge.kind", "gitlab"},
	} {
		if r := rig(t, repo, nil, env, "config", "set", c.key, c.val, c.scope); r.code != 0 {
			t.Fatalf("set %v: %+v", c, r)
		}
		if r := rig(t, repo, nil, env, "config", "get", c.key, c.scope); r.code != 0 || r.out != c.val+"\n" {
			t.Fatalf("get %v: %+v", c, r)
		}
	}
	data, _ := os.ReadFile(globalFile(home))
	if !strings.Contains(string(data), "github.com/acme/rig") {
		t.Fatalf("repo section not in the global file: %s", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".rig", "config.json")); err != nil {
		t.Fatalf("local file: %v", err)
	}

	r := rig(t, repo, nil, env, "config", "get", "tracker.kind", "--json")
	var v struct{ Key, Value, Scope string }
	if err := json.Unmarshal([]byte(r.out), &v); err != nil || v != (struct{ Key, Value, Scope string }{"tracker.kind", "jira", "global"}) {
		t.Fatalf("get --json: %+v %v", r, v)
	}

	// Not set in this scope: exit 1, nothing printed.
	if r := rig(t, repo, nil, env, "config", "get", "tracker.project", "--global"); r.code != 1 || r.out != "" {
		t.Fatalf("get unset: %+v", r)
	}
	if r := rig(t, repo, nil, env, "config", "unset", "tracker.project", "--repo"); r.code != 0 {
		t.Fatalf("unset: %+v", r)
	}
	if r := rig(t, repo, nil, env, "config", "get", "tracker.project"); r.code != 1 {
		t.Fatalf("get after unset: %+v", r)
	}
	if r := rig(t, repo, nil, env, "config", "unset", "tracker.project", "--repo"); r.code != 0 {
		t.Fatalf("unset when unset: %+v", r)
	}
}

func TestConfigGetDefaultAndUnknown(t *testing.T) {
	repo, _ := newRepo(t)
	env, _ := cfgEnv(t)
	if r := rig(t, repo, nil, env, "config", "get", "tracker.kind"); r.code != 0 || r.out != "local\n" {
		t.Fatalf("default: %+v", r)
	}
	if r := rig(t, repo, nil, env, "config", "get", "push.prefixes"); r.code != 0 || r.out != "task,record\n" {
		t.Fatalf("list value: %+v", r)
	}
	if r := rig(t, repo, nil, env, "config", "get", "nope"); r.code != 2 {
		t.Fatalf("unknown key: %+v", r)
	}
}

func TestConfigList(t *testing.T) {
	repo, _ := newRepo(t)
	env, home := cfgEnv(t)
	rig(t, repo, nil, env, "config", "set", "tracker.kind", "github", "--global")

	r := rig(t, repo, nil, env, "config", "list")
	if r.code != 0 || !strings.Contains(r.out, "tracker.kind=github\n") || !strings.Contains(r.out, "workflow=basic\n") {
		t.Fatalf("list: %+v", r)
	}
	r = rig(t, repo, nil, env, "config", "list", "--show-origin")
	if !strings.Contains(r.out, "tracker.kind=github\t(global)\n") || !strings.Contains(r.out, "workflow=basic\t(default)\n") {
		t.Fatalf("show-origin: %+v", r)
	}
	r = rig(t, repo, nil, env, "config", "list", "--global", "--json")
	var vs []struct{ Key, Value, Scope string }
	if err := json.Unmarshal([]byte(r.out), &vs); err != nil || len(vs) != 1 || vs[0].Key != "tracker.kind" || vs[0].Scope != "global" {
		t.Fatalf("list --global --json: %+v %v", r, err)
	}
	if r := rig(t, repo, nil, env, "config", "list", "--local", "--json"); r.code != 0 || strings.TrimSpace(r.out) != "[]" {
		t.Fatalf("empty list: %+v", r)
	}

	// A global-only key in the local file is ignored with a warning on stderr.
	os.MkdirAll(filepath.Join(repo, ".rig"), 0o755)
	os.WriteFile(filepath.Join(repo, ".rig", "config.json"), []byte(`{"workflow":"basic"}`), 0o644)
	r = rig(t, repo, nil, env, "config", "list")
	if r.code != 0 || !strings.Contains(r.errOut, "workflow") || strings.Contains(r.out, "warning") {
		t.Fatalf("warnings: %+v", r)
	}
	_ = home
}

func TestConfigRefusalsWriteNothing(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@github.com:acme/rig.git")
	env, home := cfgEnv(t)
	for _, key := range []string{"pushPrefixes", "push.prefixes", "workflow", "jira.token", "tracker.kind=x"} {
		for _, scope := range []string{"--global", "--repo", "--local"} {
			if r := rig(t, repo, nil, env, "config", "set", key, "x", scope); r.code != 2 {
				t.Fatalf("set %s %s: %+v", key, scope, r)
			}
		}
	}
	if r := rig(t, repo, nil, env, "config", "set", "jira.token", "hunter2", "--global"); strings.Contains(r.out+r.errOut, "hunter2") {
		t.Fatalf("the refused value was echoed: %+v", r)
	}
	if _, err := os.Stat(globalFile(home)); err == nil {
		t.Fatal("a refused set wrote the global file")
	}
	if _, err := os.Stat(filepath.Join(repo, ".rig")); err == nil {
		t.Fatal("a refused set created .rig")
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Fatal("a refused set wrote .gitignore")
	}
}

func TestConfigUsageErrors(t *testing.T) {
	repo, _ := newRepo(t)
	env, _ := cfgEnv(t)
	for _, args := range [][]string{
		{"config"},
		{"config", "frob"},
		{"config", "get", "tracker.kind", "--bogus"},
		{"config", "get"},
		{"config", "get", "tracker.kind", "--global", "--local"},
		{"config", "set", "tracker.kind", "jira"},
		{"config", "set", "tracker.kind", "jira", "--global", "--json"},
		{"config", "unset", "tracker.kind"},
		{"config", "list", "extra"},
		{"config", "list", "--repo-dir", "x"},
	} {
		if r := rig(t, repo, nil, env, args...); r.code != 2 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
}

func TestConfigOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	env, _ := cfgEnv(t)
	for _, scope := range []string{"--local", "--repo"} {
		if r := rig(t, dir, nil, env, "config", "set", "tracker.kind", "github", scope); r.code != 2 {
			t.Fatalf("set %s: %+v", scope, r)
		}
	}
	if r := rig(t, dir, nil, env, "config", "set", "tracker.kind", "github", "--global"); r.code != 0 {
		t.Fatalf("set --global: %+v", r)
	}
	if r := rig(t, dir, nil, env, "config", "get", "tracker.kind"); r.code != 0 || r.out != "github\n" {
		t.Fatalf("get: %+v", r)
	}
}

func TestConfigRepoWithoutOrigin(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "remove", "origin")
	env, _ := cfgEnv(t)
	if r := rig(t, repo, nil, env, "config", "set", "tracker.project", "RIG", "--repo"); r.code != 2 {
		t.Fatalf("--repo without origin: %+v", r)
	}
}

func TestConfigLocalSharedByWorktrees(t *testing.T) {
	repo, _ := newRepo(t)
	env, _ := cfgEnv(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	gitc(t, repo, "worktree", "add", "-q", wt, "task/07-foo")
	if r := rig(t, wt, nil, env, "config", "set", "forge.kind", "gitlab", "--local"); r.code != 0 {
		t.Fatalf("set from worktree: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(repo, ".rig", "config.json")); err != nil {
		t.Fatalf("local file is not in the main checkout: %v", err)
	}
}

func TestLoginAndLogoutStubs(t *testing.T) {
	for _, c := range []string{"login", "logout"} {
		r := rig(t, t.TempDir(), nil, nil, c, "github")
		if r.code != 2 || !strings.Contains(r.errOut, "not built yet") {
			t.Fatalf("%s: %+v", c, r)
		}
	}
}

func TestUsageListsConfigAndLogin(t *testing.T) {
	r := rig(t, t.TempDir(), nil, nil, "help")
	for _, want := range []string{"rig config get", "rig config set", "rig config unset", "rig config list", "rig login <service>", "rig logout <service>"} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("usage lacks %q", want)
		}
	}
}

// TestConfigGetListNeedScope: get and list exit 2 when the asked scope does
// not exist.
func TestConfigGetListNeedScope(t *testing.T) {
	env, _ := cfgEnv(t)
	noOrigin, _ := newRepo(t)
	gitc(t, noOrigin, "remote", "remove", "origin")
	localOrigin, _ := newRepo(t) // its origin is a local path: no repo key
	for _, c := range []struct {
		name, dir, scope string
	}{
		{"local outside a repository", t.TempDir(), "--local"},
		{"repo outside a repository", t.TempDir(), "--repo"},
		{"repo with no origin", noOrigin, "--repo"},
		{"repo with a local-path origin", localOrigin, "--repo"},
	} {
		for _, args := range [][]string{
			{"config", "get", "tracker.kind", c.scope},
			{"config", "list", c.scope},
		} {
			t.Run(c.name+"/"+args[1], func(t *testing.T) {
				if r := rig(t, c.dir, nil, env, args...); r.code != 2 {
					t.Fatalf("%v: %+v", args, r)
				}
			})
		}
	}
}

func TestConfigBareRepoWithWorktree(t *testing.T) {
	src, _ := newRepo(t)
	root := filepath.Dir(src)
	bare, wt := filepath.Join(root, "bare.git"), filepath.Join(root, "wt")
	gitc(t, root, "clone", "-q", "--bare", src, bare)
	gitc(t, bare, "worktree", "add", "-q", "../wt", "main")
	env, _ := cfgEnv(t)

	if r := rig(t, wt, nil, env, "config", "set", "forge.kind", "gitlab", "--local"); r.code != 0 {
		t.Fatalf("set: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(wt, ".rig", "config.json")); err != nil {
		t.Fatalf("local file is not in the worktree: %v", err)
	}
	for _, n := range []string{".rig", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(bare, n)); err == nil {
			t.Fatalf("the bare directory has %s", n)
		}
	}

	// With no local value, the worktree's .claude/rig.json is read.
	os.Remove(filepath.Join(wt, ".rig", "config.json"))
	os.MkdirAll(filepath.Join(wt, ".claude"), 0o755)
	os.WriteFile(filepath.Join(wt, ".claude", "rig.json"), []byte(`{"forge":"github"}`), 0o644)
	if r := rig(t, wt, nil, env, "config", "get", "forge.kind"); r.code != 0 || r.out != "github\n" {
		t.Fatalf("get: %+v", r)
	}
}

func TestConfigHomeFromEnv(t *testing.T) {
	env := map[string]string{"HOME": "/h/home", "USERPROFILE": `C:\u\profile`}
	a := &app{getenv: func(k string) string { return env[k] }}
	got, err := a.homeDir()
	want := "/h/home"
	if runtime.GOOS == "windows" {
		want = `C:\u\profile`
	}
	if err != nil || got != want {
		t.Fatalf("homeDir = %q, %v; want %q", got, err, want)
	}
}
