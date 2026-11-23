package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/secret"
)

const marker = "MARKER-TOKEN-7f3a9c"

// memStore is an in-memory secret.Store.
type memStore struct {
	mu sync.Mutex
	m  map[string]secret.Secret
}

func (s *memStore) Get(_ context.Context, a string) (secret.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[a]
	if !ok {
		return secret.Secret{}, secret.ErrNotFound
	}
	return v, nil
}

func (s *memStore) Set(_ context.Context, a string, v secret.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[a] = v
	return nil
}

func (s *memStore) Delete(_ context.Context, a string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, a)
	return nil
}

// loginEnv is an app wired to in-memory stores, a recording interactive
// runner and a temp home.
type loginEnv struct {
	t        *testing.T
	a        *app
	out, err bytes.Buffer
	home     string
	repo     string
	stores   map[string]*memStore
	ran      [][]string // every interactive call: name then args
	ranCode  int
	envVars  map[string]string
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	repo, _ := newRepo(t)
	gitc(t, repo, "remote", "set-url", "origin", "git@github.com:acme/rig.git")
	home := t.TempDir()
	e := &loginEnv{t: t, repo: repo, home: home, envVars: map[string]string{"HOME": home, "USERPROFILE": home},
		stores: map[string]*memStore{storeKeychain: {m: map[string]secret.Secret{}}, storeFile: {m: map[string]secret.Secret{}}}}
	e.a = &app{
		stdout: &e.out, stderr: &e.err, cwd: repo,
		getenv:     func(k string) string { return e.envVars[k] },
		secrets:    func(kind string) (secret.Store, error) { return e.stores[kind], nil },
		httpClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, // test servers only
		interactive: func(_ context.Context, _ proc.Options, name string, args ...string) (int, error) {
			e.ran = append(e.ran, append([]string{name}, args...))
			return e.ranCode, nil
		},
	}
	return e
}

// globalConfig writes ~/.rig/config.json directly: `config set` insists on
// an https URL, and the test server is plain http on loopback.
func (e *loginEnv) globalConfig(v map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Join(e.home, ".rig"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, ".rig", "config.json"), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *loginEnv) jiraConfig(url string) {
	e.t.Helper()
	e.globalConfig(map[string]any{"tracker": map[string]any{"kind": "jira", "url": url, "project": "RIG"}})
}

// stdin gives the app a non-terminal stdin holding text.
func (e *loginEnv) stdin(text string) {
	e.t.Helper()
	f, err := os.Create(filepath.Join(e.t.TempDir(), "stdin"))
	if err != nil {
		e.t.Fatal(err)
	}
	f.WriteString(text)
	f.Seek(0, 0)
	e.t.Cleanup(func() { f.Close() })
	e.a.stdin = f
}

func (e *loginEnv) run(args ...string) int {
	e.t.Helper()
	e.out.Reset()
	e.err.Reset()
	return e.a.run(ctx, args)
}

// stubTool puts a gh or glab on PATH that logs its argv to a file and reports
// logged in or out for `auth status`.
func stubTool(t *testing.T, name string, loggedIn bool) (argvLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs need /bin/sh")
	}
	bin := t.TempDir()
	argvLog = filepath.Join(t.TempDir(), "argv")
	status := "exit 1"
	if loggedIn {
		status = "echo 'Logged in to h account octocat (keyring)'; exit 0"
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + argvLog + "'\ncase \"$*\" in\n\"auth status\"*) " + status + " ;;\n*) exit 99 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog
}

