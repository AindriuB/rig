package selfupdate

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type autoEnv struct {
	dir, exe string
	old      []byte
	reqs     int
	now      time.Time
	errOut   bytes.Buffer
}

func newAutoEnv(t *testing.T) *autoEnv {
	exe, old := installed(t)
	return &autoEnv{dir: t.TempDir(), exe: exe, old: old, now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
}

// opts counts requests made through rtp.
func (e *autoEnv) opts(mode, cur string, rtp http.RoundTripper) AutoOptions {
	counted := rt(func(r *http.Request) (*http.Response, error) {
		e.reqs++
		return rtp.RoundTrip(r)
	})
	return AutoOptions{
		Mode: mode, Dir: e.dir, Now: func() time.Time { return e.now }, Stderr: &e.errOut,
		Install: opts(e.exe, cur, "", counted),
	}
}

func lines(s string) int { return len(strings.Split(strings.TrimSpace(s), "\n")) }

func TestAutoNotifyPrintsOneLine(t *testing.T) {
	e := newAutoEnv(t)
	Auto(context.Background(), e.opts("notify", "0.5.2", fakeRelease("v0.5.3", "new", "sig")))
	if got := e.errOut.String(); got != "rig 0.5.3 is available: run rig update\n" {
		t.Fatalf("stderr %q", got)
	}
	unchanged(t, e.exe, e.old)
}

func TestAutoNotNewerPrintsNothing(t *testing.T) {
	e := newAutoEnv(t)
	Auto(context.Background(), e.opts("notify", "0.5.3", fakeRelease("v0.5.3", "new", "sig")))
	if e.errOut.Len() != 0 {
		t.Fatalf("stderr %q", e.errOut.String())
	}
}

func TestAutoOffAndUnknownModeDoNothing(t *testing.T) {
	for _, m := range []string{"", "off", "bogus"} {
		e := newAutoEnv(t)
		Auto(context.Background(), e.opts(m, "0.5.2", fakeRelease("v0.5.3", "new", "sig")))
		if e.reqs != 0 || e.errOut.Len() != 0 {
			t.Errorf("mode %q: %d requests, stderr %q", m, e.reqs, e.errOut.String())
		}
	}
}

func TestAutoTwoChecksWithin24hMakeOneRequest(t *testing.T) {
	e := newAutoEnv(t)
	o := e.opts("notify", "0.5.2", fakeRelease("v0.5.3", "new", "sig"))
	Auto(context.Background(), o)
	first := e.reqs
	e.now = e.now.Add(23 * time.Hour)
	e.errOut.Reset()
	Auto(context.Background(), o)
	if first != 1 || e.reqs != 1 {
		t.Fatalf("requests: first %d, total %d", first, e.reqs)
	}
	if !strings.Contains(e.errOut.String(), "0.5.3 is available") {
		t.Fatalf("cached result not used: %q", e.errOut.String())
	}
	e.now = e.now.Add(2 * time.Hour)
	Auto(context.Background(), o)
	if e.reqs != 2 {
		t.Fatalf("after 25h: %d requests", e.reqs)
	}
}

func TestAutoFailedCheckPrintsNothingAndRecordsTime(t *testing.T) {
	e := newAutoEnv(t)
	down := rt(func(*http.Request) (*http.Response, error) { return resp(500, "no"), nil })
	o := e.opts("install", "0.5.2", down)
	Auto(context.Background(), o)
	Auto(context.Background(), o)
	if e.errOut.Len() != 0 || e.reqs != 1 {
		t.Fatalf("stderr %q, %d requests", e.errOut.String(), e.reqs)
	}
	if _, err := os.Stat(filepath.Join(e.dir, cacheFile)); err != nil {
		t.Fatal(err)
	}
}

func TestAutoSlowCheckTimesOut(t *testing.T) {
	e := newAutoEnv(t)
	slow := rt(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	o := e.opts("notify", "0.5.2", slow)
	o.Timeout = 50 * time.Millisecond
	Auto(context.Background(), o)
	if e.errOut.Len() != 0 {
		t.Fatalf("stderr %q", e.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, cacheFile)); err != nil {
		t.Fatal("time not recorded")
	}
}

func TestAutoInstallUpdatesAndNamesVersion(t *testing.T) {
	e := newAutoEnv(t)
	Auto(context.Background(), e.opts("install", "0.5.2", fakeRelease("v0.5.3", "new rig", "sig")))
	if b, _ := os.ReadFile(e.exe); string(b) != "new rig" {
		t.Fatalf("not installed: %q", b)
	}
	if got := e.errOut.String(); lines(got) != 1 || !strings.Contains(got, "0.5.3") {
		t.Fatalf("stderr %q", got)
	}
}

func TestAutoFailedInstallLeavesBinaryAndPrintsOneLine(t *testing.T) {
	e := newAutoEnv(t)
	o := e.opts("install", "0.5.2", fakeRelease("v0.5.3", "new rig", "sig"))
	o.Install.verify = func(*Download) error { return &CheckError{Check: "signature", Detail: "bad\nmultiline"} }
	Auto(context.Background(), o)
	unchanged(t, e.exe, e.old)
	if got := e.errOut.String(); lines(got) != 1 || !strings.Contains(got, "automatic update failed") {
		t.Fatalf("stderr %q", got)
	}
}

// I1: a held lock means no download, no stderr and no cache.
func TestAutoHeldLockMeansNoDownloadNoStderrNoCache(t *testing.T) {
	e := newAutoEnv(t)
	l, err := tryLock(e.exe)
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	downloads := 0
	inner := fakeRelease("v0.5.3", "new rig", "sig")
	o := e.opts("install", "0.5.2", rt(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/rig-linux-amd64") {
			downloads++
		}
		return inner.RoundTrip(r)
	}))
	Auto(context.Background(), o)
	if downloads != 0 || e.errOut.Len() != 0 {
		t.Fatalf("downloads %d, stderr %q", downloads, e.errOut.String())
	}
	if b, _ := os.ReadFile(e.exe); !bytes.Equal(b, e.old) {
		t.Fatalf("binary changed: %q", b)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, cacheFile))
	if err != nil {
		t.Fatalf("the completed lookup should be cached: %v", err)
	}
	if strings.Contains(string(b), `"latest":""`) || !strings.Contains(string(b), "0.5.3") {
		t.Fatalf("cache %s", b)
	}
}

