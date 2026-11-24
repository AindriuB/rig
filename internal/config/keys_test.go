package config

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCheckValue(t *testing.T) {
	abs, _ := filepath.Abs(filepath.Join("wf", "flow.json"))
	good := map[string][]string{
		KeyForgeKind:      {"github", "gitlab"},
		KeyForgeHost:      {"github.com", "git.example.com:8443", "localhost"},
		KeyTrackerKind:    {"local", "jira", "github", "gitlab"},
		KeyTrackerURL:     {"https://acme.atlassian.net", "https://jira.example.com/base"},
		KeyTrackerProject: {"RIG", "B2B", "acme/rig", "group/sub/name"},
		KeyWorkflow:       {"basic", "two-phase", abs},
	}
	bad := map[string][]string{
		KeyForgeKind:      {"", "bitbucket", "GitHub"},
		KeyForgeHost:      {"", "-x.com", "a b", "https://github.com", "host/path"},
		KeyTrackerKind:    {"", "plain", "Jira"},
		KeyTrackerURL:     {"", "http://acme.atlassian.net", "https://", "acme.atlassian.net", "https://u:p@acme.atlassian.net", "ftp://x"},
		KeyTrackerProject: {"", "rig", "R", "acme/", "/rig", "a b"},
		KeyWorkflow:       {"", "Basic", "rel/path.json", "../x"},
	}
	for key, vs := range good {
		for _, v := range vs {
			if err := checkValue(key, v); err != nil {
				t.Errorf("checkValue(%s, %q) = %v", key, v, err)
			}
		}
	}
	for key, vs := range bad {
		for _, v := range vs {
			if err := checkValue(key, v); err == nil {
				t.Errorf("checkValue(%s, %q) = nil", key, v)
			}
		}
	}
}

func TestCheckProjectByKind(t *testing.T) {
	cases := []struct {
		kind, project string
		ok            bool
	}{
		{"jira", "RIG", true},
		{"jira", "RIG_2", true},
		{"jira", "rig", false},
		{"jira", "R", false},
		{"jira", "acme/rig", false},
		{"github", "acme/rig", true},
		{"github", "a/b/c", false},
		{"github", "RIG", false},
		{"gitlab", "group/sub/name", true},
		{"gitlab", "RIG", false},
		{"local", "RIG", true},
		{"local", "acme/rig", true},
	}
	for _, c := range cases {
		if err := checkProject(c.kind, c.project); (err == nil) != c.ok {
			t.Errorf("checkProject(%s, %s) = %v", c.kind, c.project, err)
		}
	}
}

func TestCheckSettableRefusesCredentials(t *testing.T) {
	for _, key := range []string{
		"tracker.token", "jira.Token", "tracker.password", "x.SECRET",
		"tracker.apikey", "tracker.apiKey", "forge.key", "jira.auth",
		"tracker.credential", "tracker.credentials", "token",
	} {
		err := checkSettable(key)
		if err == nil || !strings.Contains(err.Error(), "rig login") {
			t.Errorf("checkSettable(%q) = %v, want a refusal naming rig login", key, err)
		}
	}
}

func TestCheckSettableRefusesGlobalOnlyAndUnknown(t *testing.T) {
	for _, key := range []string{
		"push.prefixes", "pushPrefixes", "pushprefixes", "push_prefixes",
		"Push.Prefixes", "PUSH-PREFIXES", "workflow", "Workflow",
	} {
		err := checkSettable(key)
		if err == nil || !strings.Contains(err.Error(), "global file") {
			t.Errorf("checkSettable(%q) = %v, want global-only refusal", key, err)
		}
	}
	for _, key := range []string{"forge", "forge.kinds", "tracker", "repos", "", "tracker.url.x"} {
		if err := checkSettable(key); err == nil {
			t.Errorf("checkSettable(%q) = nil", key)
		}
	}
	for _, key := range []string{KeyForgeKind, KeyForgeHost, KeyTrackerKind, KeyTrackerURL, KeyTrackerProject} {
		if err := checkSettable(key); err != nil {
			t.Errorf("checkSettable(%q) = %v", key, err)
		}
	}
}

func TestKeysIsACopy(t *testing.T) {
	k := Keys()
	if len(k) != len(keys) || !slices.IsSorted(k) {
		t.Fatalf("Keys() = %v", k)
	}
	k[0] = "mutated"
	if keys[0] == "mutated" {
		t.Fatal("Keys() returned the internal slice")
	}
}