// jiraServer answers /myself with status, requiring user and marker token on 200.
func jiraServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginGitHubLoggedInRunsNoLogin(t *testing.T) {
	argv := stubTool(t, "gh", true)
	e := newLoginEnv(t)
	if code := e.run("login", "github"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if len(e.ran) != 0 {
		t.Fatalf("ran %v", e.ran)
	}
	if !strings.Contains(e.out.String(), "Already logged in to github.com as octocat") {
		t.Fatalf("out: %q", e.out.String())
	}
	if b, _ := os.ReadFile(argv); !strings.Contains(string(b), "auth status --hostname github.com") {
		t.Fatalf("argv: %q", b)
	}
}

func TestLoginGitHubLoggedOutRunsLogin(t *testing.T) {
	stubTool(t, "gh", false)
	e := newLoginEnv(t)
	if code := e.run("login", "github"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	want := []string{"gh", "auth", "login", "--hostname", "github.com"}
	if len(e.ran) != 1 || strings.Join(e.ran[0], " ") != strings.Join(want, " ") {
		t.Fatalf("ran %v", e.ran)
	}
	e.ranCode = 3
	if code := e.run("login", "github"); code != 1 {
		t.Fatalf("tool failure: exit %d", code)
	}
}

func TestLoginGitLabUsesGlabAndHost(t *testing.T) {
	stubTool(t, "glab", false)
	e := newLoginEnv(t)
	gitc(t, e.repo, "remote", "set-url", "origin", "git@gitlab.example.com:o/r.git")
	if code := e.run("login", "gitlab"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if len(e.ran) != 1 || strings.Join(e.ran[0], " ") != "glab auth login --hostname gitlab.example.com" {
		t.Fatalf("ran %v", e.ran)
	}
}

func TestLoginJiraStoresOn200(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--url", srv.URL, "--user", "me@x.io"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	got, err := e.stores[storeKeychain].Get(ctx, "jira:"+host)
	if err != nil || got.User != "me@x.io" || got.Token != marker {
		t.Fatalf("stored %+v, %v", got, err)
	}
	if len(e.stores[storeFile].m) != 0 {
		t.Fatal("file store written")
	}
}

func TestLoginJiraReadsUserThenTokenFromPipe(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.stdin("me@x.io\n" + marker + "\n")
	if code := e.run("login", "jira", "--url", srv.URL); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	got, _ := e.stores[storeKeychain].Get(ctx, "jira:"+strings.TrimPrefix(srv.URL, "https://"))
	if got.User != "me@x.io" || got.Token != marker {
		t.Fatalf("stored %+v", got)
	}
}

func TestLoginJiraRejectedStoresNothing(t *testing.T) {
	for _, status := range []int{401, 403} {
		srv := jiraServer(t, status)
		e := newLoginEnv(t)
		e.stdin(marker + "\n")
		if code := e.run("login", "jira", "--url", srv.URL, "--user", "me@x.io"); code != 1 {
			t.Fatalf("%d: exit %d", status, code)
		}
		if len(e.stores[storeKeychain].m)+len(e.stores[storeFile].m) != 0 {
			t.Fatalf("%d: stored something", status)
		}
	}
	srv := jiraServer(t, 500)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--url", srv.URL, "--user", "me@x.io"); code != 2 {
		t.Fatalf("500: exit %d", code)
	}
}

func TestLoginJiraInputErrors(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.stdin("")
	if code := e.run("login", "jira", "--url", srv.URL, "--user", "u"); code != 2 {
		t.Fatalf("empty token: exit %d", code)
	}
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--user", "u"); code != 2 { // no site anywhere
		t.Fatalf("no site: exit %d", code)
	}
	for _, args := range [][]string{
		{"login", "jira", "--token", "x"}, {"login", "nope"}, {"login", "github", "--url", "x"},
		{"login", "jira", "--store", "disk"}, {"login", "--json"}, {"logout", "jira", "--json"},
	} {
		e.stdin(marker + "\n")
		if code := e.run(args...); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	if len(e.stores[storeKeychain].m) != 0 {
		t.Fatal("stored something")
	}
}

func TestLoginJiraSiteFromConfigAndRepoScope(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.globalConfig(map[string]any{"tracker": map[string]any{"url": srv.URL}})
	cfgBefore := fileSnapshot(t, e.home, e.repo)

	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--user", "me@x.io", "--repo"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	if _, err := e.stores[storeKeychain].Get(ctx, "jira:"+host+":github.com/acme/rig"); err != nil {
		t.Fatalf("repo-keyed secret missing: %v", err)
	}
	if _, err := e.stores[storeKeychain].Get(ctx, "jira:"+host); err == nil {
		t.Fatal("global secret written by --repo")
	}
	if after := fileSnapshot(t, e.home, e.repo); after != cfgBefore {
		t.Fatalf("login changed files:\nbefore %s\nafter  %s", cfgBefore, after)
	}

	// Outside a repository, --repo is exit 2.
	e.a.cwd = t.TempDir()
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--user", "me@x.io", "--repo"); code != 2 {
		t.Fatalf("--repo outside a repo: exit %d", code)
	}
}

