package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/tracker/trackertest"
)

const fakeEnv = "RIG_EXEC_TEST_FAKEPROG"

// TestMain turns the test binary into the user program when fakeEnv is set:
// args are <state dir> <mode>. That keeps the tests free of /bin/sh.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		fakeProgram(os.Args[1], os.Args[2])
		return
	}
	os.Exit(m.Run())
}

func fakeProgram(dir, mode string) {
	line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
	f, _ := os.OpenFile(filepath.Join(dir, "requests.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(line)
	f.Close()
	fmt.Fprintln(os.Stderr, "first stderr line")
	fmt.Fprintln(os.Stderr, "fake stderr tail")

	switch {
	case mode == "exit1":
		os.Exit(1)
	case mode == "badjson":
		fmt.Print("not json")
		return
	case mode == "two":
		fmt.Print(`{"v":1,"ok":true,"caps":{}}{"v":1,"ok":true,"caps":{}}`)
		return
	case mode == "trailing":
		fmt.Print(`{"v":1,"ok":true,"caps":{}} junk`)
		return
	case mode == "wrongv":
		fmt.Print(`{"v":2,"ok":true,"caps":{}}`)
		return
	case mode == "nopayload":
		fmt.Print(`{"v":1,"ok":true}`)
		return
	case mode == "oversize":
		fmt.Printf(`{"v":1,"ok":true,"caps":{},"pad":"%s"}`, strings.Repeat("x", MaxOutput))
		return
	case mode == "sleep":
		time.Sleep(time.Minute)
		return
	case mode == "env":
		fmt.Printf(`{"v":1,"ok":true,"caps":{"levels":[%q]}}`, "tok="+os.Getenv("RIG_JIRA_TOKEN"))
		return
	case strings.HasPrefix(mode, "err:"):
		code := strings.TrimPrefix(mode, "err:")
		if code == "none" {
			code = ""
		}
		out, _ := json.Marshal(map[string]any{"v": 1, "ok": false, "error": "boom", "code": code})
		fmt.Print(string(out))
		return
	}

	var req struct {
		Op      string `json:"op"`
		Key     string `json:"key"`
		Status  string `json:"status"`
		Query   struct{ Labels []string }
		Comment struct{ Body string }
		Issue   struct {
			Type, Title, Body string
			Acceptance        []string
			Labels            []string
			ParentKey         string
		}
	}
	json.Unmarshal(line, &req)
	reply := func(k string, v any) {
		out, _ := json.Marshal(map[string]any{"v": 1, "ok": true, k: v})
		fmt.Print(string(out))
	}
	fail := func(code string) {
		out, _ := json.Marshal(map[string]any{"v": 1, "ok": false, "error": "failed", "code": code})
		fmt.Print(string(out))
	}
	type issue = map[string]any
	all := map[string]issue{
		"P-1": {"key": "P-1", "labels": []string{"l"}, "parent": nil},
		"C-1": {"key": "C-1", "labels": []string{"l"}, "parent": map[string]any{"key": "P-1"}},
		"O-1": {"key": "O-1", "labels": []string{}, "parent": nil},
	}
	order := []string{"P-1", "C-1", "O-1"}
	createdFile := filepath.Join(dir, "created.json")
	var created []map[string]any
	if b, err := os.ReadFile(createdFile); err == nil {
		json.Unmarshal(b, &created)
	}
	for _, c := range created {
		k := c["key"].(string)
		ls := []string{}
		for _, l := range c["labels"].([]any) {
			ls = append(ls, l.(string))
		}
		c["labels"] = ls
		all[k] = c
		order = append(order, k)
	}
	statusFile := filepath.Join(dir, "status-"+req.Key)
	status := func() string {
		b, err := os.ReadFile(statusFile)
		if err != nil {
			return "To Do"
		}
		return string(b)
	}
	on := mode == "caps-all" || mode == "create-unsupported" || mode == "create-nokey" || mode == "create-badkey" || mode == "types-empty"
	switch req.Op {
	case "caps":
		reply("caps", map[string]any{"parent": on, "children": on, "types": on, "search": on, "comment": on, "setStatus": on, "create": on, "levels": []string{}})
	case "create":
		if mode == "create-unsupported" {
			fail("unsupported")
			return
		}
		if mode == "create-nokey" {
			reply("issue", issue{"title": req.Issue.Title})
			return
		}
		if mode == "create-badkey" {
			reply("issue", issue{"key": "a b\n", "title": req.Issue.Title})
			return
		}
		key := fmt.Sprintf("N-%d", len(created)+1)
		is := issue{"key": key, "url": "https://x/" + key, "title": req.Issue.Title, "type": req.Issue.Type,
			"description": req.Issue.Body, "acceptance": req.Issue.Acceptance, "labels": req.Issue.Labels, "rev": "r-new"}
		if req.Issue.ParentKey != "" {
			is["parent"] = map[string]any{"key": req.Issue.ParentKey}
		}
		created = append(created, is)
		b, _ := json.Marshal(created)
		os.WriteFile(createdFile, b, 0o600)
		reply("issue", is)
	case "types":
		if mode == "types-off-nocode" {
			fmt.Print(`{"v":1,"ok":false,"error":"unknown op"}`)
			return
		}
		if mode == "types-off-exit" {
			os.Exit(3)
		}
		if mode == "create-unsupported" || mode == "caps-off" {
			fail("unsupported")
			return
		}
		if mode == "types-empty" {
			reply("types", []issue{})
			return
		}
		reply("types", []issue{{"name": "Epic", "level": 2}, {"name": "Task", "level": 0}, {"name": "Bug"}})
	case "issue":
		is, ok := all[req.Key]
		if !ok {
			fail("not-found")
			return
		}
		st := status()
		is["status"] = st
		is["rev"] = "r-" + st
		reply("issue", is)
	case "search":
		out := []issue{}
		for _, k := range order {
			if len(req.Query.Labels) == 0 || has(all[k]["labels"].([]string), req.Query.Labels[0]) {
				out = append(out, issue{"key": k})
			}
		}
		if q := struct{ Query struct{ Limit int } }{}; json.Unmarshal(line, &q) == nil && q.Query.Limit > 0 && len(out) > q.Query.Limit {
			out = out[:q.Query.Limit]
		}
		reply("issues", out)
	case "children":
		if _, ok := all[req.Key]; !ok {
			fail("not-found")
			return
		}
		out := []issue{}
		if req.Key == "P-1" {
			out = append(out, issue{"key": "C-1"})
		}
		reply("issues", out)
	case "comment":
		if _, ok := all[req.Key]; !ok {
			fail("not-found")
			return
		}
		reply("comment", map[string]any{"id": "c1", "url": "u"})
	case "status":
		if _, ok := all[req.Key]; !ok {
			fail("not-found")
			return
		}
		if req.Status != "To Do" && req.Status != "Done" {
			fail("no-transition")
			return
		}
		from := status()
		os.WriteFile(statusFile, []byte(req.Status), 0o600)
		reply("status", map[string]any{"from": from, "to": req.Status, "changed": from != req.Status})
	}
}

func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// newFake returns a Tracker running the test binary as the program, and the
// state dir that records each request line.
func newFake(t *testing.T, mode string, opts Options) (*Tracker, string) {
	t.Helper()
	t.Setenv(fakeEnv, "1")
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := New([]string{exe, dir, mode}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return tr, dir
}

func requests(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "requests.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestNewRefusesEmptyArgv(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Error("New(nil) succeeded")
	}
}

func TestContract(t *testing.T) {
	for _, mode := range []string{"caps-all", "caps-off"} {
		t.Run(mode, func(t *testing.T) {
			trackertest.Run(t, func(t *testing.T) (tracker.Tracker, trackertest.Seed) {
				tr, _ := newFake(t, mode, Options{})
				return tr, trackertest.Seed{
					Key: "O-1", Status: "To Do", NextStatus: "Done",
					ParentKey: "P-1", ChildKey: "C-1", Label: "l", MissingKey: "X-9",
				}
			})
		})
	}
}

func TestErrorCodes(t *testing.T) {
	cases := map[string]error{
		"not-found":     tracker.ErrNotFound,
		"unsupported":   tracker.ErrUnsupported,
		"auth":          tracker.ErrAuth,
		"no-transition": tracker.ErrNoTransition,
	}
	for code, want := range cases {
		tr, _ := newFake(t, "err:"+code, Options{})
		_, err := tr.Issue(context.Background(), "A-1")
		if !errors.Is(err, want) {
			t.Errorf("code %s: %v, want %v", code, err, want)
		}
	}
	tr, _ := newFake(t, "err:none", Options{})
	_, err := tr.Issue(context.Background(), "A-1")
	for _, s := range []error{tracker.ErrNotFound, tracker.ErrUnsupported, tracker.ErrAuth, tracker.ErrNoTransition} {
		if err == nil || errors.Is(err, s) {
			t.Errorf("no code: %v, want plain error", err)
		}
	}
	tr, _ = newFake(t, "err:weird", Options{})
	_, err = tr.Issue(context.Background(), "A-1")
	if err == nil || !strings.Contains(err.Error(), "weird") {
		t.Errorf("unknown code: %v, want an error naming it", err)
	}
	// Capabilities reports auth failure as ErrAuth.
	tr, _ = newFake(t, "err:auth", Options{})
	if _, err := tr.Capabilities(context.Background()); !errors.Is(err, tracker.ErrAuth) {
		t.Errorf("Capabilities = %v, want ErrAuth", err)
	}
}

func TestBadRunsAreErrorsWithStderrTail(t *testing.T) {
	for _, mode := range []string{"exit1", "badjson", "two", "trailing", "wrongv", "nopayload", "oversize", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			tr, _ := newFake(t, mode, Options{Timeout: 3 * time.Second})
			if mode == "sleep" {
				tr, _ = newFake(t, mode, Options{Timeout: 500 * time.Millisecond})
			}
			_, err := tr.Capabilities(context.Background())
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), "fake stderr tail") {
				t.Errorf("error lacks stderr tail: %v", err)
			}
			if mode == "oversize" && !strings.Contains(err.Error(), "limit") {
				t.Errorf("oversize error does not say so: %v", err)
			}
		})
	}
}

