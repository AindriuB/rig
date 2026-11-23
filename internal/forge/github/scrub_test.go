//go:build !windows

package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
)

// The forge client must not hand a tracker token to gh, for a plain call and
// for the blocking watch.
func TestGhNeverSeesTrackerToken(t *testing.T) {
	dir := t.TempDir()
	envLog := filepath.Join(dir, "env")
	script := "#!/bin/sh\nprintf '%s\\n' \"jira=${RIG_JIRA_TOKEN-unset}\" >>'" + envLog + "'\necho '[]'\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RIG_JIRA_TOKEN", "s3cret-token")

	c := New("")
	if _, err := c.ListPRs(ctx, forge.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.watch(ctx, "pr", "checks", "1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(envLog)
	got := strings.Fields(string(b))
	if len(got) != 2 || got[0] != "jira=unset" || got[1] != "jira=unset" {
		t.Fatalf("gh saw the token: %q", got)
	}
}