// fileSnapshot lists every file under dirs with its size, for a before/after check.
func fileSnapshot(t *testing.T, dirs ...string) string {
	t.Helper()
	var sb strings.Builder
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			if strings.Contains(p, string(filepath.Separator)+".git"+string(filepath.Separator)) {
				return nil
			}
			b, _ := os.ReadFile(p)
			sb.WriteString(p + ":" + string(b) + "\n")
			return nil
		})
	}
	return sb.String()
}

func TestLoginStoreFile(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	code := e.run("login", "jira", "--url", srv.URL, "--user", "u", "--store", "file")
	if runtime.GOOS == "windows" {
		if code != 2 {
			t.Fatalf("windows: exit %d", code)
		}
		return
	}
	if code != 0 || len(e.stores[storeFile].m) != 1 || len(e.stores[storeKeychain].m) != 0 {
		t.Fatalf("exit %d, stores %v %v", code, e.stores[storeFile].m, e.stores[storeKeychain].m)
	}
}

func TestLoginLeaksNoToken(t *testing.T) {
	argv := stubTool(t, "gh", false)
	for _, status := range []int{200, 401, 500} {
		srv := jiraServer(t, status)
		e := newLoginEnv(t)
		e.a.cwd = e.repo
		e.stdin(marker + "\n")
		var seen []string
		e.a.interactive = func(_ context.Context, _ proc.Options, name string, args ...string) (int, error) {
			seen = append(seen, name+" "+strings.Join(args, " "))
			return 0, nil
		}
		e.jiraConfig(srv.URL)
		e.run("login", "--user", "me@x.io")
		out, errOut := e.out.String(), e.err.String()
		e.stdin(marker + "\n")
		e.run("login", "jira", "--user", "me@x.io", "--store", "file")
		out += e.out.String()
		errOut += e.err.String()
		e.envVars[jiraTokenEnv] = marker
		e.run("login", "status", "--json")
		e.run("login", "status")
		out += e.out.String()
		errOut += e.err.String()
		e.run("logout", "jira")
		out += e.out.String()
		errOut += e.err.String()
		if strings.Contains(out+errOut, marker) {
			t.Fatalf("%d: token in output:\n%s\n%s", status, out, errOut)
		}
		for _, s := range seen {
			if strings.Contains(s, marker) {
				t.Fatalf("token in argv: %s", s)
			}
		}
		if b, _ := os.ReadFile(argv); strings.Contains(string(b), marker) {
			t.Fatalf("token in stub argv: %s", b)
		}
		if snap := fileSnapshot(t, e.home, e.repo); strings.Contains(snap, marker) {
			t.Fatalf("token in a file:\n%s", snap)
		}
	}
}

func TestLogoutJira(t *testing.T) {
	e := newLoginEnv(t)
	e.stores[storeKeychain].m["jira:acme.atlassian.net"] = secret.Secret{User: "u", Token: marker}
	e.stores[storeFile].m["jira:acme.atlassian.net"] = secret.Secret{User: "u", Token: marker}
	e.stores[storeKeychain].m["jira:acme.atlassian.net:github.com/acme/rig"] = secret.Secret{User: "u", Token: marker}
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if len(e.stores[storeKeychain].m) != 1 {
		t.Fatalf("global login not removed alone: %v", e.stores[storeKeychain].m)
	}
	// Windows has no file store, so logout leaves a file entry alone there.
	if wantFile := 0; runtime.GOOS != "windows" && len(e.stores[storeFile].m) != wantFile {
		t.Fatal("file entry kept")
	} else if runtime.GOOS == "windows" && len(e.stores[storeFile].m) != 1 {
		t.Fatal("file entry touched on Windows")
	}
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net", "--repo"); code != 0 || len(e.stores[storeKeychain].m) != 0 {
		t.Fatalf("--repo: exit %d, %v", code, e.stores[storeKeychain].m)
	}
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 0 {
		t.Fatalf("already out: exit %d", code)
	}
}

