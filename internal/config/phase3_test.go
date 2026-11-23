package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// AC3: exec is a tracker kind; an unknown kind is still refused.
func TestTrackerKindExec(t *testing.T) {
	if err := checkValue(KeyTrackerKind, "exec"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := checkValue(KeyTrackerKind, "plain"); err == nil {
		t.Fatal("plain accepted")
	}
	e := repoEnv(t)
	if err := open(t, e).Set(ScopeGlobal, KeyTrackerKind, "exec"); err != nil {
		t.Fatal(err)
	}
}

// AC2: tracker.command is global-only: Set and Unset refuse it in every
// scope, and a list written to the global file by hand is read and round-trips.
func TestTrackerCommandSetScopes(t *testing.T) {
	e := repoEnv(t)
	l := open(t, e)
	for _, scope := range []string{ScopeRepo, ScopeLocal, ScopeGlobal} {
		for _, key := range []string{KeyTrackerCommand, "Tracker.Command"} {
			if err := l.Set(scope, key, `["prog"]`); err == nil || !strings.Contains(err.Error(), "read only from the global file") {
				t.Errorf("Set(%s, %s) = %v, want refusal", scope, key, err)
			}
			if err := l.Unset(scope, key); err == nil || !strings.Contains(err.Error(), "read only from the global file") {
				t.Errorf("Unset(%s, %s) = %v, want refusal", scope, key, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(e.root, ".rig")); err == nil {
		t.Error("a refused write created .rig")
	}
	want := []string{"/usr/bin/prog", "--flag", "a b"}
	e.global(t, `{"tracker":{"command":["/usr/bin/prog","--flag","a b"]}}`)
	v, ok := open(t, e).Get(KeyTrackerCommand)
	if !ok || v.Scope != ScopeGlobal || !reflect.DeepEqual(v.Value, want) {
		t.Fatalf("Get = %+v, %v", v, ok)
	}
}

// AC1: a project file never supplies tracker.command, by any path.
func TestTrackerCommandIgnoredFromProject(t *testing.T) {
	e := newEnv(t)
	e.local(t, `{"tracker":{"command":["evil"]}}`)
	e.rigJSON(t, `{}`)
	l := open(t, e)
	if v, ok := l.Get(KeyTrackerCommand); ok {
		t.Fatalf("Get = %+v", v)
	}
	if _, ok := l.GetIn(ScopeLocal, KeyTrackerCommand); ok {
		t.Fatal("GetIn local")
	}
	for _, v := range l.List() {
		if v.Key == KeyTrackerCommand {
			t.Fatalf("List has %+v", v)
		}
	}
	if len(l.Warnings()) == 0 {
		t.Error("no warning")
	}
	if v, err := l.TrackerCommand(); err != nil || v.Value != nil {
		t.Fatalf("TrackerCommand = %+v, %v", v, err)
	}
	// The global file's value wins and is the only source.
	e.global(t, `{"tracker":{"command":["good","x"]}}`)
	l = open(t, e)
	v, err := l.TrackerCommand()
	if err != nil || v.Scope != ScopeGlobal || !reflect.DeepEqual(v.Value, []string{"good", "x"}) {
		t.Fatalf("TrackerCommand = %+v, %v", v, err)
	}
	e.global(t, `{"tracker":{"command":"notalist"}}`)
	if _, err := Open(e.opts()); err == nil {
		t.Fatal("string command accepted")
	}
}

// AC4: the jira keys validate and resolve through every scope.
func TestJiraKeys(t *testing.T) {
	for _, v := range []string{"auto", "v2", "v3"} {
		if err := checkValue(KeyTrackerJiraAPI, v); err != nil {
			t.Errorf("api %q: %v", v, err)
		}
	}
	for _, v := range []string{"", "v1", "V3", "auto "} {
		if err := checkValue(KeyTrackerJiraAPI, v); err == nil {
			t.Errorf("api %q accepted", v)
		}
	}
	for _, v := range []string{"customfield_10014", "epic-link"} {
		if err := checkValue(KeyTrackerJiraEpicField, v); err != nil {
			t.Errorf("epicField %q: %v", v, err)
		}
	}
	for _, v := range []string{"", "a b", "x/y"} {
		if err := checkValue(KeyTrackerJiraEpicField, v); err == nil {
			t.Errorf("epicField %q accepted", v)
		}
	}
	l := open(t, newEnv(t))
	if l.JiraAPI() != "auto" {
		t.Errorf("default api = %q", l.JiraAPI())
	}
	if _, ok := l.Get(KeyTrackerJiraEpicField); ok {
		t.Error("epicField set by default")
	}
	vals := map[string]string{KeyTrackerJiraAPI: "v2", KeyTrackerJiraEpicField: "customfield_1"}
	for key, val := range vals {
		for mask := 0; mask < 16; mask++ {
			e := newEnv(t)
			nested := `{"tracker":{"jira":{"` + strings.TrimPrefix(key, "tracker.jira.") + `":"` + val + `"}}}`
			var global, repo string
			if mask&1 != 0 {
				e.local(t, nested)
			}
			if mask&2 != 0 {
				repo = `"repos":{"github.com/acme/rig":` + nested + `}`
			}
			if mask&4 != 0 {
				e.rigJSON(t, `{}`)
			}
			if mask&8 != 0 {
				global = nested[1 : len(nested)-1]
			}
			e.global(t, "{"+strings.Trim(global+","+repo, ",")+"}")
			v, ok := open(t, e).Get(key)
			want := ""
			for bit, scope := range []string{ScopeLocal, ScopeRepo, "", ScopeGlobal} {
				if scope != "" && mask&(1<<bit) != 0 {
					want = scope
					break
				}
			}
			if want == "" && ok || want != "" && (!ok || v.Scope != want || v.Value != val) {
				t.Errorf("%s mask %d: %+v %v, want scope %q", key, mask, v, ok, want)
			}
		}
	}
	e := newEnv(t)
	e.local(t, `{"tracker":{"jira":{"api":"v1"}}}`)
	if _, err := Open(e.opts()); err == nil {
		t.Error("bad api in file accepted")
	}
	l = open(t, newEnv(t))
	if err := l.Set(ScopeGlobal, KeyTrackerJiraAPI, "v3"); err != nil || l.JiraAPI() != "v3" {
		t.Fatalf("Set: %v, api %q", err, l.JiraAPI())
	}
	if err := l.Set(ScopeGlobal, KeyTrackerJiraAPI, "v9"); err == nil {
		t.Fatal("v9 accepted")
	}
}

// AC4: rig.json has no jira keys today, so it contributes none; the order
// local, repo, rig.json, global is the shared tracker order.
func TestJiraKeysOrderMatchesTrackerKeys(t *testing.T) {
	want := order(KeyTrackerKind)
	for _, k := range []string{KeyTrackerJiraAPI, KeyTrackerJiraEpicField} {
		if got := order(k); !reflect.DeepEqual(got, want) {
			t.Errorf("order(%s) = %v, want %v", k, got, want)
		}
	}
	if got := order(KeyTrackerCommand); !reflect.DeepEqual(got, order(KeyWorkflow)) {
		t.Errorf("order(tracker.command) = %v", got)
	}
}

func trackFile(t *testing.T, e env, rel, body string) {
	t.Helper()
	put(t, filepath.Join(e.root, rel), body)
	git(t, e.root, "add", "-f", rel)
}

// The local-config check: every way .rig can come from the repository.
func TestLocalCommitted(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T, e env, want bool) {
		t.Helper()
		got, err := LocalCommitted(ctx, e.root)
		if err != nil || got != want {
			t.Fatalf("LocalCommitted = %v, %v; want %v", got, err, want)
		}
	}
	t.Run("untracked", func(t *testing.T) {
		e := repoEnv(t)
		e.local(t, `{}`)
		check(t, e, false)
	})
	t.Run("tracked file", func(t *testing.T) {
		e := repoEnv(t)
		trackFile(t, e, ".rig/config.json", `{}`)
		check(t, e, true)
	})
	t.Run("other tracked path under .rig", func(t *testing.T) {
		e := repoEnv(t)
		trackFile(t, e, ".rig/other/x", `x`)
		check(t, e, true)
	})
	t.Run("case variant dir", func(t *testing.T) {
		e := repoEnv(t)
		trackFile(t, e, ".RIG/config.json", `{}`)
		check(t, e, true)
	})
	t.Run("case variant file", func(t *testing.T) {
		e := repoEnv(t)
		trackFile(t, e, ".rig/Config.json", `{}`)
		check(t, e, true)
	})
	t.Run("gitlink", func(t *testing.T) {
		e := repoEnv(t)
		git(t, e.root, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",.rig")
		check(t, e, true)
	})
	t.Run("symlinked dir", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks needs privileges on Windows")
		}
		e := repoEnv(t)
		put(t, filepath.Join(e.root, "real", "config.json"), `{}`)
		if err := os.Symlink("real", filepath.Join(e.root, ".rig")); err != nil {
			t.Fatal(err)
		}
		check(t, e, true)
	})
	t.Run("symlinked file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks needs privileges on Windows")
		}
		e := repoEnv(t)
		put(t, filepath.Join(e.root, "elsewhere.json"), `{}`)
		if err := os.MkdirAll(filepath.Join(e.root, ".rig"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "elsewhere.json"), filepath.Join(e.root, ".rig", "config.json")); err != nil {
			t.Fatal(err)
		}
		check(t, e, true)
	})
	t.Run("not a repository fails closed", func(t *testing.T) {
		if _, err := LocalCommitted(ctx, newEnv(t).root); err == nil {
			t.Fatal("no error outside a repository")
		}
	})
}

