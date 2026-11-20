//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// With a gitlab.com origin, the real forge selection picks the gitlab adapter
// and the commands print the same neutral lines they print for GitHub.
func TestGitLabOriginSelectsGlab(t *testing.T) {
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@gitlab.com:o/r.git")
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
"mr view 5 "*) echo '{"iid":5,"web_url":"https://gitlab.com/o/r/-/merge_requests/5","title":"Add parser","state":"opened","source_branch":"task/07-foo","target_branch":"main","sha":"abc123","has_conflicts":true,"head_pipeline":{"id":77,"status":"success"}}' ;;
"api projects/:id/pipelines/77/jobs"*) echo '[{"id":1,"name":"build","status":"success"}]' ;;
*) echo "stub: unexpected: $*" >&2; exit 99 ;;
esac
`
	os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho 'gh was called' >&2\nexit 99\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, cwd: repo, getenv: func(string) string { return "" }, forge: selectForge}
	if code := a.run(ctx, []string{"pr", "checks", "5"}); code != 0 || out.String() != "#5  green  task/07-foo\n  pass     build\n" {
		t.Fatalf("pr checks: exit %d\n%s%s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := a.run(ctx, []string{"pr", "wait", "5"}); code != 0 || out.String() != "CI green: PR #5\n" {
		t.Fatalf("pr wait: exit %d\n%s%s", code, out.String(), errOut.String())
	}
}
