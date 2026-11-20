package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/workspace/cli/internal/forge"
	"github.com/AindriuB/workspace/cli/internal/forge/fake"
)

func TestWaitGreen(t *testing.T) {
	f := &fake.Forge{Wait: forge.WaitResult{Green: true}}
	if r := kit(t, t.TempDir(), f, nil, "pr", "wait", "12"); r.code != 0 || r.out != "CI green: PR #12\n" {
		t.Fatalf("%+v", r)
	}
	if r := kit(t, t.TempDir(), f, nil, "pr", "wait", "--run", "345"); r.code != 0 || r.out != "CI green: run 345\n" {
		t.Fatalf("%+v", r)
	}
	if f.Calls[0] != "WaitChecks {PR:12 Run:0}" || f.Calls[1] != "WaitChecks {PR:0 Run:345}" {
		t.Fatalf("calls = %q", f.Calls)
	}
}

func TestWaitRedPR(t *testing.T) {
	f := &fake.Forge{
		Wait: forge.WaitResult{Failed: []forge.Check{{Name: "build"}, {Name: "lint"}}},
		Logs: []forge.LogLine{
			{Job: "build", Text: "build\t-\t\x1b[36;1mstep\x1b[0m\r"},
			{Job: "build", Text: "build\t-\t##[error]boom\r"},
			{Job: "build", Text: "build\t-\tCleaning up\r"},
		},
	}
	r := kit(t, t.TempDir(), f, nil, "pr", "wait", "12")
	want := "CI red: PR #12\nFailed checks:\n  build\n  lint\n--- log tail ---\nbuild\t-\tstep\nbuild\t-\t##[error]boom\n"
	if r.code != 1 || r.out != want {
		t.Fatalf("got %q\nwant %q", r.out, want)
	}

	f.LogsErr = forge.ErrNoLogs
	r = kit(t, t.TempDir(), f, nil, "pr", "wait", "12")
	if r.code != 1 || r.out != "CI red: PR #12\nFailed checks:\n  build\n  lint\n" {
		t.Fatalf("no logs: %q", r.out)
	}
}

func TestWaitRedRun(t *testing.T) {
	f := &fake.Forge{Wait: forge.WaitResult{Conclusion: "failure", Failed: []forge.Check{{Name: "test (ubuntu)"}}}}
	r := kit(t, t.TempDir(), f, nil, "pr", "wait", "--run", "345")
	want := "CI red: run 345 (failure)\nFailed jobs:\n  test (ubuntu)\n--- log tail ---\n(no failed-job log available from gh yet)\n"
	if r.code != 1 || r.out != want {
		t.Fatalf("got %q\nwant %q", r.out, want)
	}
}

func TestWaitErrorsExit2(t *testing.T) {
	f := &fake.Forge{WaitErr: errors.New("gh failed: no checks reported on the 'x' branch")}
	r := kit(t, t.TempDir(), f, nil, "pr", "wait", "12")
	if r.code != 2 || r.errOut != "ci-wait: gh failed: no checks reported on the 'x' branch\n" || r.out != "" {
		t.Fatalf("%+v", r)
	}

	f = &fake.Forge{WaitBlock: true}
	start := time.Now()
	r = kit(t, t.TempDir(), f, map[string]string{"CI_WAIT_TIMEOUT": "1"}, "pr", "wait", "12")
	if r.code != 2 || r.errOut != "ci-wait: timed out after 1s waiting on PR #12\n" {
		t.Fatalf("timeout: %+v", r)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout did not stop the wait")
	}
}

func TestWaitUsage(t *testing.T) {
	for _, tc := range []struct {
		env  string
		args []string
	}{
		{"", nil},
		{"", []string{"abc"}},
		{"", []string{"-5"}},
		{"", []string{"0"}},
		{"", []string{"12", "13"}},
		{"", []string{"12", "--run", "3"}},
		{"", []string{"--run"}},
		{"", []string{"--run", "x"}},
		{"", []string{"12", "--watch"}},
		{"", []string{"12", "--repo", "/nonexistent/dir"}},
		{"x", []string{"12"}},
		{"0", []string{"12"}},
		{"-1", []string{"12"}},
	} {
		r := kit(t, t.TempDir(), &fake.Forge{Wait: forge.WaitResult{Green: true}},
			map[string]string{"CI_WAIT_TIMEOUT": tc.env}, append([]string{"pr", "wait"}, tc.args...)...)
		if r.code != 2 || !strings.HasPrefix(r.errOut, "ci-wait: ") {
			t.Errorf("pr wait %v (CI_WAIT_TIMEOUT=%q): %+v", tc.args, tc.env, r)
		}
	}
}

func logLines(job string, n int) []forge.LogLine {
	var out []forge.LogLine
	for i := 1; i <= n; i++ {
		out = append(out, forge.LogLine{Job: job, Text: fmt.Sprintf("%s\tstep\tline %d", job, i)})
	}
	return out
}

func TestLogTail(t *testing.T) {
	// One job: the last 40 lines.
	got := logTail(logLines("job", 500))
	if len(got) != 40 || got[0] != "job\tstep\tline 461" || got[39] != "job\tstep\tline 500" {
		t.Fatalf("cap: %d lines, %q .. %q", len(got), got[0], got[len(got)-1])
	}
	// Two jobs share the 40.
	got = logTail(append(logLines("build", 100), logLines("gate", 100)...))
	if len(got) != 40 || got[19] != "build\tstep\tline 100" || got[20] != "gate\tstep\tline 81" {
		t.Fatalf("shared: %q", got)
	}
	// A job's share ends at its last error line.
	logs := []forge.LogLine{{Job: "b", Text: "b\ts\tcompiling"}, {Job: "b", Text: "b\ts\t##[error]boom"}}
	for i := 0; i < 60; i++ {
		logs = append(logs, forge.LogLine{Job: "b", Text: "b\ts\tcleanup"})
	}
	got = logTail(logs)
	if len(got) != 2 || got[1] != "b\ts\t##[error]boom" {
		t.Fatalf("error line: %q", got)
	}
	// More jobs than lines: one each, capped at 40.
	logs = nil
	for i := 0; i < 50; i++ {
		logs = append(logs, logLines(fmt.Sprintf("j%d", i), 3)...)
	}
	if got = logTail(logs); len(got) != 40 {
		t.Fatalf("many jobs: %d lines", len(got))
	}
	if logTail(nil) != nil {
		t.Fatal("empty log")
	}
}
