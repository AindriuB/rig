//go:build !windows

package gitlab

import (
	"strings"
	"testing"
)

// A RIG_*_TOKEN in rig's environment must not reach glab.
func TestGlabDoesNotInheritRigTokens(t *testing.T) {
	t.Setenv("RIG_JIRA_TOKEN", "secret")
	stub(t, `"api x"*) echo "token=${RIG_JIRA_TOKEN-unset}" ;;`)
	res, err := client().glab(ctx, "api", "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "token=unset" {
		t.Fatalf("glab saw %q", got)
	}
}
