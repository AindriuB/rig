package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Automatic update timing and file names.
const (
	autoInterval = 24 * time.Hour
	autoTimeout  = 2 * time.Second
	cacheFile    = "update-check.json"
)

// AutoOptions is one automatic update pass.
type AutoOptions struct {
	Mode    string // "notify" or "install"; anything else does nothing
	Dir     string // the global ~/.rig directory: the cache lives here
	Now     func() time.Time
	Stderr  io.Writer
	Install Options // Exe, Current and Client; To and Check are ignored
	// Update runs the install; nil is Update.
	Update func(context.Context, Options) Result
	// Timeout bounds the release check; 0 is 2 s.
	Timeout time.Duration
}

type checkCache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest,omitempty"`
}

// Auto checks for a newer release at most once per 24 h and, when there is
// one, prints a notice or installs it. It never returns an error and writes
// only one line to Stderr.
func Auto(ctx context.Context, o AutoOptions) {
	if o.Mode != "notify" && o.Mode != "install" || o.Dir == "" || ctx.Err() != nil {
		return
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	path := filepath.Join(o.Dir, cacheFile)
	var c checkCache
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	latest := ""
	if t := now(); !c.Checked.IsZero() && t.Sub(c.Checked) >= 0 && t.Sub(c.Checked) < autoInterval {
		latest = c.Latest
	} else {
		latest = lookupLatest(ctx, o)
		if ctx.Err() != nil {
			return // cancelled: not a completed check
		}
		writeCache(path, checkCache{Checked: t, Latest: latest})
	}
	if latest == "" {
		return
	}
	if ok, err := SignedEra(latest); err != nil || !ok {
		return
	}
	if cmp, err := CompareVersions(latest, o.Install.Current); err != nil || cmp <= 0 {
		return
	}
	if o.Mode == "notify" {
		fmt.Fprintf(o.Stderr, "rig %s is available: run rig update\n", latest)
		return
	}
	install(ctx, o, now, path)
}

// lookupLatest is the latest release version within the timeout, "" on any
// failure.
func lookupLatest(ctx context.Context, o AutoOptions) string {
	if o.Install.Client == nil {
		return ""
	}
	d := o.Timeout
	if d == 0 {
		d = autoTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	rel, err := o.Install.Client.Lookup(ctx, "")
	if err != nil {
		return ""
	}
	return trimV(rel.Tag)
}

func writeCache(path string, c checkCache) {
	b, err := json.Marshal(c)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o600)
}

// install runs the update. The update takes the lock beside the binary; a
// busy lock means nothing happens. A failed install clears the cached version
// so it is not retried until the next check. A cancelled pass writes nothing.
func install(ctx context.Context, o AutoOptions, now func() time.Time, cachePath string) {
	run := o.Update
	if run == nil {
		run = Update
	}
	opts := o.Install
	opts.To, opts.Check = "", false
	res := run(ctx, opts)
	msg := strings.Join(strings.Fields(res.Message), " ")
	if res.Action == ActionUpdated {
		fmt.Fprintf(o.Stderr, "%s\n", msg)
		return
	}
	if res.Action == ActionRefused && res.Check == "lock" || ctx.Err() != nil {
		return
	}
	writeCache(cachePath, checkCache{Checked: now()})
	if msg == "" {
		msg = res.Action
	}
	fmt.Fprintf(o.Stderr, "rig: automatic update failed: %s\n", msg)
}
