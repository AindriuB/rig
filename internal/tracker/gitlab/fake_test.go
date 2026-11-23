//go:build !windows

package gitlab

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The fake glab is this test binary re-run through a /bin/sh shim, so it can
// keep state (issues, notes) in a file and read the stdin JSON. It serves the
// calls recorded in the package comment and logs every call to a file.

const fakeProject = "grp/sub/proj"

type fakeIssue struct {
	IID       int64    `json:"iid"`
	WebURL    string   `json:"web_url"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Labels    []string `json:"labels"`
	IssueType string   `json:"issue_type"`
	Updated   string   `json:"updated_at"`
	Parent    int64    `json:"parent_iid"`
}

type fakeState struct {
	Hierarchy bool        `json:"hierarchy"`
	NoAuth    bool        `json:"noauth"`
	GQLFail   bool        `json:"gqlfail"`
	Counter   int         `json:"counter"`
	Issues    []fakeIssue `json:"issues"`
}

type fakeCall struct {
	Args      []string `json:"args"`
	Stdin     string   `json:"stdin"`
	TokenSeen bool     `json:"token_seen"`
}

func issueURL(iid int64) string {
	return "https://gitlab.example/" + fakeProject + "/-/issues/" + strconv.FormatInt(iid, 10)
}

// TestFakeGlab is the fake glab's body. It does nothing unless the shim runs it.
func TestFakeGlab(t *testing.T) {
	dir := os.Getenv("RIG_FAKE_GLAB_DIR")
	if dir == "" {
		t.Skip("only runs as the fake glab")
	}
	os.Exit(fakeGlab(dir, os.Args[slices.Index(os.Args, "--")+1:]))
}

func fakeGlab(dir string, args []string) int {
	stdin := ""
	var fields []string
	var endpoint, method string
	var usesInput bool
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "api":
		case "--hostname", "--header":
			i++
		case "-X":
			i++
			method = args[i]
		case "-f":
			i++
			fields = append(fields, args[i])
		case "--input":
			i++
			usesInput = true
		default:
			endpoint = a
		}
	}
	if usesInput {
		b, _ := io.ReadAll(os.Stdin)
		stdin = string(b)
	}
	_, token := os.LookupEnv("RIG_JIRA_TOKEN")
	line, _ := json.Marshal(fakeCall{Args: args, Stdin: stdin, TokenSeen: token})
	f, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, "%s\n", line)
	f.Close()

	var st fakeState
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	_ = json.Unmarshal(b, &st)
	save := func() {
		b, _ := json.Marshal(st)
		_ = os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644)
	}
	fail := func(msg string) int { fmt.Fprintln(os.Stderr, msg); return 1 }
	find := func(iid int64) *fakeIssue {
		for i := range st.Issues {
			if st.Issues[i].IID == iid {
				return &st.Issues[i]
			}
		}
		return nil
	}
	field := func(k string) string {
		for _, f := range fields {
			if v, ok := strings.CutPrefix(f, k+"="); ok {
				return v
			}
		}
		return ""
	}
	out := func(v any) int { b, _ := json.Marshal(v); fmt.Println(string(b)); return 0 }

	if st.NoAuth {
		return fail("glab: 401 Unauthorized")
	}
	base := "projects/" + url.PathEscape(fakeProject) + "/issues"
	switch {
	case endpoint == "user":
		return out(map[string]string{"username": "tester"})
	case endpoint == "graphql":
		return fakeGraphQL(&st, stdin, out)
	case endpoint == base && method == "GET":
		var res []fakeIssue
		for _, is := range st.Issues {
			if l := field("labels"); l != "" && !slices.Contains(is.Labels, l) {
				continue
			}
			res = append(res, is)
		}
		if n, err := strconv.Atoi(field("per_page")); err == nil && len(res) > n {
			res = res[:n]
		}
		return out(res)
	case strings.HasPrefix(endpoint, base+"/"):
		rest := strings.TrimPrefix(endpoint, base+"/")
		iidStr, sub, _ := strings.Cut(rest, "/")
		iid, err := strconv.ParseInt(iidStr, 10, 64)
		if err != nil || find(iid) == nil {
			return fail("glab: 404 Not Found")
		}
		is := find(iid)
		switch {
		case sub == "" && method == "GET":
			return out(is)
		case sub == "" && method == "PUT":
			st.Counter++
			is.Updated = "2026-01-01T00:00:" + fmt.Sprintf("%02d", st.Counter) + "Z"
			if field("state_event") == "close" {
				is.State = "closed"
			} else {
				is.State = "opened"
			}
			save()
			return out(is)
		case sub == "notes" && method == "POST":
			st.Counter++
			save()
			return out(map[string]any{"id": 100 + st.Counter})
		}
	}
	return fail("fake glab: unexpected call " + strings.Join(args, " "))
}

func fakeGraphQL(st *fakeState, stdin string, out func(any) int) int {
	if st.GQLFail {
		// Real glab exits 1 on a GraphQL errors array: message on stderr, body on stdout.
		body := `{"errors":[{"message":"Field 'widgetDefinitions' doesn't exist on type 'WorkItemType'"}]}`
		fmt.Println(body)
		fmt.Fprintln(os.Stderr, "glab: Field 'widgetDefinitions' doesn't exist on type 'WorkItemType'")
		return 1
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal([]byte(stdin), &req)
	if strings.Contains(req.Query, "workItemTypes") {
		if !st.Hierarchy {
			return out(map[string]any{"data": map[string]any{"project": map[string]any{"workItemTypes": map[string]any{"nodes": []any{
				map[string]any{"name": "Issue", "widgetDefinitions": []any{map[string]string{"type": "LABELS"}}},
			}}}}})
		}
		return out(map[string]any{"data": map[string]any{"project": map[string]any{"workItemTypes": map[string]any{"nodes": []any{
			map[string]any{"name": "Issue", "widgetDefinitions": []any{map[string]string{"type": "HIERARCHY"}}},
		}}}}})
	}
	iid, _ := strconv.ParseInt(fmt.Sprint(req.Variables["iid"]), 10, 64)
	ref := func(is fakeIssue) map[string]any {
		return map[string]any{"iid": strconv.FormatInt(is.IID, 10), "webUrl": is.WebURL, "title": is.Title,
			"workItemType": map[string]string{"name": "Issue"}}
	}
	var nodes []any
	for _, is := range st.Issues {
		if is.IID != iid {
			continue
		}
		var widget = map[string]any{"type": "HIERARCHY", "children": map[string]any{"nodes": []any{}}}
		kids := []any{}
		for _, k := range st.Issues {
			if k.Parent == iid {
				kids = append(kids, ref(k))
			}
		}
		widget["children"] = map[string]any{"nodes": kids}
		if is.Parent != 0 {
			for _, p := range st.Issues {
				if p.IID == is.Parent {
					widget["parent"] = ref(p)
				}
			}
		}
		nodes = append(nodes, map[string]any{"widgets": []any{map[string]string{"type": "LABELS"}, widget}})
	}
	if nodes == nil {
		nodes = []any{}
	}
	return out(map[string]any{"data": map[string]any{"project": map[string]any{"workItems": map[string]any{"nodes": nodes}}}})
}

// fakeEnv installs the fake glab on PATH and returns its state dir.
func fakeEnv(t *testing.T, hierarchy bool) string {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nRIG_FAKE_GLAB_DIR='" + dir + "' exec '" + exe + "' -test.run='^TestFakeGlab$' -- \"$@\"\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	mk := func(iid, parent int64, title string, labels ...string) fakeIssue {
		return fakeIssue{IID: iid, WebURL: issueURL(iid), Title: title, State: "opened", Labels: labels,
			IssueType: "issue", Updated: "2026-01-01T00:00:00Z", Parent: parent}
	}
	st := fakeState{Hierarchy: hierarchy, Issues: []fakeIssue{mk(1, 0, "parent", "rig"), mk(2, 1, "child", "rig"), mk(3, 0, "other", "other")}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readCalls(t *testing.T, dir string) []fakeCall {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	var calls []fakeCall
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var c fakeCall
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