func TestLogoutForgeIsExit2WithCommand(t *testing.T) {
	e := newLoginEnv(t)
	if code := e.run("logout", "github"); code != 2 || !strings.Contains(e.err.String(), "gh auth logout --hostname github.com") {
		t.Fatalf("exit %d: %q", code, e.err.String())
	}
	if code := e.run("logout", "gitlab"); code != 2 || !strings.Contains(e.err.String(), "glab auth logout --hostname gitlab.com") {
		t.Fatalf("exit %d: %q", code, e.err.String())
	}
	if len(e.ran) != 0 {
		t.Fatalf("ran %v", e.ran)
	}
}

func TestLoginNoServiceForgeThenJira(t *testing.T) {
	stubTool(t, "gh", false)
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)

	// tracker local: forge only.
	if code := e.run("login"); code != 0 || len(e.ran) != 1 {
		t.Fatalf("local: exit %d, ran %v\n%s", code, e.ran, e.err.String())
	}
	e.ran = nil

	e.jiraConfig(srv.URL)
	e.stdin("me@x.io\n" + marker + "\n")
	if code := e.run("login"); code != 0 || len(e.ran) != 1 {
		t.Fatalf("jira: exit %d, ran %v\n%s", code, e.ran, e.err.String())
	}
	if len(e.stores[storeKeychain].m) != 1 {
		t.Fatalf("jira login not stored: %v", e.stores[storeKeychain].m)
	}

	// Forge failure makes the whole login exit 1 though Jira worked.
	e.ranCode = 1
	e.stdin("me@x.io\n" + marker + "\n")
	if code := e.run("login"); code != 1 {
		t.Fatalf("forge failed: exit %d", code)
	}
}

func TestLoginStatusJSON(t *testing.T) {
	stubTool(t, "gh", true)
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.jiraConfig(srv.URL)
	host := strings.TrimPrefix(srv.URL, "https://")
	e.stores[storeKeychain].m["jira:"+host] = secret.Secret{User: "me@x.io", Token: marker}

	if code := e.run("login", "status", "--json"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	var got []loginStatus
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("json: %v\n%s", err, e.out.String())
	}
	if got[0] != (loginStatus{Service: "github", LoggedIn: true, User: "octocat"}) {
		t.Fatalf("forge: %+v", got[0])
	}
	if got[1] != (loginStatus{Service: "jira", LoggedIn: true, User: "me@x.io", Scope: "global", Store: "keychain"}) {
		t.Fatalf("jira: %+v", got[1])
	}

	// RIG_JIRA_TOKEN overrides the stored token at use time; status says env.
	e.envVars[jiraTokenEnv] = "other"
	e.run("login", "status", "--json")
	json.Unmarshal(e.out.Bytes(), &got)
	if got[1].Store != "env" || !got[1].LoggedIn {
		t.Fatalf("env: %+v", got[1])
	}
	if e.stores[storeKeychain].m["jira:"+host].Token != marker {
		t.Fatal("env token was stored")
	}

	// A rejected token is reported, not an exit code.
	bad := jiraServer(t, 401)
	e.jiraConfig(bad.URL)
	e.stores[storeKeychain].m["jira:"+strings.TrimPrefix(bad.URL, "https://")] = secret.Secret{User: "u", Token: "t"}
	delete(e.envVars, jiraTokenEnv)
	if code := e.run("login", "status"); code != 0 || !strings.Contains(e.out.String(), "jira: not logged in") {
		t.Fatalf("exit %d\n%s", code, e.out.String())
	}
}

