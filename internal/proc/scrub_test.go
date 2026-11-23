package proc

import (
	"runtime"
	"slices"
	"testing"
)

func TestBuildEnvScrubsTokens(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/h", "RIG_JIRA_TOKEN=a", "RIG_GITHUB_TOKEN=b", "RIG_ACTOR=me", "RIG__TOKEN=x"}
	got := buildEnv(Options{ScrubTokens: true}, parent)
	want := []string{"PATH=/bin", "HOME=/h", "RIG_ACTOR=me", "RIG__TOKEN=x"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildEnvExplicitWins(t *testing.T) {
	parent := []string{"RIG_JIRA_TOKEN=a", "PATH=/bin"}
	got := buildEnv(Options{ScrubTokens: true, Env: []string{"RIG_JIRA_TOKEN=explicit"}}, parent)
	want := []string{"PATH=/bin", "RIG_JIRA_TOKEN=explicit"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildEnvDefaultUnchanged(t *testing.T) {
	if got := buildEnv(Options{}, []string{"RIG_JIRA_TOKEN=a"}); got != nil {
		t.Fatalf("got %q, want nil (inherit)", got)
	}
	got := buildEnv(Options{Env: []string{"A=1"}}, []string{"RIG_JIRA_TOKEN=a"})
	if want := []string{"RIG_JIRA_TOKEN=a", "A=1"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildEnvCaseSensitivity(t *testing.T) {
	got := buildEnv(Options{ScrubTokens: true}, []string{"rig_jira_token=a", "Rig_X_Token=b", "K=v"})
	if runtime.GOOS == "windows" {
		if want := []string{"K=v"}; !slices.Equal(got, want) {
			t.Fatalf("windows: got %q, want %q", got, want)
		}
		return
	}
	if len(got) != 3 {
		t.Fatalf("exact case expected off Windows: got %q", got)
	}
}
