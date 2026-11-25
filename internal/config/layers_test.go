package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testRemote = "git@github.com:acme/rig.git"

// env is a fake home and a checkout root, both under t.TempDir.
type env struct{ home, root string }

func newEnv(t *testing.T) env {
	t.Helper()
	return env{home: t.TempDir(), root: t.TempDir()}
}

func (e env) opts() Options {
	return Options{Home: func() (string, error) { return e.home, nil }, Root: e.root, Remote: testRemote}
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e env) global(t *testing.T, body string) {
	put(t, filepath.Join(e.home, ".rig", "config.json"), body)
}
func (e env) local(t *testing.T, body string) {
	put(t, filepath.Join(e.root, ".rig", "config.json"), body)
}
func (e env) rigJSON(t *testing.T, body string) {
	put(t, filepath.Join(e.root, ".claude", "rig.json"), body)
}

func open(t *testing.T, e env) *Layers {
	t.Helper()
	l, err := Open(e.opts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l
}

// AC1: every combination of the four file scopes for a key all four can
// hold; the highest present wins, and with none the key is unset.
func TestLookupOrderEveryCombination(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			e := newEnv(t)
			has := func(bit int) bool { return mask&(1<<bit) != 0 }
			var globalTop, repo string
			if has(0) {
				e.local(t, `{"forge":{"host":"local.example"}}`)
			}
			if has(1) {
				repo = `"repos":{"github.com/acme/rig":{"forge":{"host":"repo.example"}}}`
			}
			if has(2) {
				e.rigJSON(t, `{"host":"rigjson.example"}`)
			}
			if has(3) {
				globalTop = `"forge":{"host":"global.example"}`
			}
			e.global(t, "{"+strings.Trim(strings.Join([]string{globalTop, repo}, ","), ",")+"}")
			v, ok := open(t, e).Get(KeyForgeHost)
			want := ""
			for bit, scope := range []string{ScopeLocal, ScopeRepo, ScopeRigJSON, ScopeGlobal} {
				if has(bit) {
					want = scope
					break
				}
			}
			if want == "" {
				if ok {
					t.Fatalf("Get = %+v, want unset", v)
				}
				return
			}
			host := map[string]string{ScopeLocal: "local", ScopeRepo: "repo", ScopeRigJSON: "rigjson", ScopeGlobal: "global"}[want] + ".example"
			if !ok || v.Scope != want || v.Value != host {
				t.Fatalf("Get = %+v, %v; want %s from %s", v, ok, host, want)
			}
		})
	}
}

// AC1: a key rig.json cannot hold falls through to global, then default.
func TestLookupOrderWithDefault(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		e := newEnv(t)
		has := func(bit int) bool { return mask&(1<<bit) != 0 }
		if has(0) {
			e.local(t, `{"tracker":{"kind":"github"}}`)
		}
		parts := []string{}
		if has(1) {
			parts = append(parts, `"repos":{"github.com/acme/rig":{"tracker":{"kind":"gitlab"}}}`)
		}
		if has(2) {
			parts = append(parts, `"tracker":{"kind":"jira"}`)
		}
		e.rigJSON(t, `{"forge":"gitlab"}`)
		e.global(t, "{"+strings.Join(parts, ",")+"}")
		v, ok := open(t, e).Get(KeyTrackerKind)
		want := Value{Key: KeyTrackerKind, Value: "local", Scope: ScopeDefault}
		switch {
		case has(0):
			want.Value, want.Scope = "github", ScopeLocal
		case has(1):
			want.Value, want.Scope = "gitlab", ScopeRepo
		case has(2):
			want.Value, want.Scope = "jira", ScopeGlobal
		}
		if !ok || v != want {
			t.Errorf("mask %d: Get = %+v, want %+v", mask, v, want)
		}
	}
}

