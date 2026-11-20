//go:build !windows

package gitlab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/forge"
)

var ctx = context.Background()

// stub puts a fake `glab` on PATH. It appends each call's arguments to a log,
// joined by "|", then runs body, a shell case statement over "$*". $N in the
// body is the number of this call, starting at 1.
func stub(t *testing.T, body string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n(IFS='|'; printf '%s\\n' \"$*\") >>'" + log + "'\nN=$(wc -l <'" + log + "' | tr -d ' ')\ncase \"$*\" in\n" + body +
		"\n*) echo \"stub: unexpected: $*\" >&2; exit 99 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func client() *Client {
	c := New("")
	c.Poll = 10 * time.Millisecond
	return c
}

const mrBody = `{"iid":5,"web_url":"https://gitlab.com/o/r/-/merge_requests/5","title":"Add parser","state":"opened","draft":true,"source_branch":"task/07-foo","target_branch":"main","sha":"abc123","has_conflicts":true,"merge_status":"cannot_be_merged","head_pipeline":{"id":77,"status":"failed","web_url":"https://gitlab.com/o/r/-/pipelines/77"}}`

// The jobs API lists the newest job first.
const jobsBody = `[{"id":4,"name":"deploy","status":"manual","allow_failure":true,"web_url":"https://gitlab.com/o/r/-/jobs/4"},{"id":3,"name":"flaky","status":"failed","allow_failure":true,"web_url":"https://gitlab.com/o/r/-/jobs/3"},{"id":2,"name":"test","status":"failed","web_url":"https://gitlab.com/o/r/-/jobs/2"},{"id":1,"name":"build","status":"success","web_url":"https://gitlab.com/o/r/-/jobs/1"}]`

func TestViewPR(t *testing.T) {
	calls := stub(t, `"mr view 5 "*) echo '`+mrBody+`' ;;
"api projects/:id/pipelines/77/jobs"*) echo '`+jobsBody+`' ;;`)
	pr, err := client().ViewPR(ctx, "5")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.PullRequest{
		Number: 5, URL: "https://gitlab.com/o/r/-/merge_requests/5", Title: "Add parser",
		State: forge.StateOpen, Draft: true, Head: "task/07-foo", HeadSHA: "abc123",
		Base: "main", Mergeable: forge.MergeableNo,
		Checks: []forge.Check{
			{Name: "build", State: forge.CheckPass, URL: "https://gitlab.com/o/r/-/jobs/1"},
			{Name: "test", State: forge.CheckFail, URL: "https://gitlab.com/o/r/-/jobs/2"},
			{Name: "flaky", State: forge.CheckSkipped, URL: "https://gitlab.com/o/r/-/jobs/3"},
			{Name: "deploy", State: forge.CheckSkipped, URL: "https://gitlab.com/o/r/-/jobs/4"},
		},
	}
	if !reflect.DeepEqual(pr, want) {
		t.Fatalf("got  %+v\nwant %+v", pr, want)
	}
	if forge.Rollup(pr.Checks) != forge.CIRed {
		t.Fatalf("rollup = %s", forge.Rollup(pr.Checks))
	}
	wantCalls := []string{"mr|view|5|--output|json", "api|projects/:id/pipelines/77/jobs?per_page=100"}
	if got := calls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %q", got)
	}
}

// The pipeline's own status is the authority: the job list can miss the job
// that decided it, and a required manual job blocks the pipeline.
func TestViewPRAgreesWithThePipelineStatus(t *testing.T) {
	view := func(status, jobs string) forge.PullRequest {
		t.Helper()
		stub(t, `"mr view 5 "*) echo '{"iid":5,"head_pipeline":{"id":77,"status":"`+status+`","web_url":"https://p/77"}}' ;;
"api projects/:id/pipelines/77/jobs"*) echo '`+jobs+`' ;;`)
		pr, err := client().ViewPR(ctx, "5")
		if err != nil {
			t.Fatal(err)
		}
		return pr
	}
	pass := `[{"id":1,"name":"build","status":"success"}]`
	// A failed pipeline whose listed jobs all passed (the failed job is on
	// a later page, or is a trigger job) is still red.
	pr := view("failed", pass)
	if forge.Rollup(pr.Checks) != forge.CIRed || pr.Checks[1] != (forge.Check{Name: "pipeline", State: forge.CheckFail, URL: "https://p/77"}) {
		t.Fatalf("failed pipeline, passing jobs: %+v", pr.Checks)
	}
	if pr = view("running", pass); forge.Rollup(pr.Checks) != forge.CIRunning {
		t.Fatalf("running pipeline, passing jobs: %+v", pr.Checks)
	}
	if pr = view("success", pass); forge.Rollup(pr.Checks) != forge.CIGreen || len(pr.Checks) != 1 {
		t.Fatalf("green pipeline: %+v", pr.Checks)
	}
	// A manual job that may not fail blocks the pipeline: running, not green.
	pr = view("manual", `[{"id":2,"name":"deploy","status":"manual"},{"id":1,"name":"build","status":"success"}]`)
	if forge.Rollup(pr.Checks) != forge.CIRunning || pr.Checks[1].State != forge.CheckPending || len(pr.Checks) != 2 {
		t.Fatalf("blocking manual job: %+v", pr.Checks)
	}
}

