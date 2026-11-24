package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/forge/fake"
)

var ctx = context.Background()

// gitc runs git in dir with a fixed identity and no user config.
func gitc(t *testing.T, dir string, args ...string) string {
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
	return strings.TrimSpace(string(out))
}

// newRepo makes a repository on main with a bare origin, plus a task file and
// branch for task 07.
func newRepo(t *testing.T) (repo, remote string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, repo = filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	gitc(t, root, "init", "-q", "--bare", "-b", "main", remote)
	gitc(t, root, "init", "-q", "-b", "main", repo)
	os.MkdirAll(filepath.Join(repo, "docs", "plan", "tasks"), 0o755)
	os.WriteFile(filepath.Join(repo, "docs", "plan", "tasks", "07-foo.md"), []byte("# 07\n"), 0o644)
	gitc(t, repo, "add", ".")
	gitc(t, repo, "commit", "-q", "-m", "first")
	gitc(t, repo, "remote", "add", "origin", remote)
	gitc(t, repo, "push", "-q", "origin", "main")
	gitc(t, repo, "branch", "task/07-foo")
	return repo, remote
}

type result struct {
	code        int
	out, errOut string
}

// rig runs the command in dir against the fake forge.
func rig(t *testing.T, dir string, f *fake.Forge, env map[string]string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errOut, cwd: dir,
		getenv: func(k string) string { return env[k] },
		forge: func(context.Context, string) (forge.Forge, error) {
			if f == nil {
				t.Fatal("the command reached for a forge")
			}
			return f, nil
		},
	}
	code := a.run(ctx, args)
	return result{code, out.String(), errOut.String()}
}

func TestUsage(t *testing.T) {
	if r := rig(t, t.TempDir(), nil, nil); r.code != 2 {
		t.Fatalf("no command: exit %d", r.code)
	}
	if r := rig(t, t.TempDir(), nil, nil, "frobnicate"); r.code != 2 {
		t.Fatalf("unknown command: exit %d", r.code)
	}
	for _, arg := range []string{"version", "--version"} {
		if r := rig(t, t.TempDir(), nil, nil, arg); r.code != 0 || r.out != version+"\n" || r.errOut != "" {
			t.Fatalf("%s: %+v", arg, r)
		}
	}
	if r := rig(t, t.TempDir(), nil, nil, "help"); r.code != 0 || !strings.Contains(r.out, "rig push") {
		t.Fatalf("help: %+v", r)
	}
}