func TestProgramDoesNotSeeTokens(t *testing.T) {
	t.Setenv("RIG_JIRA_TOKEN", "s3cret")
	tr, _ := newFake(t, "env", Options{})
	c, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Levels) != 1 || c.Levels[0] != "tok=" {
		t.Errorf("program saw the token: %v", c.Levels)
	}
}

func TestMalformedKeyNeverStartsProgram(t *testing.T) {
	tr, dir := newFake(t, "caps-all", Options{})
	ctx := context.Background()
	tr.Issue(ctx, "a b")
	tr.Children(ctx, "a/b")
	tr.Comment(ctx, "", tracker.Comment{Body: "x"})
	tr.SetStatus(ctx, "a#b", "Done")
	if got := requests(t, dir); len(got) != 0 {
		t.Errorf("program ran for malformed keys: %v", got)
	}
}

func TestRequestsAreExactlyTheOperationData(t *testing.T) {
	tr, dir := newFake(t, "caps-all", Options{})
	ctx := context.Background()
	tr.Capabilities(ctx)
	tr.Capabilities(ctx) // cached: no second caps run
	tr.Issue(ctx, "O-1")
	tr.Search(ctx, tracker.Query{Text: "q", Labels: []string{"l"}, Limit: 3})
	tr.Children(ctx, "P-1")
	tr.Comment(ctx, "O-1", tracker.Comment{Body: "hi"})
	tr.SetStatus(ctx, "O-1", "Done")
	want := []string{
		`{"v":1,"op":"caps"}`,
		`{"v":1,"op":"issue","key":"O-1"}`,
		`{"v":1,"op":"search","query":{"text":"q","labels":["l"],"limit":3}}`,
		`{"v":1,"op":"children","key":"P-1"}`,
		`{"v":1,"op":"comment","key":"O-1","comment":{"body":"hi"}}`,
		`{"v":1,"op":"status","key":"O-1","status":"Done"}`,
	}
	got := requests(t, dir)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUnsupportedDoesNotStartProgram(t *testing.T) {
	tr, dir := newFake(t, "caps-off", Options{})
	ctx := context.Background()
	if _, err := tr.Search(ctx, tracker.Query{}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Fatalf("Search = %v", err)
	}
	tr.Comment(ctx, "O-1", tracker.Comment{Body: "x"})
	tr.SetStatus(ctx, "O-1", "Done")
	if got := requests(t, dir); len(got) != 1 {
		t.Errorf("want only the caps run, got %v", got)
	}
}

func TestCreateSendsOpAndDecodesIssue(t *testing.T) {
	tr, dir := newFake(t, "caps-all", Options{})
	ctx := context.Background()
	is, err := tr.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T", Body: "B", Acceptance: []string{"a"}, Labels: []string{"l"}, ParentKey: "P-1"})
	if err != nil {
		t.Fatal(err)
	}
	if is.Key != "N-1" || is.URL == "" || is.Rev == "" {
		t.Errorf("issue = %+v", is)
	}
	want := `{"v":1,"op":"create","issue":{"type":"Task","title":"T","body":"B","acceptance":["a"],"labels":["l"],"parentKey":"P-1"}}`
	got := requests(t, dir)
	if len(got) != 2 || got[1] != want {
		t.Errorf("requests = %v, want create line %s", got, want)
	}
}

func TestCreateWithoutKeyIsAnError(t *testing.T) {
	tr, _ := newFake(t, "create-nokey", Options{})
	_, err := tr.Create(context.Background(), tracker.NewIssue{Type: "Task", Title: "T"})
	if err == nil || errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("Create with a keyless response = %v, want a non-unsupported error", err)
	}
}