// A trigger-only pipeline has no listed jobs: every reading of it is green.
func TestASuccessfulPipelineWithNoListedJobsIsGreen(t *testing.T) {
	stub(t, `"mr list "*) echo '[{"iid":5,"state":"opened"}]' ;;
"mr view 5 "*) echo '{"iid":5,"head_pipeline":{"id":77,"status":"success","web_url":"https://p/77"}}' ;;
"api projects/:id/pipelines/77/jobs"*) echo '[]' ;;`)
	want := []forge.Check{{Name: "pipeline", State: forge.CheckPass, URL: "https://p/77"}}
	pr, err := client().ViewPR(ctx, "5")
	if err != nil || forge.Rollup(pr.Checks) != forge.CIGreen || !reflect.DeepEqual(pr.Checks, want) {
		t.Fatalf("ViewPR checks = %+v, %v; want %+v", pr.Checks, err, want)
	}
	checks, err := client().Checks(ctx, 5)
	if err != nil || forge.Rollup(checks) != forge.CIGreen {
		t.Fatalf("Checks = %+v, %v; want green", checks, err)
	}
	prs, err := client().ListPRs(ctx, forge.ListOptions{})
	if err != nil || len(prs) != 1 || forge.Rollup(prs[0].Checks) != forge.CIGreen {
		t.Fatalf("ListPRs = %+v, %v; want one green PR", prs, err)
	}
	res, err := client().WaitChecks(ctx, forge.WaitTarget{PR: 5})
	if err != nil || !res.Green {
		t.Fatalf("WaitChecks = %+v, %v; want green", res, err)
	}
}

func TestListPRsReportsAFailedView(t *testing.T) {
	stub(t, `"mr list "*) echo '[{"iid":5,"state":"opened"}]' ;;
"mr view 5 "*) echo "502 Bad Gateway" >&2; exit 1 ;;`)
	if prs, err := client().ListPRs(ctx, forge.ListOptions{}); err == nil || !strings.Contains(err.Error(), "!5") {
		t.Fatalf("ListPRs = %+v, %v; want an error naming !5", prs, err)
	}
}

