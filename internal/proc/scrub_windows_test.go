//go:build windows

package proc

import (
	"context"
	"strings"
	"testing"
)

func TestRunScrubTokensOtherCase(t *testing.T) {
	t.Setenv("rig_jira_token", "secret")
	t.Setenv("RIG_ACTOR", "me")
	res, err := Run(context.Background(), Options{ScrubTokens: true}, "cmd", "/c", "set")
	if err != nil {
		t.Fatal(err)
	}
	out := strings.ToLower(string(res.Stdout))
	if strings.Contains(out, "rig_jira_token") || !strings.Contains(out, "rig_actor=me") {
		t.Fatalf("env: %q", res.Stdout)
	}
}