func TestRigJSONReadAsForgeKeys(t *testing.T) {
	e := newEnv(t)
	e.rigJSON(t, `{"forge":"gitlab","host":"git.example.com"}`)
	e.global(t, `{"forge":{"kind":"github","host":"github.com"}}`)
	l := open(t, e)
	if v, _ := l.Get(KeyForgeKind); v.Value != "gitlab" || v.Scope != ScopeRigJSON {
		t.Errorf("forge.kind = %+v", v)
	}
	if v, _ := l.Get(KeyForgeHost); v.Value != "git.example.com" || v.Scope != ScopeRigJSON {
		t.Errorf("forge.host = %+v", v)
	}
}

func TestPushPrefixesPrecedence(t *testing.T) {
	e := newEnv(t)
	l := open(t, e)
	if v, _ := l.Get(KeyPushPrefixes); !reflect.DeepEqual(v.Value, []string{"task", "record"}) || v.Scope != ScopeDefault {
		t.Errorf("default = %+v", v)
	}
	e.global(t, `{"push":{"prefixes":["g"]},"repos":{"github.com/acme/rig":{"push":{"prefixes":["r"]}}}}`)
	if v, _ := open(t, e).Get(KeyPushPrefixes); !reflect.DeepEqual(v.Value, []string{"r"}) || v.Scope != ScopeRepo {
		t.Errorf("repo = %+v", v)
	}
	e.rigJSON(t, `{"pushPrefixes":["wip"]}`)
	v, _ := open(t, e).Get(KeyPushPrefixes)
	if !reflect.DeepEqual(v.Value, []string{"wip"}) || v.Scope != ScopeRigJSON {
		t.Errorf("rig.json = %+v", v)
	}
	v.Value.([]string)[0] = "mutated"
	if v, _ := open(t, e).Get(KeyPushPrefixes); v.Value.([]string)[0] != "wip" {
		t.Error("Get returned a shared slice")
	}
}

// AC2: a project .rig file that sets a global-only key is ignored with a
// warning, even when its value is not valid.
func TestLocalGlobalOnlyKeysIgnored(t *testing.T) {
	e := newEnv(t)
	e.local(t, `{"workflow":"/bin/evil","push":{"prefixes":["main"]},"forge":{"kind":"gitlab"}}`)
	l := open(t, e)
	if v, _ := l.Get(KeyWorkflow); v.Value != "basic" || v.Scope != ScopeDefault {
		t.Errorf("workflow = %+v", v)
	}
	if v, _ := l.Get(KeyPushPrefixes); v.Scope != ScopeDefault {
		t.Errorf("push.prefixes = %+v", v)
	}
	if v, _ := l.Get(KeyForgeKind); v.Value != "gitlab" || v.Scope != ScopeLocal {
		t.Errorf("forge.kind = %+v", v)
	}
	w := strings.Join(l.Warnings(), "\n")
	if !strings.Contains(w, "workflow") || !strings.Contains(w, "push.prefixes") {
		t.Errorf("warnings = %q", w)
	}
	e.local(t, `{"push":{"prefixes":"not a list"},"workflow":3}`)
	if l := open(t, e); len(l.Warnings()) != 2 {
		t.Errorf("warnings = %q", l.Warnings())
	}
}

func TestWorkflowFromGlobalFile(t *testing.T) {
	e := newEnv(t)
	abs, _ := filepath.Abs("flow.json")
	e.global(t, fmt.Sprintf(`{"workflow":"two-phase","repos":{"github.com/acme/rig":{"workflow":%q}}}`, abs))
	if v, _ := open(t, e).Get(KeyWorkflow); v.Value != abs || v.Scope != ScopeRepo {
		t.Errorf("workflow = %+v", v)
	}
}

