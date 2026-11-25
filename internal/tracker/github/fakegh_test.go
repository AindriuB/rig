//go:build !windows

package github

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The fake gh is this test binary run again: the script on PATH execs it, and
// TestMain sees RIG_FAKE_GH_STATE and plays gh instead of running tests. State
// lives in a JSON file so it survives between gh calls.
//
// The gh calls it answers are the ones the adapter ASSUMES (recorded, not run
// against GitHub; see the package doc):
//
//	gh api user                                       -> {"login":...}
//	gh api users/OWNER                                -> {"type":"Organization"|"User"}
//	gh api -H GraphQL-Features:... -f query=... -f owner= -f name= -F number=N graphql
//	gh api --method GET -f q=... -F per_page=N search/issues
//	gh api --method POST  --input - repos/O/R/issues/N/comments
//	gh api --method PATCH --input - repos/O/R/issues/N
func TestMain(m *testing.M) {
	if p := os.Getenv("RIG_FAKE_GH_STATE"); p != "" {
		os.Exit(runFakeGH(p, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeIssue struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	State   string   `json:"state"` // OPEN or CLOSED
	Updated int      `json:"updated"`
	Labels  []string `json:"labels"`
	Type    string   `json:"type"`
	Parent  int      `json:"parent"`
	PR      bool     `json:"pr"`
	Repo    string   `json:"repo"` // the repository the item (or, as a sub-issue, the child) really belongs to; empty = this one
	// ParentRepo is the repository of Parent; empty = this one.
	ParentRepo string `json:"parentRepo"`
}

type fakeState struct {
	Owner    string               `json:"owner"`
	Name     string               `json:"name"`
	OwnerOrg bool                 `json:"ownerOrg"`
	AuthFail bool                 `json:"authFail"`
	Clock    int                  `json:"clock"`
	Comments int                  `json:"comments"`
	Issues   map[string]fakeIssue `json:"issues"`
	// Types are the organisation's issue types.
	Types []string `json:"types"`
	// FailSub makes the sub-issues call fail.
	FailSub bool `json:"failSub"`
}

// fakeGH installs the fake gh on PATH and returns the state file path and a
// function that reads the call log (one JSON array of argv per line).
func fakeGH(t *testing.T, st fakeState) (statePath string, calls func() [][]string) {
	t.Helper()
	dir := t.TempDir()
	statePath = filepath.Join(dir, "state.json")
	writeFakeState(t, statePath, st)
	script := "#!/bin/sh\nexec '" + os.Args[0] + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RIG_FAKE_GH_STATE", statePath)
	return statePath, func() [][]string {
		b, _ := os.ReadFile(statePath + ".log")
		var out [][]string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			var e struct {
				Argv  []string `json:"argv"`
				Stdin string   `json:"stdin"`
				Token string   `json:"token"`
			}
			if err := json.Unmarshal([]byte(l), &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, append(e.Argv, "STDIN="+e.Stdin, "TOKEN="+e.Token))
		}
		return out
	}
}

func writeFakeState(t testing.TB, path string, st fakeState) {
	b, _ := json.Marshal(st)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runFakeGH(statePath string, args []string) int {
	var st fakeState
	b, err := os.ReadFile(statePath)
	if err != nil || json.Unmarshal(b, &st) != nil {
		fmt.Fprintln(os.Stderr, "fake gh: no state")
		return 99
	}
	stdin := ""
	if hasArg(args, "--input") {
		raw, _ := io.ReadAll(os.Stdin)
		stdin = string(raw)
	}
	logLine, _ := json.Marshal(map[string]any{"argv": args, "stdin": stdin, "token": os.Getenv("RIG_JIRA_TOKEN")})
	if f, err := os.OpenFile(statePath+".log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Write(append(logLine, '\n'))
		f.Close()
	}
	out, code := fakeCall(&st, args, stdin)
	b, _ = json.Marshal(st)
	os.WriteFile(statePath, b, 0o644)
	return code(out)
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// fakeCall returns a function that prints the answer and gives the exit code.
func fakeCall(st *fakeState, args []string, stdin string) (string, func(string) int) {
	reply := func(s string) (string, func(string) int) {
		return s, func(o string) int { fmt.Print(o); return 0 }
	}
	fail := func(msg string) (string, func(string) int) {
		return "", func(string) int { fmt.Fprintln(os.Stderr, msg); return 1 }
	}
	if len(args) == 0 || args[0] != "api" {
		return fail("fake gh: unexpected: " + strings.Join(args, " "))
	}
	if st.AuthFail {
		return fail("gh: To get started with GitHub CLI, please run:  gh auth login")
	}
	// Flags and the one positional endpoint.
	fields := map[string]string{}
	method, endpoint := "GET", ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--hostname", "-H":
			i++
		case "--method", "-X":
			i++
			method = rest[i]
		case "-f", "-F", "--raw-field", "--field":
			i++
			k, v, _ := strings.Cut(rest[i], "=")
			fields[k] = v
		case "--input":
			i++
		default:
			if strings.HasPrefix(rest[i], "-") {
				return fail("fake gh: unexpected flag " + rest[i])
			}
			endpoint = rest[i]
		}
	}
	repo := "repos/" + st.Owner + "/" + st.Name
	switch {
	case endpoint == "user":
		return reply(`{"login":"me"}`)
	case endpoint == "users/"+st.Owner:
		typ := "User"
		if st.OwnerOrg {
			typ = "Organization"
		}
		return reply(`{"type":"` + typ + `"}`)
	case endpoint == "graphql":
		if fields["owner"] != st.Owner || fields["name"] != st.Name {
			return fail("gh: Could not resolve to a Repository")
		}
		n := fields["number"]
		is, ok := st.Issues[n]
		if !ok || is.PR {
			return fail("gh: Could not resolve to an Issue with the number of " + n + ".")
		}
		if strings.Contains(fields["query"], "subIssues") {
			var nodes []string
			for _, k := range sortedKeys(st.Issues) {
				c := st.Issues[k]
				if !c.PR && strconv.Itoa(c.Parent) == n {
					nodes = append(nodes, ref(st, k, c, c.Repo))
				}
			}
			return reply(`{"data":{"repository":{"issue":{"subIssues":{"nodes":[` + strings.Join(nodes, ",") + `]}}}}}`)
		}
		var labels []string
		for _, l := range is.Labels {
			labels = append(labels, `{"name":`+jstr(l)+`}`)
		}
		parent := "null"
		if is.Parent != 0 {
			p := strconv.Itoa(is.Parent)
			parent = ref(st, p, st.Issues[p], is.ParentRepo)
		}
		typ := "null"
		if is.Type != "" {
			typ = `{"name":` + jstr(is.Type) + `}`
		}
		return reply(fmt.Sprintf(`{"data":{"repository":{"issue":{"number":%s,"url":%s,"title":%s,"body":%s,"state":%s,"updatedAt":%s,"labels":{"nodes":[%s]},"assignees":{"nodes":[{"login":"ann"}]},"issueType":%s,"parent":%s}}}}`,
			n, jstr(issueURL(st, n)), jstr(is.Title), jstr(is.Body), jstr(is.State), jstr(fmt.Sprintf("2024-01-01T00:00:%02dZ", is.Updated)), strings.Join(labels, ","), typ, parent))
	case endpoint == "search/issues" && method == "GET":
		q := fields["q"]
		if !strings.Contains(q, "repo:"+st.Owner+"/"+st.Name+" is:issue") {
			return fail("fake gh: search not scoped: " + q)
		}
		var want []string
		for _, m := range regexp.MustCompile(`label:"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(q, -1) {
			want = append(want, strings.ReplaceAll(m[1], `\"`, `"`))
		}
		per, _ := strconv.Atoi(fields["per_page"])
		var items []string
		for _, k := range sortedKeys(st.Issues) {
			c := st.Issues[k]
			if c.PR || !hasAll(c.Labels, want) || len(items) >= per {
				continue
			}
			typ := "null"
			if c.Type != "" {
				typ = `{"name":` + jstr(c.Type) + `}`
			}
			repoPath := st.Owner + "/" + st.Name
			if c.Repo != "" {
				repoPath = c.Repo
			}
			items = append(items, fmt.Sprintf(`{"number":%s,"html_url":%s,"repository_url":%s,"title":%s,"type":%s}`, k, jstr("https://github.com/"+repoPath+"/issues/"+k), jstr("https://api.github.com/repos/"+repoPath), jstr(c.Title), typ))
		}
		return reply(`{"total_count":` + strconv.Itoa(len(items)) + `,"items":[` + strings.Join(items, ",") + `]}`)
	case endpoint == "orgs/"+st.Owner+"/issue-types" && method == "GET":
		if !st.OwnerOrg {
			return fail("gh: Not Found (HTTP 404)")
		}
		var ts []string
		for i, n := range st.Types {
			ts = append(ts, fmt.Sprintf(`{"id":%d,"name":%s}`, 900+i, jstr(n)))
		}
		return reply("[" + strings.Join(ts, ",") + "]")
	case endpoint == repo+"/issues" && method == "POST":
		var req struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
			Type   string   `json:"type"`
		}
		_ = json.Unmarshal([]byte(stdin), &req)
		n := 1
		for _, k := range sortedKeys(st.Issues) {
			a, _ := strconv.Atoi(k)
			n = max(n, a+1)
		}
		k := strconv.Itoa(n)
		st.Issues[k] = fakeIssue{Title: req.Title, Body: req.Body, State: "OPEN", Updated: tick(st), Labels: req.Labels, Type: req.Type}
		return reply(fmt.Sprintf(`{"number":%d,"id":%d,"html_url":%s,"updated_at":%s}`, n, 5000+n, jstr(issueURL(st, k)), jstr(fmt.Sprintf("2024-01-01T00:00:%02dZ", st.Issues[k].Updated))))
	case strings.HasPrefix(endpoint, repo+"/issues/"):
		tail := strings.TrimPrefix(endpoint, repo+"/issues/")
		n, sub, _ := strings.Cut(tail, "/")
		is, ok := st.Issues[n]
		if !ok {
			return fail("gh: Not Found (HTTP 404)")
		}
		var body map[string]string
		_ = json.Unmarshal([]byte(stdin), &body)
		switch {
		case method == "POST" && sub == "sub_issues":
			if st.FailSub {
				return fail("gh: Validation Failed (HTTP 422)")
			}
			var req struct {
				ID int `json:"sub_issue_id"`
			}
			_ = json.Unmarshal([]byte(stdin), &req)
			c := strconv.Itoa(req.ID - 5000)
			ci, ok := st.Issues[c]
			if !ok {
				return fail("gh: Not Found (HTTP 404)")
			}
			ci.Parent, _ = strconv.Atoi(n)
			ci.Updated = tick(st)
			st.Issues[c] = ci
			return reply(`{}`)
		case method == "POST" && sub == "comments":
			st.Comments++
			is.Updated = tick(st)
			st.Issues[n] = is
			return reply(fmt.Sprintf(`{"id":%d,"html_url":%s}`, 1000+st.Comments, jstr(issueURL(st, n)+"#issuecomment-"+strconv.Itoa(1000+st.Comments))))
		case method == "PATCH" && sub == "":
			is.State = strings.ToUpper(body["state"])
			is.Updated = tick(st)
			st.Issues[n] = is
			return reply(`{}`)
		}
	}
	return fail("fake gh: unexpected: " + strings.Join(args, " "))
}

func tick(st *fakeState) int { st.Clock++; return 10 + st.Clock }

func issueURL(st *fakeState, n string) string {
	return "https://github.com/" + st.Owner + "/" + st.Name + "/issues/" + n
}

func ref(st *fakeState, n string, is fakeIssue, repoPath string) string {
	if repoPath == "" {
		repoPath = st.Owner + "/" + st.Name
	}
	typ := "null"
	if is.Type != "" {
		typ = `{"name":` + jstr(is.Type) + `}`
	}
	return fmt.Sprintf(`{"number":%s,"url":%s,"title":%s,"issueType":%s,"repository":{"nameWithOwner":%s}}`, n, jstr(issueURL(st, n)), jstr(is.Title), typ, jstr(repoPath))
}

func jstr(s string) string { b, _ := json.Marshal(s); return string(b) }

func sortedKeys(m map[string]fakeIssue) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { a, _ := strconv.Atoi(ks[i]); b, _ := strconv.Atoi(ks[j]); return a < b })
	return ks
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}
