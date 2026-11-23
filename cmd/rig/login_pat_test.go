package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/AindriuB/rig/internal/proc"
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
