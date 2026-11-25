package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const dl = "https://github.com/AindriuB/rig/releases/download/"

// fakeRelease serves one release (tag) with a binary body; `sig` is the
// SHA256SUMS.sig body. A tag the server does not know is a 404.
func fakeRelease(tag, binary, sig string) http.RoundTripper {
	assets := `[{"name":"rig-linux-amd64","browser_download_url":"` + dl + tag + `/rig-linux-amd64"},
{"name":"SHA256SUMS","browser_download_url":"` + dl + tag + `/SHA256SUMS"},
{"name":"SHA256SUMS.sig","browser_download_url":"` + dl + tag + `/SHA256SUMS.sig"}]`
	return rt(func(r *http.Request) (*http.Response, error) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/latest"), strings.HasSuffix(p, "/tags/"+tag):
			return resp(200, `{"tag_name":"`+tag+`","assets":`+assets+`}`), nil
		case strings.HasSuffix(p, "/rig-linux-amd64"):
			return resp(200, binary), nil
		case strings.HasSuffix(p, "/SHA256SUMS"):
			return resp(200, "sums"), nil
		case strings.HasSuffix(p, "/SHA256SUMS.sig"):
			return resp(200, sig), nil
		}
		return resp(404, "no"), nil
	})
}

func installed(t *testing.T) (string, []byte) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "rig")
	old := []byte("old rig")
	if err := os.WriteFile(exe, old, 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, old
}

func opts(exe, cur, to string, rtp http.RoundTripper) Options {
	return Options{
		Exe: exe, Current: cur, To: to, Client: NewClient(rtp), GOOS: "linux", GOARCH: "amd64",
		verify:     func(*Download) error { return nil },
		RunVersion: func(ctx context.Context, p string) (string, error) { return "0.5.3\n", nil },
	}
}

func unchanged(t *testing.T, exe string, old []byte) {
	t.Helper()
	got, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatalf("installed file changed: %q %v", got, err)
	}
	ents, _ := os.ReadDir(filepath.Dir(exe))
	if len(ents) != 1 {
		t.Fatalf("stray files left: %v", ents)
	}
}