// AC6: values are checked when read.
func TestOpenRejectsBadFiles(t *testing.T) {
	cases := []struct{ global, local, rigJSON string }{
		{global: `{"forge":{"kind":"bitbucket"}}`},
		{global: `{"tracker":{"kind":"plain"}}`},
		{global: `{"tracker":{"url":"http://acme.atlassian.net"}}`},
		{global: `{"tracker":{"project":"lower"}}`},
		{global: `{"tracker":{"token":"abc"}}`},
		{global: `{"forge":{"kinds":"github"}}`},
		{global: `{"forge":"github"}`},
		{global: `{"pushPrefixes":["task"]}`},
		{global: `{"push":{"prefixes":[]}}`},
		{global: `{"push":{"prefixes":["-f"]}}`},
		{global: `{"workflow":"rel/path"}`},
		{global: `{"repos":[]}`},
		{global: `{"repos":{"github.com/acme/rig":{"tracker":{"kind":"x"}}}}`},
		{global: `not json`},
		{local: `{"forge":{"host":"a b"}}`},
		{local: `{"tracker":{"password":"x"}}`},
		{rigJSON: `{"forge":"bitbucket"}`},
		{rigJSON: `{"host":"-bad"}`},
	}
	for _, c := range cases {
		e := newEnv(t)
		if c.global != "" {
			e.global(t, c.global)
		}
		if c.local != "" {
			e.local(t, c.local)
		}
		if c.rigJSON != "" {
			e.rigJSON(t, c.rigJSON)
		}
		if _, err := Open(e.opts()); err == nil {
			t.Errorf("Open(%+v) = nil error", c)
		}
	}
}

// A repos section for another repository is not this repository's.
func TestRepoSectionForOtherRepoIgnored(t *testing.T) {
	e := newEnv(t)
	e.global(t, `{"repos":{"github.com/acme/other":{"tracker":{"project":"OTHER"}}}}`)
	if v, ok := open(t, e).Get(KeyTrackerProject); ok {
		t.Errorf("tracker.project = %+v", v)
	}
}

// AC6: with no files the tracker is local.
func TestNoFilesIsLocalTracker(t *testing.T) {
	e := newEnv(t)
	l := open(t, e)
	tr, err := l.Tracker()
	if err != nil || tr != (Tracker{Kind: "local"}) {
		t.Fatalf("Tracker = %+v, %v", tr, err)
	}
	if len(l.Warnings()) != 0 {
		t.Errorf("warnings = %q", l.Warnings())
	}
	want := []Value{
		{Key: KeyPushPrefixes, Value: []string{"task", "record"}, Scope: ScopeDefault},
		{Key: KeyTrackerKind, Value: "local", Scope: ScopeDefault},
		{Key: KeyUpdateAuto, Value: "off", Scope: ScopeDefault},
		{Key: KeyWorkflow, Value: "basic", Scope: ScopeDefault},
	}
	if got := l.List(); !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v", got)
	}
}

// AC7: Tracker names the key a jira tracker is missing.
func TestTracker(t *testing.T) {
	cases := []struct {
		global string
		want   Tracker
		errHas string
	}{
		{`{"tracker":{"kind":"jira"}}`, Tracker{}, "tracker.url"},
		{`{"tracker":{"kind":"jira","url":"https://acme.atlassian.net"}}`, Tracker{}, "tracker.project"},
		{`{"tracker":{"kind":"jira","project":"RIG"}}`, Tracker{}, "tracker.url"},
		{`{"tracker":{"kind":"jira","url":"https://acme.atlassian.net"},"repos":{"github.com/acme/rig":{"tracker":{"project":"RIG"}}}}`,
			Tracker{Kind: "jira", URL: "https://acme.atlassian.net", Project: "RIG"}, ""},
		{`{"tracker":{"kind":"jira","url":"https://acme.atlassian.net","project":"acme/rig"}}`, Tracker{}, "tracker.project"},
		{`{"tracker":{"kind":"github"}}`, Tracker{Kind: "github"}, ""},
		{`{"tracker":{"kind":"github","project":"acme/rig"}}`, Tracker{Kind: "github", Project: "acme/rig"}, ""},
		{`{"tracker":{"kind":"gitlab","project":"g/s/n","url":"https://git.example.com"}}`,
			Tracker{Kind: "gitlab", Project: "g/s/n", URL: "https://git.example.com"}, ""},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.global(t, c.global)
		l := open(t, e)
		got, err := l.Tracker()
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%s: Tracker err = %v, want naming %s", c.global, err, c.errHas)
			}
			if !strings.Contains(strings.Join(l.Warnings(), "\n"), c.errHas) {
				t.Errorf("%s: warnings = %q", c.global, l.Warnings())
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: Tracker = %+v, %v", c.global, got, err)
		}
	}
}

