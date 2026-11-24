package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/secret"
)

// patServer answers GET /rest/api/2/myself 200 for the marker Bearer token and
// 401 otherwise; any other path is recorded as a miss.
func patServer(t *testing.T) (srv *httptest.Server, paths func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")[:min(6, len(r.Header.Get("Authorization")))])
		mu.Unlock()
		if r.URL.Path != "/rest/api/2/myself" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+marker {
			w.WriteHeader(401)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

// AC8: --pat stores a token with no user, after a v2 myself check.
func TestLoginJiraPATStoresWithoutUser(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--pat", "--url", srv.URL); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	got, err := e.stores[storeKeychain].Get(ctx, "jira:"+host)
	if err != nil || got.User != "" || got.Token != marker {
		t.Fatalf("stored %+v, %v", got, err)
	}
	if p := paths(); len(p) != 1 || p[0] != "/rest/api/2/myself Bearer" {
		t.Fatalf("requests %v", p)
	}
	if strings.Contains(e.out.String()+e.err.String(), marker) {
		t.Fatal("token printed")
	}
}

func TestLoginJiraPATRepoScope(t *testing.T) {
	srv, _ := patServer(t)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--pat", "--repo", "--url", srv.URL); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	if _, err := e.stores[storeKeychain].Get(ctx, "jira:"+host+":github.com/acme/rig"); err != nil {
		t.Fatalf("repo-keyed secret missing: %v", err)
	}
}

func TestLoginJiraPATRejectedStoresNothing(t *testing.T) {
	srv, _ := patServer(t)
	e := newLoginEnv(t)
	e.stdin("WRONG-TOKEN\n")
	if code := e.run("login", "jira", "--pat", "--url", srv.URL); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(e.stores[storeKeychain].m)+len(e.stores[storeFile].m) != 0 {
		t.Fatal("stored something")
	}
}

func TestLoginJiraPATInputErrors(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	for _, args := range [][]string{
		{"login", "jira", "--pat", "--user", "me@x.io", "--url", srv.URL},
		{"login", "github", "--pat"},
		{"logout", "jira", "--pat", "--url", srv.URL},
	} {
		e.stdin(marker + "\n")
		if code := e.run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if len(paths()) != 0 || len(e.stores[storeKeychain].m) != 0 {
		t.Fatal("a refused command reached the network or the store")
	}
}

// phase-3.md: gh and glab run with the RIG_*_TOKEN variables scrubbed.
func TestLoginForgeToolsScrubTokens(t *testing.T) {
	e := newLoginEnv(t)
	var opts []proc.Options
	e.a.interactive = func(_ context.Context, o proc.Options, _ string, _ ...string) (int, error) {
		opts = append(opts, o)
		return 0, nil
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs need /bin/sh")
	}
	bin := t.TempDir()
	dump := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\necho \"tok=[$RIG_JIRA_TOKEN]\" > '" + dump + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RIG_JIRA_TOKEN", "LEAK")
	if code := e.run("login", "github"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if b, _ := os.ReadFile(dump); string(b) != "tok=[]\n" {
		t.Errorf("auth status saw env %q", b)
	}
	if len(opts) != 1 || !opts[0].ScrubTokens {
		t.Errorf("auth login options %+v", opts)
	}
}

// jiraStatusOf runs `rig login status --json` and returns the jira entry.
func jiraStatusOf(t *testing.T, e *loginEnv) loginStatus {
	t.Helper()
	stubTool(t, "gh", true)
	if code := e.run("login", "status", "--json"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	var got []loginStatus
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("json: %v\n%s", err, e.out.String())
	}
	return got[1]
}

func TestLoginStatusStoredPATChecksV2WithBearer(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	e.jiraConfig(srv.URL)
	e.stores[storeKeychain].m["jira:"+strings.TrimPrefix(srv.URL, "https://")] = secret.Secret{Token: marker}

	got := jiraStatusOf(t, e)
	if got != (loginStatus{Service: "jira", LoggedIn: true, Scope: "global", Store: "keychain"}) {
		t.Fatalf("jira: %+v", got)
	}
	if p := paths(); len(p) != 1 || p[0] != "/rest/api/2/myself Bearer" {
		t.Fatalf("requests %v", p)
	}
}

// With a stored PAT, RIG_JIRA_TOKEN replaces the token and the login stays a
// PAT: the env value is sent as a Bearer credential to the v2 API.
func TestLoginStatusEnvTokenOverStoredPATIsCheckedAsBearer(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	e.jiraConfig(srv.URL)
	e.stores[storeKeychain].m["jira:"+strings.TrimPrefix(srv.URL, "https://")] = secret.Secret{Token: "stored-stale"}
	e.envVars[jiraTokenEnv] = marker

	got := jiraStatusOf(t, e)
	if !got.LoggedIn || got.Store != "env" || got.User != "" || got.Error != "" {
		t.Fatalf("jira: %+v", got)
	}
	if p := paths(); len(p) != 1 || p[0] != "/rest/api/2/myself Bearer" {
		t.Fatalf("requests %v", p)
	}
}

// RIG_JIRA_TOKEN alone is not enough: there is no stored user, so status
// reports the error without calling the site.
func TestLoginStatusEnvTokenWithNothingStoredIsAnError(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	e.jiraConfig(srv.URL)
	e.envVars[jiraTokenEnv] = marker

	got := jiraStatusOf(t, e)
	if got.LoggedIn || got.Store != "env" || !strings.Contains(got.Error, jiraTokenEnv+" is set but no user is stored") {
		t.Fatalf("jira: %+v", got)
	}
	if p := paths(); len(p) != 0 {
		t.Fatalf("requests %v", p)
	}
}

// Round 3: --token is the documented switch; --pat stays as an alias.
func TestLoginJiraTokenStoresWithoutUser(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--token", "--pat", "--url", srv.URL); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	got, err := e.stores[storeKeychain].Get(ctx, "jira:"+host)
	if err != nil || got.User != "" || got.Token != marker {
		t.Fatalf("stored %+v, %v", got, err)
	}
	if p := paths(); len(p) != 1 || p[0] != "/rest/api/2/myself Bearer" {
		t.Fatalf("requests %v", p)
	}
}

func TestLoginTokenNeverTakesAValue(t *testing.T) {
	srv, paths := patServer(t)
	e := newLoginEnv(t)
	for _, args := range [][]string{
		{"login", "jira", "--token=" + marker, "--url", srv.URL},
		{"login", "jira", "--token", marker, "--url", srv.URL},
		{"login", "jira", "--url", srv.URL, "--token", marker},
		{"login", "jira", "--pat=" + marker, "--url", srv.URL},
		{"login", "jira", "--pat", marker, "--url", srv.URL},
		{"logout", "jira", "--token=" + marker, "--url", srv.URL},
	} {
		e.out.Reset()
		e.err.Reset()
		e.stdin(marker + "\n")
		if code := e.run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
		msg := e.err.String()
		if !strings.Contains(msg, "prompts for the token") || !strings.Contains(msg, "command line") {
			t.Errorf("%v: message %q", args, msg)
		}
		if strings.Contains(msg+e.out.String(), marker) {
			t.Errorf("%v: token echoed", args)
		}
	}
	if len(paths()) != 0 || len(e.stores[storeKeychain].m)+len(e.stores[storeFile].m) != 0 {
		t.Fatal("a refused command reached the network or the store")
	}
}

// 051 item 2: no argument error echoes a possible secret.
func TestArgumentErrorsNeverEchoSecret(t *testing.T) {
	e := newLoginEnv(t)
	for _, args := range [][]string{
		{"login", "SECRETX", "--token"},
		{"login", "jira", "--token", "-SECRETX"},
		{"login", "jira", "--Token=SECRETX"},
		{"login", "jira", "--SECRETX!=1"},
		{"login", "jira", "-SECRETX!"},
		{"logout", "SECRETX"},
		{"logout", "jira", "--Token=SECRETX"},
	} {
		e.out.Reset()
		e.err.Reset()
		if code := e.run(args...); code == 0 {
			t.Errorf("%v: exit 0", args)
		}
		if got := e.out.String() + e.err.String(); strings.Contains(got, "SECRETX") {
			t.Errorf("%v: secret echoed: %q", args, got)
		}
	}
	e.err.Reset()
	e.run("login", "jira", "--task=SECRETX")
	if got := e.err.String(); !strings.Contains(got, `"--task"`) {
		t.Errorf("option name not shown: %q", got)
	}
	e.err.Reset()
	e.run("login", "jira", "-SECRETX!")
	if got := e.err.String(); !strings.Contains(got, "unknown option (not shown)") {
		t.Errorf("odd option shown: %q", got)
	}
	e.err.Reset()
	e.run("login", "SECRETX")
	if got := e.err.String(); !strings.Contains(got, "github, gitlab, jira or status") {
		t.Errorf("services not listed: %q", got)
	}
}

// 051 item 6: the no-service message names --token, not --pat.
func TestLoginNoServiceMessageNamesToken(t *testing.T) {
	e := newLoginEnv(t)
	if code := e.run("login", "--token"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if msg := e.err.String(); !strings.Contains(msg, "--url, --user, --repo, --store and --token apply to jira, which tracker.kind does not select") {
		t.Fatalf("message %q", msg)
	}
}

func TestLoginTokenMessagesNameToken(t *testing.T) {
	srv, _ := patServer(t)
	e := newLoginEnv(t)
	for _, args := range [][]string{
		{"login", "jira", "--token", "--user", "me@x.io", "--url", srv.URL},
		{"login", "github", "--token"},
	} {
		e.err.Reset()
		if code := e.run(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
		if msg := e.err.String(); !strings.Contains(msg, "--token") || strings.Contains(msg, "--pat") {
			t.Errorf("%v: message %q", args, msg)
		}
	}
}