func TestLoginJiraNamesSiteAndSourceBeforePrompt(t *testing.T) {
	srv := jiraServer(t, 200)
	host := strings.TrimPrefix(srv.URL, "https://")
	e := newLoginEnv(t)
	e.stdin(marker + "\n")
	e.run("login", "jira", "--url", srv.URL, "--user", "u")
	if want := "Jira site: " + host + " (from --url)\n"; !strings.HasPrefix(e.err.String(), want) {
		t.Fatalf("stderr %q", e.err.String())
	}

	// A project-scoped tracker.url (a committed .rig/config.json) is named.
	b, _ := json.Marshal(map[string]any{"tracker": map[string]any{"url": srv.URL}})
	os.MkdirAll(filepath.Join(e.repo, ".rig"), 0o755)
	if err := os.WriteFile(filepath.Join(e.repo, ".rig", "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	e.stdin("u\n" + marker + "\n")
	e.run("login", "jira")
	if want := "Jira site: " + host + " (from tracker.url in local config)\n"; !strings.HasPrefix(e.err.String(), want) {
		t.Fatalf("stderr %q", e.err.String())
	}
}

func TestLogoutSkipsAStoreThatCannotOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no file store, so an unopenable keychain leaves nothing to delete (exit 2 by the fail-closed rule)")
	}
	e := newLoginEnv(t)
	e.a.secrets = func(kind string) (secret.Store, error) {
		if kind == storeKeychain {
			return nil, errors.New("no secret-tool")
		}
		return e.stores[kind], nil
	}
	e.stores[storeFile].m["jira:acme.atlassian.net"] = secret.Secret{User: "u", Token: marker}
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if !strings.Contains(e.err.String(), "skipped the keychain store") {
		t.Fatalf("stderr %q", e.err.String())
	}
	if len(e.stores[storeFile].m) != 0 {
		t.Fatal("file entry kept")
	}
}

// flakyStore is a memStore whose Get and Delete can be made to fail.
type flakyStore struct {
	memStore
	getErr, delErr error
}

func (s *flakyStore) Get(ctx context.Context, a string) (secret.Secret, error) {
	if s.getErr != nil {
		return secret.Secret{}, s.getErr
	}
	return s.memStore.Get(ctx, a)
}

func (s *flakyStore) Delete(ctx context.Context, a string) error {
	if s.delErr != nil {
		return s.delErr
	}
	return s.memStore.Delete(ctx, a)
}

const logoutAcct = "jira:acme.atlassian.net"

// logoutEnv wires a flaky keychain and the memory file store.
func logoutEnv(t *testing.T) (*loginEnv, *flakyStore) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no file store on windows")
	}
	e := newLoginEnv(t)
	kc := &flakyStore{memStore: memStore{m: map[string]secret.Secret{}}}
	e.a.secrets = func(kind string) (secret.Store, error) {
		if kind == storeKeychain {
			return kc, nil
		}
		return e.stores[kind], nil
	}
	return e, kc
}

func TestLogoutFailsWhenKeychainHoldsEntryAndDeleteFails(t *testing.T) {
	e, kc := logoutEnv(t)
	kc.m[logoutAcct] = secret.Secret{User: "u", Token: marker}
	kc.delErr = errors.New("secret-tool clear: no Secret Service")
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 2 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if strings.Contains(e.out.String(), "Logged out") {
		t.Fatalf("stdout %q", e.out.String())
	}
}

func TestLogoutUnreachableKeychainFileStoreDeletes(t *testing.T) {
	e, kc := logoutEnv(t)
	kc.getErr = errors.New("keychain locked")
	e.stores[storeFile].m[logoutAcct] = secret.Secret{User: "u", Token: marker}
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if !strings.Contains(e.err.String(), "keychain locked") || !strings.Contains(e.out.String(), "Logged out of acme.atlassian.net") {
		t.Fatalf("stderr %q stdout %q", e.err.String(), e.out.String())
	}
	if len(e.stores[storeFile].m) != 0 {
		t.Fatal("file entry kept")
	}
}