// AC4: the home directory comes from Options.Home.
func TestHomeInjected(t *testing.T) {
	e := newEnv(t)
	e.global(t, `{"forge":{"kind":"gitlab"}}`)
	called := false
	o := e.opts()
	o.Home = func() (string, error) { called = true; return e.home, nil }
	l, err := Open(o)
	if err != nil || !called {
		t.Fatalf("Open = %v, called %v", err, called)
	}
	if v, _ := l.Get(KeyForgeKind); v.Value != "gitlab" || v.Scope != ScopeGlobal {
		t.Errorf("forge.kind = %+v", v)
	}
	o.Home = func() (string, error) { return "", errors.New("no home") }
	if _, err := Open(o); err == nil {
		t.Error("Open with a failing Home = nil error")
	}
}

func TestOpenOutsideRepoAndWithoutRemote(t *testing.T) {
	e := newEnv(t)
	e.global(t, `{"tracker":{"kind":"github"},"repos":{"github.com/acme/rig":{"tracker":{"kind":"gitlab"}}}}`)
	o := e.opts()
	o.Root, o.Remote = "", ""
	l, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := l.Get(KeyTrackerKind); v.Value != "github" || v.Scope != ScopeGlobal {
		t.Errorf("tracker.kind = %+v", v)
	}
	if l.RepoKey() != "" {
		t.Errorf("RepoKey = %q", l.RepoKey())
	}
	if _, ok := l.Get("nope"); ok {
		t.Error("Get of an unknown key = ok")
	}
}

// A Home of "" means no global file: the layer is empty, and the file
// ./.rig/config.json in the current directory is not read as the global one.
func TestEmptyHomeIsNotTheCurrentDirectory(t *testing.T) {
	cwd := t.TempDir()
	put(t, filepath.Join(cwd, Dir, ConfigFile), `{"forge":{"kind":"gitlab"}}`)
	t.Chdir(cwd)
	l, err := Open(Options{Home: func() (string, error) { return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := l.Get(KeyForgeKind); ok && v.Scope == ScopeGlobal {
		t.Errorf("forge.kind = %+v from the current directory", v)
	}
}

// update.auto is looked up in the global file alone, so Get agrees with UpdateAuto.
func TestUpdateAutoLookupGlobalOnly(t *testing.T) {
	e := newEnv(t)
	e.local(t, `{"update":{"auto":"off"}}`)
	e.global(t, `{"update":{"auto":"install"},"repos":{"github.com/acme/rig":{"update":{"auto":"off"}}}}`)
	l := open(t, e)
	v, _ := l.Get(KeyUpdateAuto)
	if v.Scope != ScopeGlobal || v.Value != l.UpdateAuto() || v.Value == "off" {
		t.Errorf("update.auto = %+v, UpdateAuto = %q", v, l.UpdateAuto())
	}

	e = newEnv(t)
	e.global(t, `{"repos":{"github.com/acme/rig":{"update":{"auto":"install"}}}}`)
	if v, _ := open(t, e).Get(KeyUpdateAuto); v.Value != "off" || v.Scope != ScopeDefault {
		t.Errorf("repos only = %+v", v)
	}
	e = newEnv(t)
	e.local(t, `{"update":{"auto":"install"}}`)
	if v, _ := open(t, e).Get(KeyUpdateAuto); v.Value != "off" || v.Scope != ScopeDefault {
		t.Errorf("local only = %+v", v)
	}
}
