//go:build !windows

package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/workspace/cli/internal/forge"
)

var ctx = context.Background()

// stub puts a fake `gh` on PATH. It appends each call's arguments to a log,
// one call per line with arguments joined by "|", then runs body, a shell
// case statement over "$*".
func stub(t *testing.T, body string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n(IFS='|'; printf '%s\\n' \"$*\") >>'" + log + "'\ncase \"$*\" in\n" + body +
		"\n*) echo \"stub: unexpected: $*\" >&2; exit 99 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

const prBody = `{"number":12,"url":"https://github.com/o/r/pull/12","title":"Add parser",
"state":"OPEN","isDraft":true,"headRefName":"task/07-foo","headRefOid":"abc123","baseRefName":"main",
"mergeable":"CONFLICTING","statusCheckRollup":[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://x/1"},
 {"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","conclusion":""},
 {"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"CANCELLED"},
 {"__typename":"CheckRun","name":"docs","status":"COMPLETED","conclusion":"SKIPPED"},
 {"__typename":"StatusContext","context":"ci/ext","state":"FAILURE","targetUrl":"https://x/2"},
 {"__typename":"StatusContext","context":"ci/wait","state":"PENDING"}]}`

func TestViewPR(t *testing.T) {
	calls := stub(t, `"pr view "*) cat <<'J'
`+prBody+`
J
;;`)
	pr, err := New("").ViewPR(ctx, "12")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.PullRequest{
		Number: 12, URL: "https://github.com/o/r/pull/12", Title: "Add parser",
		State: forge.StateOpen, Draft: true, Head: "task/07-foo", HeadSHA: "abc123",
		Base: "main", Mergeable: forge.MergeableNo,
		Checks: []forge.Check{
			{Name: "build", State: forge.CheckPass, URL: "https://x/1"},
			{Name: "test", State: forge.CheckPending},
			{Name: "lint", State: forge.CheckCancel},
			{Name: "docs", State: forge.CheckSkipped},
			{Name: "ci/ext", State: forge.CheckFail, URL: "https://x/2"},
			{Name: "ci/wait", State: forge.CheckPending},
		},
	}
	if !reflect.DeepEqual(pr, want) {
		t.Fatalf("got  %+v\nwant %+v", pr, want)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{"pr|view|12|--json|" + prFields}) {
		t.Fatalf("calls = %q", got)
	}
}

func TestViewPRNotFoundAndErrors(t *testing.T) {
	stub(t, `"pr view 9 "*) echo 'no pull requests found for branch "x"' >&2; exit 1 ;;
"pr view 8 "*) echo 'HTTP 502' >&2; exit 1 ;;`)
	if _, err := New("").ViewPR(ctx, "9"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := New("").ViewPR(ctx, "8"); err == nil || err.Error() != "gh failed: HTTP 502" {
		t.Fatalf("err = %v", err)
	}
	if _, err := New("").ViewPR(ctx, "--web"); err == nil {
		t.Fatal("a reference that looks like a flag was accepted")
	}
}

func TestListPRs(t *testing.T) {
	calls := stub(t, `"pr list "*) echo '[`+strings.ReplaceAll(prBody, "\n", " ")+`]' ;;`)
	prs, err := New("").ListPRs(ctx, forge.ListOptions{Mine: true, Head: "task/07-foo", Limit: 15})
	if err != nil || len(prs) != 1 || prs[0].Number != 12 {
		t.Fatalf("ListPRs = %+v, %v", prs, err)
	}
	want := "pr|list|--state|open|--limit|15|--head|task/07-foo|--author|@me|--json|" + prFields
	if got := calls(); got[0] != want {
		t.Fatalf("call = %q\nwant   %q", got[0], want)
	}
}

func TestCreatePRNeverRunsWithoutHead(t *testing.T) {
	calls := stub(t, `"pr create "*) echo "https://github.com/o/r/pull/12" ;;
"pr view "*) echo '`+strings.ReplaceAll(prBody, "\n", " ")+`' ;;`)
	if _, err := New("").CreatePR(ctx, forge.CreatePRInput{Title: "x"}); err == nil {
		t.Fatal("CreatePR without a head branch succeeded")
	}
	if got := calls(); got[0] != "" {
		t.Fatalf("gh ran without a head branch: %q", got)
	}
	pr, err := New("").CreatePR(ctx, forge.CreatePRInput{
		Head: "task/07-foo", Base: "main", Title: "Add; $(x)", Body: "b\nc", Draft: true})
	if err != nil || pr.Number != 12 {
		t.Fatalf("CreatePR = %+v, %v", pr, err)
	}
	got := calls()
	// The body's newline splits the logged call over two lines.
	if got[0] != "pr|create|--head|task/07-foo|--title|Add; $(x)|--body|b" || got[1] != "c|--base|main|--draft" {
		t.Fatalf("calls = %q", got)
	}
	if got[2] != "pr|view|https://github.com/o/r/pull/12|--json|"+prFields {
		t.Fatalf("view call = %q", got[2])
	}
}

func TestWaitChecksPR(t *testing.T) {
	calls := stub(t, `"pr checks 12 --watch --fail-fast") exit 0 ;;`)
	res, err := New("").WaitChecks(ctx, forge.WaitTarget{PR: 12})
	if err != nil || !res.Green {
		t.Fatalf("green: %+v, %v", res, err)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("calls = %q", got)
	}

	calls = stub(t, `"pr checks 12 --watch"*) exit 1 ;;
"pr checks 12 --json"*) printf 'build\thttps://github.com/o/r/actions/runs/7/job/1\nlint\thttps://ci.example/9\n' ;;`)
	res, err = New("").WaitChecks(ctx, forge.WaitTarget{PR: 12})
	want := []forge.Check{
		{Name: "build", State: forge.CheckFail, URL: "https://github.com/o/r/actions/runs/7/job/1"},
		{Name: "lint", State: forge.CheckFail, URL: "https://ci.example/9"},
	}
	if err != nil || res.Green || !reflect.DeepEqual(res.Failed, want) {
		t.Fatalf("red: %+v, %v", res, err)
	}
	if got := calls()[1]; got != "pr|checks|12|--json|name,bucket,link|--jq|"+failedJQ {
		t.Fatalf("query = %q", got)
	}
}

func TestWaitChecksErrors(t *testing.T) {
	stub(t, `"pr checks 12 --watch"*) echo "no checks reported on the 'x' branch" >&2; exit 1 ;;
"pr checks 12 --json"*) exit 0 ;;`)
	_, err := New("").WaitChecks(ctx, forge.WaitTarget{PR: 12})
	if err == nil || !strings.Contains(err.Error(), "gh failed: no checks reported") {
		t.Fatalf("no failed checks: err = %v", err)
	}

	stub(t, `"pr checks 12 --watch"*) exit 1 ;;
"pr checks 12 --json"*) echo "HTTP 500" >&2; exit 1 ;;`)
	if _, err := New("").WaitChecks(ctx, forge.WaitTarget{PR: 12}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("query error: err = %v", err)
	}

	stub(t, `"pr checks 12 --watch"*) sleep 30 ;;`)
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := New("").WaitChecks(tctx, forge.WaitTarget{PR: 12}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout waited for the watch")
	}
}

func TestWaitChecksRun(t *testing.T) {
	calls := stub(t, `"run watch 345 --exit-status") exit 1 ;;
"run view 345 --json conclusion"*) echo failure ;;
"run view 345 --json jobs"*) printf 'test (ubuntu)\ngate\n' ;;`)
	res, err := New("").WaitChecks(ctx, forge.WaitTarget{Run: 345})
	if err != nil || res.Green || res.Conclusion != "failure" || len(res.Failed) != 2 || res.Failed[0].Name != "test (ubuntu)" {
		t.Fatalf("red run: %+v, %v", res, err)
	}
	if got := calls(); got[1] != "run|view|345|--json|conclusion|--jq|.conclusion" || got[2] != "run|view|345|--json|jobs|--jq|"+failedJobsJQ {
		t.Fatalf("calls = %q", got)
	}

	stub(t, `"run watch 345 --exit-status") exit 1 ;;
"run view 345 --json conclusion"*) echo "" ;;`)
	if _, err := New("").WaitChecks(ctx, forge.WaitTarget{Run: 345}); err == nil {
		t.Fatal("a run with no failure conclusion was reported as red")
	}
}

func TestFailedLogs(t *testing.T) {
	calls := stub(t, `"api --allow-escape-sequences "*) exit 1 ;;
"api repos/{owner}/{repo}/actions/jobs/41/logs") printf 'one\ntwo\n' ;;
"run view 345 --log-failed") printf 'build\tstep\tboom\n' ;;
"run view 346 --log-failed") echo "run is still in progress" >&2; exit 1 ;;`)
	c := New("")
	failed := []forge.Check{
		{Name: "build (21)", URL: "https://github.com/o/r/actions/runs/7/job/41"},
		{Name: "external", URL: "https://ci.example/9"},
	}
	got, err := c.FailedLogs(ctx, forge.WaitTarget{PR: 12}, failed)
	want := []forge.LogLine{{Job: "build (21)", Text: "build (21)\t-\tone"}, {Job: "build (21)", Text: "build (21)\t-\ttwo"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("PR logs = %+v, %v", got, err)
	}
	if n := calls(); n[0] != "api|--allow-escape-sequences|repos/{owner}/{repo}/actions/jobs/41/logs" {
		t.Fatalf("first call = %q", n[0])
	}
	if _, err := c.FailedLogs(ctx, forge.WaitTarget{PR: 12}, failed[1:]); !errors.Is(err, forge.ErrNoLogs) {
		t.Fatalf("no job links: err = %v, want ErrNoLogs", err)
	}
	got, err = c.FailedLogs(ctx, forge.WaitTarget{Run: 345}, nil)
	if err != nil || len(got) != 1 || got[0].Job != "build" || got[0].Text != "build\tstep\tboom" {
		t.Fatalf("run logs = %+v, %v", got, err)
	}
	if got, err := c.FailedLogs(ctx, forge.WaitTarget{Run: 346}, nil); err != nil || len(got) != 0 {
		t.Fatalf("unavailable log = %+v, %v", got, err)
	}
}

func TestDirIsWhereGhRuns(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "cwd")
	stub(t, `"pr checks "*) pwd -P >'`+out+`' ;;`)
	if _, err := New(dir).WaitChecks(ctx, forge.WaitTarget{PR: 1}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	want, _ := filepath.EvalSymlinks(dir)
	if strings.TrimSpace(string(b)) != want {
		t.Fatalf("gh ran in %q, want %q", b, want)
	}
}