func TestUpdateOutcomeTable(t *testing.T) {
	ctx := context.Background()
	t.Run("updated", func(t *testing.T) {
		exe, _ := installed(t)
		r := Update(ctx, opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new rig", "sig")))
		if r.Action != ActionUpdated || r.ExitCode() != 0 || r.Target != "0.5.3" {
			t.Fatalf("%+v", r)
		}
		if r.Message != "rig updated 0.5.2 -> 0.5.3 ("+exe+")" {
			t.Fatalf("msg %q", r.Message)
		}
		if b, _ := os.ReadFile(exe); string(b) != "new rig" {
			t.Fatalf("installed %q", b)
		}
	})
	t.Run("up to date", func(t *testing.T) {
		exe, old := installed(t)
		r := Update(ctx, opts(exe, "0.5.3", "", fakeRelease("v0.5.3", "new", "sig")))
		if r.Action != ActionUpToDate || r.ExitCode() != 0 || r.Message != "rig 0.5.3 is up to date" {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("latest older, no --to", func(t *testing.T) {
		exe, old := installed(t)
		r := Update(ctx, opts(exe, "0.6.0", "", fakeRelease("v0.5.3", "new", "sig")))
		if r.Action != ActionRefused || r.ExitCode() != 1 || !strings.Contains(r.Message, "0.5.3") || !strings.Contains(r.Message, "0.6.0") {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("before 0.5.2", func(t *testing.T) {
		exe, old := installed(t)
		r := Update(ctx, opts(exe, "0.6.0", "v0.5.1", fakeRelease("v0.5.1", "new", "sig")))
		if r.Action != ActionRefused || r.ExitCode() != 1 || !strings.Contains(r.Message, "0.5.1") {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("not writable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("directory permissions not enforced")
		}
		exe, old := installed(t)
		dir := filepath.Dir(exe)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o755)
		r := Update(ctx, opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new", "sig")))
		if r.Action != ActionRefused || r.ExitCode() != 1 || !strings.Contains(r.Message, exe) {
			t.Fatalf("%+v", r)
		}
		os.Chmod(dir, 0o755)
		unchanged(t, exe, old)
	})
	t.Run("network error", func(t *testing.T) {
		exe, old := installed(t)
		r := Update(ctx, opts(exe, "0.5.2", "", rt(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("down")
		})))
		if r.Action != ActionFailed || r.ExitCode() != 2 || r.Check != "" || !strings.Contains(r.Message, "down") {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("signature fails", func(t *testing.T) {
		exe, old := installed(t)
		o := opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new", "bad sig"))
		o.verify = nil // the real keys reject the made-up signature
		r := Update(ctx, o)
		if r.Action != ActionFailed || r.ExitCode() != 2 || r.Check != "signature" {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("checksum fails", func(t *testing.T) {
		exe, old := installed(t)
		o := opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new", "sig"))
		o.verify = func(*Download) error { return &CheckError{Check: "checksum", Detail: "mismatch"} }
		r := Update(ctx, o)
		if r.Action != ActionFailed || r.ExitCode() != 2 || r.Check != "checksum" {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
	t.Run("check", func(t *testing.T) {
		for _, c := range []struct {
			cur, action string
			code        int
		}{{"0.5.3", ActionUpToDate, 0}, {"0.5.2", ActionAvailable, 1}} {
			exe, old := installed(t)
			o := opts(exe, c.cur, "", fakeRelease("v0.5.3", "new", "sig"))
			o.Check = true
			r := Update(ctx, o)
			if r.Action != c.action || r.ExitCode() != c.code {
				t.Fatalf("%s: %+v", c.cur, r)
			}
			unchanged(t, exe, old)
		}
		for _, c := range []struct{ cur, to, latest string }{
			{"0.6.0", "", "v0.5.3"}, {"0.5.3", "", "v0.5.2"}, {"0.6.0", "", "v0.5.1"}, {"0.6.0", "v0.5.1", "v0.5.1"}, {"0.6.0", "v0.5.3", "v0.5.3"},
		} {
			exe, old := installed(t)
			o := opts(exe, c.cur, c.to, fakeRelease(c.latest, "new", "sig"))
			o.Check = true
			r := Update(ctx, o)
			if r.Action != ActionUpToDate || r.ExitCode() != 0 || r.Message != "rig "+c.cur+" is up to date" {
				t.Fatalf("%+v: %+v", c, r)
			}
			unchanged(t, exe, old)
		}
		exe, old := installed(t)
		o := opts(exe, "0.5.2", "", rt(func(*http.Request) (*http.Response, error) { return resp(500, "x"), nil }))
		o.Check = true
		if r := Update(ctx, o); r.ExitCode() != 2 {
			t.Fatalf("%+v", r)
		}
		unchanged(t, exe, old)
	})
}

func TestUpdateVersionCheck(t *testing.T) {
	for name, run := range map[string]func(context.Context, string) (string, error){
		"mismatch": func(context.Context, string) (string, error) { return "0.5.9\n", nil },
		"wont run": func(context.Context, string) (string, error) { return "", errors.New("exec format error") },
	} {
		t.Run(name, func(t *testing.T) {
			exe, old := installed(t)
			o := opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new", "sig"))
			var staged string
			o.RunVersion = func(ctx context.Context, p string) (string, error) {
				staged = p
				if b, _ := os.ReadFile(exe); !bytes.Equal(b, old) {
					t.Error("target replaced before the version check")
				}
				return run(ctx, p)
			}
			r := Update(context.Background(), o)
			if r.Action != ActionFailed || r.ExitCode() != 2 || r.Check != "version" {
				t.Fatalf("%+v", r)
			}
			if _, err := os.Stat(staged); !os.IsNotExist(err) {
				t.Fatalf("staged file not discarded: %v", err)
			}
			unchanged(t, exe, old)
		})
	}
}

// Signature runs before the checksum, which runs before the staged binary is
// executed; a failure stops the later steps.
func TestUpdateCheckOrder(t *testing.T) {
	exe, old := installed(t)
	var order []string
	o := opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "new", "sig"))
	o.verify = func(*Download) error { order = append(order, "verify"); return nil }
	o.RunVersion = func(context.Context, string) (string, error) {
		order = append(order, "version")
		return "0.5.3", nil
	}
	if r := Update(context.Background(), o); r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
	if strings.Join(order, ",") != "verify,version" {
		t.Fatalf("order %v", order)
	}
	// Verify itself checks the signature before the checksum.
	exe2, old2 := installed(t)
	o = opts(exe2, "0.5.2", "", fakeRelease("v0.5.3", "new", "sig"))
	o.verify = func(*Download) error { return &CheckError{Check: "signature", Detail: "x"} }
	o.RunVersion = func(context.Context, string) (string, error) { t.Error("ran staged binary"); return "", nil }
	if r := Update(context.Background(), o); r.Check != "signature" {
		t.Fatalf("%+v", r)
	}
	unchanged(t, exe2, old2)
	_ = old
}

func TestUpdateTo(t *testing.T) {
	exe, _ := installed(t)
	o := opts(exe, "0.6.0", "v0.5.3", fakeRelease("v0.5.3", "older", "sig"))
	r := Update(context.Background(), o)
	if r.Action != ActionUpdated || r.Target != "0.5.3" {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(exe); string(b) != "older" {
		t.Fatalf("installed %q", b)
	}
	// 0.5.2 itself is the first signed release.
	exe, old := installed(t)
	o = opts(exe, "0.6.0", "0.5.2", fakeRelease("v0.5.2", "x", "sig"))
	o.RunVersion = func(context.Context, string) (string, error) { return "0.5.2", nil }
	if r := Update(context.Background(), o); r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
	_ = old
}

func TestUpdateRunsStagedVersionThroughProc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	exe, _ := installed(t)
	o := opts(exe, "0.5.2", "", fakeRelease("v0.5.3", "#!/bin/sh\necho 0.5.3\n", "sig"))
	o.RunVersion = nil
	r := Update(context.Background(), o)
	if r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
}

// CleanupOld never reports an error, whether or not <exe>.old exists.
func TestCleanupOldIsQuiet(t *testing.T) {
	exe, _ := installed(t)
	CleanupOld(exe) // no .old: nothing happens
	if err := os.WriteFile(exe+".old", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatalf(".old left: %v", err)
	}
	CleanupOld(filepath.Join(t.TempDir(), "missing", "rig"))
}

func TestUpdateCheckBadToIsFailedNotAvailable(t *testing.T) {
	exe, _ := installed(t)
	o := opts(exe, "0.6.0", "foo", fakeRelease("v0.7.0", "x", "sig"))
	o.Check = true
	if r := Update(context.Background(), o); r.Action != ActionFailed {
		t.Fatalf("%+v", r)
	}
}