func TestIssueTypesEmptyListIsUnsupported(t *testing.T) {
	tr, _ := newFake(t, "types-empty", Options{})
	if _, err := tr.IssueTypes(context.Background()); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("IssueTypes = %v, want ErrUnsupported", err)
	}
}

func TestCreateRefusesBadParentKeyBeforeProgram(t *testing.T) {
	tr, dir := newFake(t, "caps-all", Options{})
	if _, err := tr.Create(context.Background(), tracker.NewIssue{Type: "Task", Title: "T", ParentKey: "a b"}); err == nil {
		t.Error("want error")
	}
	if got := requests(t, dir); len(got) != 0 {
		t.Errorf("program ran: %v", got)
	}
}

func TestIssueTypesKeepsOmittedLevelNil(t *testing.T) {
	tr, dir := newFake(t, "caps-all", Options{})
	types, err := tr.IssueTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 3 || types[0].Level == nil || *types[0].Level != 2 || types[1].Level == nil || *types[1].Level != 0 || types[2].Level != nil {
		t.Errorf("types = %+v", types)
	}
	if got := requests(t, dir); len(got) != 1 || got[0] != `{"v":1,"op":"types"}` {
		t.Errorf("requests = %v", got)
	}
}

func TestUnsupportedAnswersMapToErrUnsupported(t *testing.T) {
	tr, _ := newFake(t, "create-unsupported", Options{})
	ctx := context.Background()
	if _, err := tr.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T"}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("Create = %v", err)
	}
	if _, err := tr.IssueTypes(ctx); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("IssueTypes = %v", err)
	}
}

