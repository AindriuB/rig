package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/workspace/cli/internal/forge"
	"github.com/AindriuB/workspace/cli/internal/forge/fake"
)

func TestStatus(t *testing.T) {
	repo, _ := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	gitc(t, repo, "worktree", "add", "-q", wt, "task/07-foo")
	gitc(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
	os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(repo, ".git", handoffFile), []byte("Next: verify 07\nThen record\n"), 0o644)

	f := prs()
	// From inside the worktree: the task files and handoff still come from
	// the main checkout and the common git dir.
	r := kit(t, wt, f, nil, "status")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"HANDOFF from the last session (",
		"  Next: verify 07\n  Then record\n",
		"OPEN TASKS (1):\n  07-foo\n",
		"WORKTREES (1):\n  task/07-foo  ahead 1  dirty 1  " + wt + "\n",
		"OPEN PRS (2):\n  #12  red  task/07-foo  Add parser  conflict  draft\n",
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if f.Calls[0] != "ListPRs {State: Head: Mine:true Limit:15}" {
		t.Fatalf("calls = %q", f.Calls)
	}

	var got statusOut
	r = kit(t, repo, f, nil, "status", "--json")
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if got.Handoff == nil || len(got.Handoff.Lines) != 2 || len(got.Tasks) != 1 || len(got.Worktrees) != 1 ||
		got.Worktrees[0].Ahead != 1 || got.Worktrees[0].Dirty != 1 || len(got.PRs) != 2 || got.PRs[0].CI != forge.CIRed {
		t.Fatalf("json = %+v", got)
	}
}

func TestStatusSurvivesAnUnreachableForgeAndAStaleHandoff(t *testing.T) {
	repo, _ := newRepo(t)
	handoff := filepath.Join(repo, ".git", handoffFile)
	os.WriteFile(handoff, []byte("old news\n"), 0o644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(handoff, old, old)

	r := kit(t, repo, &fake.Forge{Err: errors.New("gh not found on PATH")}, nil, "status")
	if r.code != 0 || !strings.Contains(r.out, "OPEN PRS: unavailable (gh not found on PATH)") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, "HANDOFF") {
		t.Fatalf("an 8-day-old handoff was shown:\n%s", r.out)
	}
	if r := kit(t, repo, prs(), nil, "status", "--verbose"); r.code != 2 {
		t.Fatalf("unknown flag: %+v", r)
	}
	if r := kit(t, t.TempDir(), &fake.Forge{}, nil, "status"); r.code != 0 || !strings.Contains(r.out, "Not in a git repository.") {
		t.Fatalf("outside a repo: %+v", r)
	}
}

func TestForgeKind(t *testing.T) {
	repo, _ := newRepo(t)
	set := func(url string) { gitc(t, repo, "remote", "set-url", "origin", url) }
	cfg := filepath.Join(repo, ".claude", "kit.json")
	os.MkdirAll(filepath.Dir(cfg), 0o755)

	if k, err := forgeKind(ctx, t.TempDir()); err != nil || k != forge.GitHub {
		t.Fatalf("outside a repo: %q, %v", k, err)
	}
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("local-path origin: %q, %v", k, err)
	}
	set("git@github.com:o/r.git")
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("github origin: %q, %v", k, err)
	}
	set("https://gitlab.com/o/r.git")
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitLab {
		t.Fatalf("gitlab origin: %q, %v", k, err)
	}
	set("git@git.example.com:o/r.git")
	// A host that names no forge, such as an SSH config alias, is GitHub.
	for _, url := range []string{"git@git.example.com:o/r.git", "work:o/r.git"} {
		set(url)
		if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
			t.Fatalf("%s: %q, %v", url, k, err)
		}
	}
	os.WriteFile(cfg, []byte(`not json`), 0o644)
	if _, err := forgeKind(ctx, repo); err == nil {
		t.Fatal("a broken kit.json was ignored")
	}
	os.WriteFile(cfg, []byte(`{"host":"gitlab.example.com"}`), 0o644)
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitLab {
		t.Fatalf("host override: %q, %v", k, err)
	}
	os.WriteFile(cfg, []byte(`{"forge":"github"}`), 0o644)
	if k, err := forgeKind(ctx, repo); err != nil || k != forge.GitHub {
		t.Fatalf("forge override: %q, %v", k, err)
	}
}
