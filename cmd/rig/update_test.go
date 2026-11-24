package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AindriuB/rig/internal/selfupdate"
)

type updRT func(*http.Request) (*http.Response, error)

func (f updRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func updApp(t *testing.T, exe string, res selfupdate.Result, got *selfupdate.Options) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, getenv: func(string) string { return "" }}
	a.exe = func() (string, error) { return exe, nil }
	a.updater = func(_ context.Context, o selfupdate.Options) selfupdate.Result {
		if got != nil {
			*got = o
		}
		return res
	}
	return a, &out, &errOut
}

func TestUpdateOutcomeExitCodes(t *testing.T) {
	for _, c := range []struct {
		action, msg string
		code        int
	}{
		{selfupdate.ActionUpdated, "rig updated 0.5.1 -> 0.5.2 (/x/rig)", 0},
		{selfupdate.ActionUpToDate, "rig 0.5.1 is up to date", 0},
		{selfupdate.ActionRefused, "latest 0.5.0 is older than 0.5.1", 1},
		{selfupdate.ActionAvailable, "rig 0.5.2 is available", 1},
		{selfupdate.ActionFailed, "signature check failed", 2},
	} {
		a, out, _ := updApp(t, "/x/rig", selfupdate.Result{Action: c.action, Message: c.msg}, nil)
		if code := a.run(context.Background(), []string{"update"}); code != c.code {
			t.Errorf("%s: exit %d, want %d", c.action, code, c.code)
		}
		if strings.TrimSpace(out.String()) != c.msg {
			t.Errorf("%s: stdout %q", c.action, out.String())
		}
	}
}

func TestUpdateFlagsReachUpdater(t *testing.T) {
	var got selfupdate.Options
	a, _, _ := updApp(t, "/x/rig", selfupdate.Result{Action: selfupdate.ActionUpToDate}, &got)
	if code := a.run(context.Background(), []string{"update", "--check", "--to", "v0.5.2"}); code != 0 {
		t.Fatal(code)
	}
	if !got.Check || got.To != "v0.5.2" || got.Exe != "/x/rig" || got.Current != version {
		t.Fatalf("%+v", got)
	}
	for _, bad := range [][]string{{"update", "extra"}, {"update", "--bogus"}, {"update", "--to"}} {
		a, _, _ := updApp(t, "/x/rig", selfupdate.Result{}, nil)
		if code := a.run(context.Background(), bad); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestUpdateJSON(t *testing.T) {
	for _, c := range []struct {
		res  selfupdate.Result
		want string
	}{
		{selfupdate.Result{Current: "0.5.1", Target: "0.5.2", Action: "updated", Path: "/x/rig"},
			`{"current":"0.5.1","target":"0.5.2","action":"updated","path":"/x/rig"}`},
		{selfupdate.Result{Current: "0.5.2", Target: "0.5.2", Action: "up-to-date", Path: "/x/rig"},
			`{"current":"0.5.2","target":"0.5.2","action":"up-to-date","path":"/x/rig"}`},
		{selfupdate.Result{Current: "0.5.1", Target: "0.5.2", Action: "failed", Path: "/x/rig", Check: "signature"},
			`{"current":"0.5.1","target":"0.5.2","action":"failed","path":"/x/rig","check":"signature"}`},
	} {
		a, out, _ := updApp(t, "/x/rig", c.res, nil)
		a.run(context.Background(), []string{"update", "--json"})
		if strings.TrimSpace(out.String()) != c.want {
			t.Errorf("got %s\nwant %s", out.String(), c.want)
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Error(err)
		}
	}
}

// A failed signature through the real updater and a fake release server leaves
// the installed file byte-identical.
func TestUpdateBadSignatureLeavesInstalled(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rig")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	const base = "https://github.com/AindriuB/rig/releases/download/v9.9.9/"
	var out bytes.Buffer
	a := &app{stdout: &out, stderr: io.Discard, getenv: func(string) string { return "" }}
	a.exe = func() (string, error) { return exe, nil }
	a.updateTransport = updRT(func(r *http.Request) (*http.Response, error) {
		body := "junk"
		if strings.HasSuffix(r.URL.Path, "/latest") {
			body = `{"tag_name":"v9.9.9","assets":[
{"name":"rig-` + osArch() + `","browser_download_url":"` + base + `rig-` + osArch() + `"},
{"name":"SHA256SUMS","browser_download_url":"` + base + `SHA256SUMS"},
{"name":"SHA256SUMS.sig","browser_download_url":"` + base + `SHA256SUMS.sig"}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if code := a.run(context.Background(), []string{"update", "--json"}); code != 2 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	var m map[string]string
	if err := json.Unmarshal(out.Bytes(), &m); err != nil || m["action"] != "failed" || m["check"] != "signature" {
		t.Fatalf("%v %s", err, out.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("installed %q", b)
	}
}

func osArch() string {
	return strings.TrimPrefix(selfupdate.AssetName(runtime.GOOS, runtime.GOARCH), "rig-")
}

func TestUpdateHelpListsIt(t *testing.T) {
	a, out, _ := updApp(t, "", selfupdate.Result{}, nil)
	if a.run(context.Background(), []string{"help"}) != 0 || !strings.Contains(out.String(), "rig update [--check] [--to <version>] [--json]") {
		t.Fatal("help does not list rig update")
	}
}

func TestUpdateCleanupOldIsQuiet(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rig")
	os.WriteFile(exe, []byte("x"), 0o755)
	os.WriteFile(exe+".old", []byte("x"), 0o644)
	cleanupOld(func() (string, error) { return exe, nil })
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old left")
	}
	cleanupOld(func() (string, error) { return "", errors.New("no exe") }) // must not panic
}