func TestLogoutUnreachableKeychainNothingElseFoundFails(t *testing.T) {
	e, kc := logoutEnv(t)
	kc.getErr = errors.New("keychain locked")
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 2 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if !strings.Contains(e.err.String(), "could not check the keychain store; no entry found in file") || strings.Contains(e.out.String(), "Logged out") {
		t.Fatalf("stderr %q stdout %q", e.err.String(), e.out.String())
	}
}

func TestLogoutNotLoggedInWhenBothStoresAbsent(t *testing.T) {
	e, _ := logoutEnv(t)
	if code := e.run("logout", "jira", "--url", "https://acme.atlassian.net"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if e.out.String() != "Not logged in to acme.atlassian.net\n" {
		t.Fatalf("stdout %q", e.out.String())
	}
}

func TestForgeTargetFor(t *testing.T) {
	remotes := map[string]string{
		"github": "git@github.com:o/r.git",
		"gitlab": "git@gitlab.com:o/r.git",
		"corp":   "git@git.corp.com:o/r.git",
		"none":   "",
	}
	cases := []struct {
		name, cfgKind, cfgHost, remote string
		want                           forgeTarget
	}{
		{"nothing, github remote", "", "", "github", forgeTarget{"gh", forge.GitHub, "github.com"}},
		{"nothing, gitlab remote", "", "", "gitlab", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"nothing, corp remote", "", "", "corp", forgeTarget{"gh", forge.GitHub, "git.corp.com"}},
		{"nothing, no remote", "", "", "none", forgeTarget{"gh", forge.GitHub, "github.com"}},
		{"host only, corp remote", "", "gitlab.com", "corp", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"host only, no remote", "", "git.corp.com", "none", forgeTarget{"gh", forge.GitHub, "git.corp.com"}},
		{"github kind, corp remote", "github", "", "corp", forgeTarget{"gh", forge.GitHub, "git.corp.com"}},
		{"github kind, gitlab remote", "github", "", "gitlab", forgeTarget{"gh", forge.GitHub, "github.com"}},
		{"github kind, no remote", "github", "", "none", forgeTarget{"gh", forge.GitHub, "github.com"}},
		{"github kind and host", "github", "ghe.corp.com", "github", forgeTarget{"gh", forge.GitHub, "ghe.corp.com"}},
		{"gitlab kind, corp remote", "gitlab", "", "corp", forgeTarget{"glab", forge.GitLab, "git.corp.com"}},
		{"gitlab kind, gitlab remote", "gitlab", "", "gitlab", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"gitlab kind, github remote", "gitlab", "", "github", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"gitlab kind, no remote", "gitlab", "", "none", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"gitlab kind and host", "gitlab", "gl.corp.com", "corp", forgeTarget{"glab", forge.GitLab, "gl.corp.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newLoginEnv(t)
			fc := map[string]any{}
			if c.cfgKind != "" {
				fc["kind"] = c.cfgKind
			}
			if c.cfgHost != "" {
				fc["host"] = c.cfgHost
			}
			e.globalConfig(map[string]any{"forge": fc})
			layers, _, _, err := e.a.openConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := forgeTargetFor(layers, remotes[c.remote], ""); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestForgeTargetForForcedService(t *testing.T) {
	remotes := map[string]string{"github": "git@github.com:o/r.git", "none": ""}
	cases := []struct {
		name, service, cfgKind, cfgHost, remote string
		want                                    forgeTarget
	}{
		{"github, gitlab-looking host, kind unset", "github", "", "gitlab.example.com", "none", forgeTarget{"gh", forge.GitHub, "github.com"}},
		{"gitlab, github.com host, kind unset", "gitlab", "", "github.com", "none", forgeTarget{"glab", forge.GitLab, "gitlab.com"}},
		{"github, unplaceable host, kind unset", "github", "", "git.corp.com", "none", forgeTarget{"gh", forge.GitHub, "git.corp.com"}},
		{"gitlab, gitlab host, kind unset", "gitlab", "", "gitlab.example.com", "github", forgeTarget{"glab", forge.GitLab, "gitlab.example.com"}},
		{"github, gitlab-looking host, kind github", "github", "github", "gitlab.example.com", "none", forgeTarget{"gh", forge.GitHub, "gitlab.example.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newLoginEnv(t)
			fc := map[string]any{"host": c.cfgHost}
			if c.cfgKind != "" {
				fc["kind"] = c.cfgKind
			}
			e.globalConfig(map[string]any{"forge": fc})
			layers, _, _, err := e.a.openConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := forgeTargetFor(layers, remotes[c.remote], c.service); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLogoutPointsToRepoLoginWhenOnlyRepoEntryExists(t *testing.T) {
	e := newLoginEnv(t)
	srv := jiraServer(t, 200)
	e.globalConfig(map[string]any{"tracker": map[string]any{"url": srv.URL}})
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--user", "me@x.io", "--repo"); code != 0 {
		t.Fatalf("login: exit %d\n%s", code, e.err.String())
	}
	if code := e.run("logout", "jira"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if strings.Contains(e.out.String()+e.err.String(), "Not logged in") || !strings.Contains(e.out.String()+e.err.String(), "rig logout jira --repo") {
		t.Fatalf("stdout %q stderr %q", e.out.String(), e.err.String())
	}
	if len(e.stores[storeKeychain].m) != 1 {
		t.Fatalf("repo entry not kept: %v", e.stores[storeKeychain].m)
	}
}

// localTrackerURL writes <repo>/.rig/config.json with tracker.url set.
func (e *loginEnv) localTrackerURL(url string) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"tracker": map[string]any{"url": url}})
	if err := os.MkdirAll(filepath.Join(e.repo, ".rig"), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.repo, ".rig", "config.json"), b, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestLoginJiraRefusesTrackedLocalTrackerURL(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.localTrackerURL(srv.URL)
	gitc(t, e.repo, "add", ".rig/config.json")
	e.stdin("u\n" + marker + "\n")
	if code := e.run("login", "jira"); code != 2 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if !strings.Contains(e.err.String(), "which is committed to this repository") || strings.Contains(e.err.String(), "Jira site:") {
		t.Fatalf("stderr %q", e.err.String())
	}
	if len(e.stores[storeKeychain].m) != 0 {
		t.Fatal("token stored")
	}
	if b, _ := io.ReadAll(e.a.stdin); !strings.Contains(string(b), marker) {
		t.Fatal("stdin was read before the refusal")
	}
	// --url overrides the tracked file.
	e.stdin(marker + "\n")
	if code := e.run("login", "jira", "--url", srv.URL, "--user", "u"); code != 0 {
		t.Fatalf("--url: exit %d\n%s", code, e.err.String())
	}
}

// refuseTrackerBypass asserts login jira refuses before any prompt or token read.
func refuseTrackerBypass(t *testing.T, e *loginEnv) {
	t.Helper()
	e.stdin("u\n" + marker + "\n")
	if code := e.run("login", "jira"); code != 2 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if !strings.Contains(e.err.String(), "which is committed to this repository") || strings.Contains(e.err.String(), "Jira site:") {
		t.Fatalf("stderr %q", e.err.String())
	}
	if len(e.stores[storeKeychain].m) != 0 {
		t.Fatal("token stored")
	}
	if b, _ := io.ReadAll(e.a.stdin); !strings.Contains(string(b), marker) {
		t.Fatal("stdin was read before the refusal")
	}
}

func TestLoginJiraRefusesTrackedDirCaseVariant(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.localTrackerURL(srv.URL)
	up, low := filepath.Join(e.repo, ".RIG"), filepath.Join(e.repo, ".rig")
	if err := os.Rename(low, up); err != nil {
		t.Fatal(err)
	}
	gitc(t, e.repo, "add", ".RIG/config.json")
	if err := os.Rename(up, low); err != nil {
		t.Fatal(err)
	}
	refuseTrackerBypass(t, e)
}

func TestLoginJiraRefusesTrackedFileCaseVariant(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.localTrackerURL(srv.URL)
	dir := filepath.Join(e.repo, ".rig")
	if err := os.Rename(filepath.Join(dir, "config.json"), filepath.Join(dir, "Config.json")); err != nil {
		t.Fatal(err)
	}
	gitc(t, e.repo, "add", ".rig/Config.json")
	if err := os.Rename(filepath.Join(dir, "Config.json"), filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	refuseTrackerBypass(t, e)
}

func TestLoginJiraRefusesSymlinkedRigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	b, _ := json.Marshal(map[string]any{"tracker": map[string]any{"url": srv.URL}})
	if err := os.MkdirAll(filepath.Join(e.repo, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.repo, "real", "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, e.repo, "add", "real/config.json")
	if err := os.Symlink("real", filepath.Join(e.repo, ".rig")); err != nil {
		t.Fatal(err)
	}
	refuseTrackerBypass(t, e)
}

func TestLoginJiraRefusesSymlinkedConfigFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	b, _ := json.Marshal(map[string]any{"tracker": map[string]any{"url": srv.URL}})
	if err := os.WriteFile(filepath.Join(e.repo, "elsewhere.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.repo, ".rig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "elsewhere.json"), filepath.Join(e.repo, ".rig", "config.json")); err != nil {
		t.Fatal(err)
	}
	refuseTrackerBypass(t, e)
}

func TestLoginJiraRefusesTrackedGitlinkAtRig(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.localTrackerURL(srv.URL)
	gitc(t, e.repo, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",.rig")
	refuseTrackerBypass(t, e)
}

func TestLoginJiraAcceptsUntrackedLocalTrackerURL(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	e.localTrackerURL(srv.URL)
	e.stdin("u\n" + marker + "\n")
	if code := e.run("login", "jira"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
}

func TestLoginJiraTrackedLocalFileDoesNotAffectGlobalURL(t *testing.T) {
	srv := jiraServer(t, 200)
	e := newLoginEnv(t)
	// A tracked local file that does not set tracker.url.
	b, _ := json.Marshal(map[string]any{"tracker": map[string]any{"project": "RIG"}})
	os.MkdirAll(filepath.Join(e.repo, ".rig"), 0o755)
	if err := os.WriteFile(filepath.Join(e.repo, ".rig", "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	gitc(t, e.repo, "add", ".rig/config.json")
	e.jiraConfig(srv.URL)
	e.stdin("u\n" + marker + "\n")
	if code := e.run("login", "jira"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
}

func TestLoginForgeNamesHostBeforeAuthLogin(t *testing.T) {
	stubTool(t, "gh", false)
	e := newLoginEnv(t)
	gitc(t, e.repo, "remote", "set-url", "origin", "git@evil.example.net:o/r.git")
	e.globalConfig(map[string]any{"forge": map[string]any{"kind": "github"}})
	if code := e.run("login", "github"); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.err.String())
	}
	if want := "gh login for evil.example.net (from the origin remote)\n"; !strings.Contains(e.err.String(), want) {
		t.Fatalf("stderr %q", e.err.String())
	}
	e.globalConfig(map[string]any{"forge": map[string]any{"kind": "github", "host": "ghe.corp.com"}})
	e.run("login", "github")
	if want := "gh login for ghe.corp.com (from forge.host in global config)\n"; !strings.Contains(e.err.String(), want) {
		t.Fatalf("stderr %q", e.err.String())
	}
	gitc(t, e.repo, "remote", "set-url", "origin", "git@gitlab.com:o/r.git")
	e.globalConfig(map[string]any{})
	e.run("login", "github")
	if want := "gh login for github.com (from default)\n"; !strings.Contains(e.err.String(), want) {
		t.Fatalf("stderr %q", e.err.String())
	}
}
