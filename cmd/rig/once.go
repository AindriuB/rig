package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/AindriuB/rig/internal/store"
)

// onceFlag registers --once on fs for the write command cmd (its words, as in
// "task move"), whose command line is args. Call the returned func after
// fs.parse succeeds: it checks the value and sets a.onceKey, which the store
// the command opens stamps its events with and checks for a repeat. With no
// --once, inside a hook ($RIG_HOOK_EVENT set), the key is derived from the
// hook, the event, cmd and args, so a retried hook repeats the same writes
// as no-ops. If fs has a --json flag, a repeat is reported as JSON.
func (a *app) onceFlag(fs *flagSet, cmd string, args []string) func() error {
	once := fs.str("--once")
	return func() error {
		a.onceKey, a.onceJSON = "", false
		if j, ok := fs.bools["--json"]; ok {
			a.onceJSON = *j
		}
		for _, n := range fs.given {
			if n != "--once" {
				continue
			}
			v := strings.TrimSpace(*once)
			if v == "" {
				return errors.New("--once must not be empty")
			}
			if strings.IndexFunc(v, func(r rune) bool {
				return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
			}) >= 0 {
				return errors.New("--once must not contain control, format or line-separator characters")
			}
			a.onceKey = v
			return nil
		}
		if ev := strings.TrimSpace(a.getenv("RIG_HOOK_EVENT")); ev != "" {
			h := sha256.New()
			for _, p := range append([]string{a.getenv("RIG_HOOK_NAME"), ev, cmd}, args...) {
				fmt.Fprintf(h, "%d:%s\n", len(p), p)
			}
			a.onceKey = "hook-" + hex.EncodeToString(h.Sum(nil))[:24]
		}
		return nil
	}
}

// checkOnce returns store.ErrRepeat when the log of the task ref names
// already has this run's --once key. It runs before anything that reads the
// task's state and can refuse; the store checks again under its lock.
func (w *work) checkOnce(ctx context.Context, ref string) error {
	if w.once == "" {
		return nil
	}
	log, err := w.st.Events(ctx, ref)
	if err != nil {
		return err
	}
	if store.HasOnce(log, w.once) {
		return store.ErrRepeat
	}
	return nil
}

// printRepeat reports a repeated write: nothing was written, and that is a
// success.
func (a *app) printRepeat() int {
	if a.onceJSON {
		k, _ := json.Marshal(a.onceKey)
		fmt.Fprintf(a.stdout, "{\"repeat\":true,\"once\":%s}\n", k)
		return 0
	}
	fmt.Fprintf(a.stdout, "repeat: --once %s already recorded\n", a.onceKey)
	return 0
}
