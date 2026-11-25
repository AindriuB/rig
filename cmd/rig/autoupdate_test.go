package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AindriuB/rig/internal/selfupdate"
)

type autoRig struct {
	a        *app
	out, err *bytes.Buffer
	home     string
	calls    []selfupdate.AutoOptions
	order    []string
	reqs     int
}

// newAutoRig builds an app whose global config holds update.auto = mode ("" for none).
func newAutoRig(t *testing.T, mode string, env map[string]string) *autoRig {
	t.Helper()
	r := &autoRig{out: &bytes.Buffer{}, err: &bytes.Buffer{}, home: t.TempDir()}
	if mode != "" {
		r.writeGlobal(t, `{"update":{"auto":"`+mode+`"}}`)
	}
	r.a = &app{stdout: r.out, stderr: r.err, now: func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) }}
	r.a.getenv = func(k string) string {
		if k == "HOME" || k == "USERPROFILE" {
			return r.home
		}
		return env[k]
	}
	r.a.cwd = t.TempDir()
	r.a.exe = func() (string, error) { return filepath.Join(r.home, "rig"), nil }
	r.a.stderrTTY = func() bool { return true }
	r.a.autoRun = func(_ context.Context, o selfupdate.AutoOptions) {
		r.order = append(r.order, "auto")
		r.calls = append(r.calls, o)
	}
	return r
}

func (r *autoRig) writeGlobal(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r.home, ".rig"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, ".rig", "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAutoUpdateGatingRows(t *testing.T) {
	cases := []struct {
		name string
		mode string
		env  map[string]string
		tty  bool
		args []string
		want bool // the pass runs
	}{
		{"unset", "", nil, true, []string{"workflow", "show"}, false},
		{"off", "off", nil, true, []string{"workflow", "show"}, false},
		{"notify", "notify", nil, true, []string{"workflow", "show"}, true},
		{"install", "install", nil, true, []string{"workflow", "show"}, true},
		{"CI", "install", map[string]string{"CI": "true"}, true, []string{"workflow", "show"}, false},
		{"not a terminal", "install", nil, false, []string{"workflow", "show"}, false},
		{"json", "install", nil, true, []string{"workflow", "show", "--json"}, false},
		{"update", "install", nil, true, []string{"update", "--check"}, false},
		{"version", "install", nil, true, []string{"version"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newAutoRig(t, c.mode, c.env)
			r.a.stderrTTY = func() bool { return c.tty }
			ref := newAutoRig(t, "off", c.env)
			code := r.a.runAndAutoUpdate(context.Background(), c.args)
			refCode := ref.a.runAndAutoUpdate(context.Background(), c.args)
			if (len(r.calls) == 1) != c.want {
				t.Fatalf("pass ran %d times, want run=%v", len(r.calls), c.want)
			}
			if code != refCode || r.out.String() != ref.out.String() {
				t.Fatalf("exit/stdout differ from an off run: %d %q vs %d %q", code, r.out, refCode, ref.out)
			}
			if c.want && r.calls[0].Mode != c.mode {
				t.Fatalf("mode %q", r.calls[0].Mode)
			}
		})
	}
}

func TestAutoUpdateIgnoresRepoAndLocalFiles(t *testing.T) {
	r := newAutoRig(t, "", nil)
	r.writeGlobal(t, `{"repos":{"github.com/acme/rig":{"update":{"auto":"install"}}}}`)
	for _, p := range []string{".rig/config.json", ".claude/rig.json"} {
		path := filepath.Join(r.a.cwd, p)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(`{"update":{"auto":"install"}}`), 0o644)
	}
	r.a.runAndAutoUpdate(context.Background(), []string{"workflow", "show"})
	if len(r.calls) != 0 {
		t.Fatal("a repo or local file switched update.auto on")
	}
}

func TestAutoUpdateRunsOnlyAfterDispatch(t *testing.T) {
	r := newAutoRig(t, "notify", nil)
	// a command that records order: stdout is written during dispatch
	r.a.stdout = recorder(func() { r.order = append(r.order, "command") })
	r.a.runAndAutoUpdate(context.Background(), []string{"help"})
	if got := strings.Join(r.order, ","); got != "command,auto" {
		t.Fatalf("order %s", got)
	}
}

type recorder func()

func (f recorder) Write(p []byte) (int, error) { f(); return len(p), nil }

// The real pass through a fake release source: notify prints one stderr line
// and leaves stdout and the exit code as in an off run.
func TestAutoUpdateNotifyEndToEnd(t *testing.T) {
	r := newAutoRig(t, "notify", nil)
	r.a.autoRun = nil
	r.a.updateTransport = updRT(func(req *http.Request) (*http.Response, error) {
		r.reqs++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Request: req,
			Body: io.NopCloser(strings.NewReader(`{"tag_name":"v9.9.9","assets":[]}`))}, nil
	})
	ref := newAutoRig(t, "off", nil)
	code := r.a.runAndAutoUpdate(context.Background(), []string{"workflow", "show"})
	refCode := ref.a.runAndAutoUpdate(context.Background(), []string{"workflow", "show"})
	if code != refCode || r.out.String() != ref.out.String() {
		t.Fatal("stdout or exit differ from an off run")
	}
	if got := r.err.String(); !strings.HasSuffix(got, "rig 9.9.9 is available: run rig update\n") {
		t.Fatalf("stderr %q", got)
	}
	if r.reqs != 1 {
		t.Fatalf("%d requests", r.reqs)
	}
	// within 24 h: cached, no new request
	r.a.runAndAutoUpdate(context.Background(), []string{"workflow", "show"})
	if r.reqs != 1 {
		t.Fatalf("second run made a request: %d", r.reqs)
	}
}