func TestViewPRMergedNoPipelineAndErrors(t *testing.T) {
	stub(t, `"mr view 6 "*) echo '{"iid":6,"state":"merged","merge_commit_sha":"feed12","merge_status":"can_be_merged"}' ;;
"mr view 9 "*) echo 'no open merge request available for "x"' >&2; exit 1 ;;
"mr view 8 "*) echo 'GET https://gitlab.com/api: 500' >&2; exit 1 ;;`)
	pr, err := client().ViewPR(ctx, "6")
	if err != nil || pr.State != forge.StateMerged || pr.MergeSHA != "feed12" || len(pr.Checks) != 0 || pr.Mergeable != forge.MergeableYes {
		t.Fatalf("merged: %+v, %v", pr, err)
	}
	if _, err := client().ViewPR(ctx, "9"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := client().ViewPR(ctx, "8"); err == nil || !strings.HasPrefix(err.Error(), "glab failed: ") {
		t.Fatalf("err = %v", err)
	}
	if _, err := client().ViewPR(ctx, "--web"); err == nil {
		t.Fatal("a reference that looks like a flag was accepted")
	}
}

func TestListPRs(t *testing.T) {
	calls := stub(t, `"api user") echo '{"username":"andrew"}' ;;
"mr list "*) echo '[{"iid":5,"state":"opened","source_branch":"task/07-foo","title":"Add parser"}]' ;;
"mr view 5 "*) echo '`+mrBody+`' ;;`)
	prs, err := client().ListPRs(ctx, forge.ListOptions{Mine: true, Head: "task/07-foo", Limit: 15})
	if err != nil || len(prs) != 1 {
		t.Fatalf("ListPRs = %+v, %v", prs, err)
	}
	want := []forge.Check{{Name: "pipeline", State: forge.CheckFail, URL: "https://gitlab.com/o/r/-/pipelines/77"}}
	if prs[0].Number != 5 || prs[0].Mergeable != forge.MergeableNo || !reflect.DeepEqual(prs[0].Checks, want) {
		t.Fatalf("pr = %+v", prs[0])
	}
	if got := calls(); got[1] != "mr|list|--output|json|--per-page|15|--source-branch|task/07-foo|--author|andrew" {
		t.Fatalf("list call = %q", got[1])
	}

	calls = stub(t, `"mr list "*) echo '[{"iid":6,"state":"merged","merge_commit_sha":"feed12"}]' ;;`)
	prs, err = client().ListPRs(ctx, forge.ListOptions{State: forge.StateMerged})
	if err != nil || len(prs) != 1 || prs[0].State != forge.StateMerged || prs[0].MergeSHA != "feed12" {
		t.Fatalf("merged list = %+v, %v", prs, err)
	}
	if got := calls(); len(got) != 1 || got[0] != "mr|list|--output|json|--per-page|100|--merged" {
		t.Fatalf("calls = %q", got)
	}
}

func TestCreatePRNeverPushes(t *testing.T) {
	calls := stub(t, `"mr create "*) echo "https://gitlab.com/o/r/-/merge_requests/5" ;;
"mr view task/07-foo "*) echo '{"iid":5,"state":"opened","source_branch":"task/07-foo"}' ;;`)
	if _, err := client().CreatePR(ctx, forge.CreatePRInput{Title: "x"}); err == nil {
		t.Fatal("CreatePR without a source branch succeeded")
	}
	if got := calls(); got[0] != "" {
		t.Fatalf("glab ran without a source branch: %q", got)
	}
	pr, err := client().CreatePR(ctx, forge.CreatePRInput{Head: "task/07-foo", Base: "main", Title: "Add; $(x)", Body: "body", Draft: true})
	if err != nil || pr.Number != 5 {
		t.Fatalf("CreatePR = %+v, %v", pr, err)
	}
	want := "mr|create|--source-branch|task/07-foo|--title|Add; $(x)|--description|body|--yes|--push=false|--target-branch|main|--draft"
	if got := calls(); got[0] != want {
		t.Fatalf("call = %q\nwant   %q", got[0], want)
	}
}

func TestWaitChecksPollsUntilThePipelineFinishes(t *testing.T) {
	// Running on the first two looks, green on the third.
	calls := stub(t, `"mr view 5 "*) if [ "$N" -lt 3 ]; then s=running; else s=success; fi
  echo '{"iid":5,"head_pipeline":{"id":77,"status":"'$s'"}}' ;;`)
	res, err := client().WaitChecks(ctx, forge.WaitTarget{PR: 5})
	if err != nil || !res.Green {
		t.Fatalf("green: %+v, %v", res, err)
	}
	if got := calls(); len(got) != 3 {
		t.Fatalf("looked %d times, want 3", len(got))
	}
}

func TestWaitChecksRed(t *testing.T) {
	stub(t, `"mr view 5 "*) echo '`+mrBody+`' ;;
"api projects/:id/pipelines/77/jobs"*) echo '`+jobsBody+`' ;;
"api projects/:id/pipelines/77") echo '{"id":77,"status":"canceled"}' ;;`)
	res, err := client().WaitChecks(ctx, forge.WaitTarget{PR: 5})
	want := []forge.Check{{Name: "test", State: forge.CheckFail, URL: "https://gitlab.com/o/r/-/jobs/2"}}
	if err != nil || res.Green || res.Conclusion != "" || !reflect.DeepEqual(res.Failed, want) {
		t.Fatalf("red MR: %+v, %v", res, err)
	}
	res, err = client().WaitChecks(ctx, forge.WaitTarget{Run: 77})
	if err != nil || res.Green || res.Conclusion != "canceled" || !reflect.DeepEqual(res.Failed, want) {
		t.Fatalf("red pipeline: %+v, %v", res, err)
	}
}

func TestWaitChecksErrors(t *testing.T) {
	stub(t, `"mr view 5 "*) echo '{"iid":5}' ;;
"mr view 6 "*) echo '{"iid":6,"head_pipeline":{"id":78,"status":"manual"}}' ;;
"mr view 7 "*) echo '{"iid":7,"head_pipeline":{"id":79,"status":"running"}}' ;;
"mr view 9 "*) echo "404 Not Found" >&2; exit 1 ;;`)
	for pr, want := range map[int]string{5: "no pipeline reported", 6: "is manual", 9: "no merge request !9"} {
		_, err := client().WaitChecks(ctx, forge.WaitTarget{PR: pr})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), "glab failed: ") {
			t.Errorf("MR %d: err = %v, want %q", pr, err, want)
		}
	}
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := client().WaitChecks(tctx, forge.WaitTarget{PR: 7}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the deadline did not stop the wait")
	}
}

func TestFailedLogs(t *testing.T) {
	calls := stub(t, `"api projects/:id/jobs/2/trace") printf 'one\ntwo\n' ;;
"api projects/:id/jobs/3/trace") exit 1 ;;`)
	failed := []forge.Check{
		{Name: "test", URL: "https://gitlab.com/o/r/-/jobs/2"},
		{Name: "gone", URL: "https://gitlab.com/o/r/-/jobs/3"},
		{Name: "pipeline", URL: "https://gitlab.com/o/r/-/pipelines/77"},
	}
	got, err := client().FailedLogs(ctx, forge.WaitTarget{PR: 5}, failed)
	want := []forge.LogLine{{Job: "test", Text: "test\t-\tone"}, {Job: "test", Text: "test\t-\ttwo"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("logs = %+v, %v", got, err)
	}
	if n := calls(); len(n) != 2 {
		t.Fatalf("calls = %q", n)
	}
	if _, err := client().FailedLogs(ctx, forge.WaitTarget{PR: 5}, failed[2:]); !errors.Is(err, forge.ErrNoLogs) {
		t.Fatalf("no job links: err = %v, want ErrNoLogs", err)
	}
}