// 3.7b AC7: tracker.jira.acceptanceField is a field id, settable in every scope.
func TestJiraAcceptanceFieldKey(t *testing.T) {
	for _, v := range []string{"customfield_10100", "acceptance-criteria"} {
		if err := checkValue(KeyTrackerJiraAcceptanceField, v); err != nil {
			t.Errorf("acceptanceField %q: %v", v, err)
		}
	}
	for _, v := range []string{"", "a b", "x/y", "a.b"} {
		if err := checkValue(KeyTrackerJiraAcceptanceField, v); err == nil {
			t.Errorf("acceptanceField %q accepted", v)
		}
	}
	if !slices.Contains(Keys(), KeyTrackerJiraAcceptanceField) {
		t.Error("key not on the allow-list")
	}
	l := open(t, repoEnv(t))
	if _, ok := l.Get(KeyTrackerJiraAcceptanceField); ok {
		t.Error("acceptanceField set by default")
	}
	for _, scope := range []string{ScopeGlobal, ScopeRepo, ScopeLocal} {
		if err := l.Set(scope, KeyTrackerJiraAcceptanceField, "customfield_7"); err != nil {
			t.Fatalf("Set(%s): %v", scope, err)
		}
		if v, _ := l.GetIn(scope, KeyTrackerJiraAcceptanceField); v.Value != "customfield_7" {
			t.Errorf("GetIn(%s) = %+v", scope, v)
		}
	}
	if err := l.Set(ScopeGlobal, KeyTrackerJiraAcceptanceField, "bad id"); err == nil {
		t.Error("bad id set")
	}
}

// 3.15 AC3: worktrees.root is a non-empty path, default ".worktrees", settable
// in every scope.
func TestWorktreesRootKey(t *testing.T) {
	for _, v := range []string{".worktrees", "wt", "/abs/path", `C:\wt`} {
		if err := checkValue(KeyWorktreesRoot, v); err != nil {
			t.Errorf("worktrees.root %q: %v", v, err)
		}
	}
	for _, v := range []string{"", "a\x00b"} {
		if err := checkValue(KeyWorktreesRoot, v); err == nil {
			t.Errorf("worktrees.root %q accepted", v)
		}
	}
	if !slices.Contains(Keys(), KeyWorktreesRoot) || !slices.IsSorted(Keys()) {
		t.Error("key not on the sorted allow-list")
	}
	l := open(t, repoEnv(t))
	if got, err := l.WorktreesRoot(); err != nil || got != DefaultWorktreesRoot {
		t.Errorf("default = %q, %v", got, err)
	}
	for _, scope := range []string{ScopeGlobal, ScopeRepo, ScopeLocal} {
		if err := l.Set(scope, KeyWorktreesRoot, "wt-"+scope); err != nil {
			t.Fatalf("Set(%s): %v", scope, err)
		}
	}
	if got, err := l.WorktreesRoot(); err != nil || got != "wt-local" {
		t.Errorf("WorktreesRoot = %q, %v", got, err)
	}
}

// F3: a repository-supplied layer may only point worktrees.root inside the
// checkout; an absolute path or a '..' element is accepted from the user's
// global file (and its per-repo section) alone.
func TestWorktreesRootTrust(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x")
	for _, v := range []string{"../../x", "a/../../x", "..", abs, "/tmp/x"} {
		e := newEnv(t)
		e.local(t, `{"worktrees":{"root":`+strconv.Quote(v)+`}}`)
		_, err := open(t, e).WorktreesRoot()
		if err == nil || !strings.Contains(err.Error(), filepath.Join(e.root, ".rig", "config.json")) {
			t.Errorf("local %q: err = %v, want one naming the file", v, err)
		}
	}
	e := newEnv(t)
	e.local(t, `{"worktrees":{"root":"sub/wt"}}`)
	if got, err := open(t, e).WorktreesRoot(); err != nil || got != "sub/wt" {
		t.Errorf("local relative = %q, %v", got, err)
	}
	e = newEnv(t)
	e.global(t, `{"worktrees":{"root":`+strconv.Quote(abs)+`}}`)
	if got, err := open(t, e).WorktreesRoot(); err != nil || got != abs {
		t.Errorf("global absolute = %q, %v", got, err)
	}
	e = newEnv(t)
	e.global(t, `{"repos":{"github.com/acme/rig":{"worktrees":{"root":"../../x"}}}}`)
	if got, err := open(t, e).WorktreesRoot(); err != nil || got != "../../x" {
		t.Errorf("per-repo '..' = %q, %v", got, err)
	}
}