const trackedURL = `{"tracker":{"kind":"jira","project":"RIG","url":"https://evil.example.com"}}`

// AC1: a tracked .rig/config.json cannot supply tracker.url through get,
// list or the resolver; kind and project still come through.
func TestTrackedLocalURLNeverResolves(t *testing.T) {
	variants := map[string]func(t *testing.T, e env){
		"tracked": func(t *testing.T, e env) { trackFile(t, e, ".rig/config.json", trackedURL) },
		"case":    func(t *testing.T, e env) { trackFile(t, e, ".RIG/config.json", "{}"); e.local(t, trackedURL) },
		"other":   func(t *testing.T, e env) { trackFile(t, e, ".rig/x", "x"); e.local(t, trackedURL) },
		"gitlink": func(t *testing.T, e env) {
			e.local(t, trackedURL)
			git(t, e.root, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",.rig")
		},
	}
	for name, setup := range variants {
		t.Run(name, func(t *testing.T) {
			e := repoEnv(t)
			setup(t, e)
			l := open(t, e)
			if v, ok := l.Get(KeyTrackerURL); ok {
				t.Fatalf("Get = %+v", v)
			}
			if _, ok := l.GetIn(ScopeLocal, KeyTrackerURL); ok {
				t.Fatal("GetIn local")
			}
			for _, v := range l.List() {
				if v.Key == KeyTrackerURL {
					t.Fatalf("List has %+v", v)
				}
			}
			v, err := l.TrackerURL()
			if !errors.Is(err, ErrLocalCommitted) || v.Value != nil {
				t.Fatalf("TrackerURL = %+v, %v", v, err)
			}
			if _, err := l.Tracker(); err == nil {
				t.Fatal("Tracker resolved a jira site from a tracked file")
			}
			if len(l.Warnings()) == 0 {
				t.Error("no warning")
			}
			if k, _ := l.Get(KeyTrackerKind); k.Value != "jira" || k.Scope != ScopeLocal {
				t.Errorf("kind = %+v", k)
			}
			if p, _ := l.Get(KeyTrackerProject); p.Value != "RIG" {
				t.Errorf("project = %+v", p)
			}
		})
	}
}

