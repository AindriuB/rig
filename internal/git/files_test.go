package git

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func commitFile(t *testing.T, repo, name, msg string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, repo, "add", name)
	sh(t, repo, "commit", "-q", "-m", msg)
}

func TestLsFiles(t *testing.T) {
	repo, _ := newRepo(t)
	commitFile(t, repo, "src/b.go", "add b")
	commitFile(t, repo, "src/a.go", "add a")
	commitFile(t, repo, "src/deep/c.go", "add c")
	commitFile(t, repo, "README.md", "add readme")
	r := Repo{Dir: repo}

	got, err := r.LsFiles(ctx, []string{":(glob)src/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"src/a.go", "src/b.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("single-star glob = %v, want %v", got, want)
	}
	got, err = r.LsFiles(ctx, []string{":(glob)src/**/*.go", "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "src/a.go", "src/b.go", "src/deep/c.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("double-star glob = %v, want %v", got, want)
	}
	if got, err := r.LsFiles(ctx, nil); err != nil || got != nil {
		t.Errorf("no pathspecs = %v, %v; want nil, nil", got, err)
	}
	if got, err := r.LsFiles(ctx, []string{"nothing-here"}); err != nil || len(got) != 0 {
		t.Errorf("no match = %v, %v", got, err)
	}
}

func TestLogPaths(t *testing.T) {
	repo, _ := newRepo(t)
	commitFile(t, repo, "a.txt", "one")
	commitFile(t, repo, "b.txt", "two")
	commitFile(t, repo, "a.txt", "three")
	r := Repo{Dir: repo}

	got, err := r.LogPaths(ctx, "main", []string{"a.txt"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "three" || got[1].Subject != "one" || len(got[0].SHA) != 40 {
		t.Fatalf("log = %+v", got)
	}
	if got, _ := r.LogPaths(ctx, "main", []string{"a.txt"}, 1); len(got) != 1 {
		t.Errorf("limit 1 gave %d commits", len(got))
	}
	if got, err := r.LogPaths(ctx, "main", nil, 5); err != nil || got != nil {
		t.Errorf("no pathspecs = %v, %v", got, err)
	}
	if _, err := r.LogPaths(ctx, "--all", []string{"a.txt"}, 5); err == nil {
		t.Error("rev starting with - must be rejected")
	}
}

func TestLsFilesFromSubdirectory(t *testing.T) {
	repo, _ := newRepo(t)
	commitFile(t, repo, "src/a.go", "add a")
	r := Repo{Dir: filepath.Join(repo, "src")}
	got, err := r.LsFiles(ctx, []string{":(top,glob)src/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"src/a.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("from a subdirectory = %v, want %v", got, want)
	}
}