func TestCapsWithoutCreateLeaveItFalseAndCreateUnsupported(t *testing.T) {
	tr, dir := newFake(t, "caps-off", Options{})
	ctx := context.Background()
	c, err := tr.Capabilities(ctx)
	if err != nil || c.Create {
		t.Fatalf("caps = %+v, %v", c, err)
	}
	if _, err := tr.Create(ctx, tracker.NewIssue{Type: "Task", Title: "T"}); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("Create = %v", err)
	}
	if got := requests(t, dir); len(got) != 1 {
		t.Errorf("want only caps run, got %v", got)
	}
}

func TestIssueTypesGatedOnCaps(t *testing.T) {
	for _, mode := range []string{"types-off-nocode", "types-off-exit"} {
		tr, _ := newFake(t, mode, Options{})
		if _, err := tr.IssueTypes(context.Background()); !errors.Is(err, tracker.ErrUnsupported) {
			t.Errorf("%s: IssueTypes = %v, want ErrUnsupported", mode, err)
		}
	}
}

func TestCreateRefusesInvalidResponseKey(t *testing.T) {
	tr, _ := newFake(t, "create-badkey", Options{})
	_, err := tr.Create(context.Background(), tracker.NewIssue{Type: "Task", Title: "T"})
	if err == nil || !strings.Contains(err.Error(), "may have been created") {
		t.Errorf("Create = %v, want a may-have-been-created error", err)
	}
}
