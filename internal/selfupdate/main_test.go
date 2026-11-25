package selfupdate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
)

var errNetworkInTest = errors.New("selfupdate tests: network access refused (loopback only)")

// loopbackOnly refuses every request that is not to a loopback host, so a
// test cannot reach the real network. httptest servers keep working.
type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	h := r.URL.Hostname()
	if ip := net.ParseIP(h); h == "localhost" || ip != nil && ip.IsLoopback() {
		return l.next.RoundTrip(r)
	}
	return nil, fmt.Errorf("%w: %s", errNetworkInTest, r.URL.Host)
}

// With RIG_LOCK_HELPER set to an install path, the test binary takes that
// install's update lock, prints "locked" and holds it until stdin closes.
func TestMain(m *testing.M) {
	if target := os.Getenv("RIG_LOCK_HELPER"); target != "" {
		l, err := tryLock(target)
		if err != nil {
			fmt.Println("busy:", err)
			os.Exit(3)
		}
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		l.release()
		os.Exit(0)
	}
	http.DefaultTransport = loopbackOnly{next: http.DefaultTransport}
	os.Exit(m.Run())
}

// holdInChild starts a child process that holds the lock for exe.
func holdInChild(t *testing.T, exe string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "RIG_LOCK_HELPER="+exe)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, _ := bufio.NewReader(out).ReadString('\n')
	if line != "locked\n" {
		t.Fatalf("child did not take the lock: %q", line)
	}
	return cmd
}