// A tracked file does not hide a url from a trusted scope, and an untracked
// local file's url resolves.
func TestTrackerURLResolver(t *testing.T) {
	e := repoEnv(t)
	e.local(t, trackedURL)
	l := open(t, e)
	v, err := l.TrackerURL()
	if err != nil || v.Scope != ScopeLocal || v.Value != "https://evil.example.com" {
		t.Fatalf("untracked: %+v, %v", v, err)
	}

	e = repoEnv(t)
	trackFile(t, e, ".rig/config.json", trackedURL)
	e.global(t, `{"tracker":{"url":"https://good.example.com"}}`)
	l = open(t, e)
	// Get shows the trusted global value; the resolver still refuses, as the
	// tracked file would have won.
	if g, _ := l.Get(KeyTrackerURL); g.Scope != ScopeGlobal || g.Value != "https://good.example.com" {
		t.Fatalf("Get = %+v", g)
	}
	if v, err = l.TrackerURL(); !errors.Is(err, ErrLocalCommitted) || v.Value != nil {
		t.Fatalf("tracked over global: %+v, %v", v, err)
	}

	// Nothing set: no value, no error.
	v, err = open(t, repoEnv(t)).TrackerURL()
	if err != nil || v.Value != nil {
		t.Fatalf("unset: %+v, %v", v, err)
	}

	// A check that cannot run fails closed.
	e = newEnv(t) // not a git repository
	e.local(t, trackedURL)
	v, err = open(t, e).TrackerURL()
	if err == nil || errors.Is(err, ErrLocalCommitted) || v.Value != nil {
		t.Fatalf("not a repo: %+v, %v", v, err)
	}
	if _, ok := open(t, e).Get(KeyTrackerURL); ok {
		t.Fatal("Get returned a url that could not be checked")
	}
}