// I3: no time-based takeover, however far the clock jumps.
func TestAutoDoesNotTakeOverABusyHolderAfterAnHour(t *testing.T) {
	e := newAutoEnv(t)
	inDownload, release := make(chan struct{}), make(chan struct{})
	inner := fakeRelease("v0.5.3", "new rig", "sig")
	blocking := rt(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/rig-linux-amd64") {
			close(inDownload)
			<-release
		}
		return inner.RoundTrip(r)
	})
	done := make(chan Result)
	go func() { done <- Update(context.Background(), opts(e.exe, "0.5.2", "", blocking)) }()
	<-inDownload
	e.now = e.now.Add(time.Hour)
	downloads := 0
	o := e.opts("install", "0.5.2", rt(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/rig-linux-amd64") {
			downloads++
		}
		return inner.RoundTrip(r)
	}))
	Auto(context.Background(), o)
	if downloads != 0 || e.errOut.Len() != 0 {
		t.Fatalf("holder taken over: downloads %d, stderr %q", downloads, e.errOut.String())
	}
	close(release)
	if r := <-done; r.Action != ActionUpdated {
		t.Fatalf("%+v", r)
	}
}

func TestAutoCancelledBeforeMakesNoRequestAndNoCache(t *testing.T) {
	e := newAutoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Auto(ctx, e.opts("install", "0.5.2", fakeRelease("v0.5.3", "new rig", "sig")))
	if e.reqs != 0 || e.errOut.Len() != 0 {
		t.Fatalf("%d requests, stderr %q", e.reqs, e.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, cacheFile)); !os.IsNotExist(err) {
		t.Fatal("cache written")
	}
}

func TestAutoCancelledDuringLookupWritesNothing(t *testing.T) {
	e := newAutoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	o := e.opts("install", "0.5.2", rt(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	}))
	Auto(ctx, o)
	if e.errOut.Len() != 0 {
		t.Fatalf("stderr %q", e.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, cacheFile)); !os.IsNotExist(err) {
		t.Fatal("cache written")
	}
}

func TestAutoCancelledDuringInstallWritesNothingMore(t *testing.T) {
	e := newAutoEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	o := e.opts("install", "0.5.2", fakeRelease("v0.5.3", "new rig", "sig"))
	o.Update = func(context.Context, Options) Result {
		cancel()
		return Result{Action: ActionFailed, Message: "interrupted"}
	}
	Auto(ctx, o)
	if e.errOut.Len() != 0 {
		t.Fatalf("stderr %q", e.errOut.String())
	}
	b, err := os.ReadFile(filepath.Join(e.dir, cacheFile))
	if err != nil || !strings.Contains(string(b), "0.5.3") {
		t.Fatalf("the lookup's cache should be untouched: %q %v", b, err)
	}
}
