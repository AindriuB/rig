//go:build !windows

package proc

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const dumpEnv = `echo "J=$RIG_JIRA_TOKEN"; echo "X=$RIG_OTHER_TOKEN"; echo "A=$RIG_ACTOR"; echo "H=$HOME"; echo "P=$PATH"`

func scrubSetup(t *testing.T) {
	t.Helper()
	t.Setenv("RIG_JIRA_TOKEN", "secret")
	t.Setenv("RIG_OTHER_TOKEN", "secret2")
	t.Setenv("RIG_ACTOR", "me")
}

func checkScrubbed(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"J=\n", "X=\n", "A=me\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "H=\n") || strings.Contains(out, "P=\n") {
		t.Fatalf("HOME or PATH lost: %q", out)
	}
}

func TestRunScrubTokens(t *testing.T) {
	scrubSetup(t)
	res, err := Run(context.Background(), Options{ScrubTokens: true}, "sh", "-c", dumpEnv)
	if err != nil {
		t.Fatal(err)
	}
	checkScrubbed(t, string(res.Stdout))
}

func TestInteractiveScrubTokens(t *testing.T) {
	scrubSetup(t)
	var out bytes.Buffer
	if _, err := Interactive(context.Background(), Options{ScrubTokens: true, Stdout: &out}, "sh", "-c", dumpEnv); err != nil {
		t.Fatal(err)
	}
	checkScrubbed(t, out.String())
}

func TestRunWithoutScrubKeepsTokens(t *testing.T) {
	scrubSetup(t)
	res, err := Run(context.Background(), Options{}, "sh", "-c", dumpEnv)
	if err != nil || !strings.Contains(string(res.Stdout), "J=secret\n") {
		t.Fatalf("got %q, %v", res.Stdout, err)
	}
}
