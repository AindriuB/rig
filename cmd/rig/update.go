package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AindriuB/rig/internal/selfupdate"
)

const updateUsage = "usage: rig update [--check] [--to <version>] [--json]"

// updateJSON is the --json shape; check is the failed check, else omitted.
type updateJSON struct {
	Current string `json:"current"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Path    string `json:"path"`
	Check   string `json:"check,omitempty"`
}

// update implements `rig update`. Exit 0: updated, or already current. Exit 1:
// refused (older or unsigned target, unwritable directory) or, with --check,
// a newer release exists. Exit 2: usage, network, or a failed check.
func (a *app) update(ctx context.Context, args []string) int {
	fs := newFlags()
	check, asJSON := fs.boolean("--check"), fs.boolean("--json")
	to := fs.str("--to")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 0 {
		return a.fail("update", "%s", updateUsage)
	}
	if fs.given != nil && *to == "" {
		return a.fail("update", "--to needs a version; %s", updateUsage)
	}
	exe, err := a.executable()
	if err != nil {
		return a.fail("update", "cannot find the running rig: %v", err)
	}
	o := selfupdate.Options{
		Exe: exe, Current: version, To: *to, Check: *check,
		Client: selfupdate.NewClient(a.updateTransport),
	}
	run := a.updater
	if run == nil {
		run = selfupdate.Update
	}
	res := run(ctx, o)
	if *asJSON {
		b, _ := json.Marshal(updateJSON{res.Current, res.Target, res.Action, res.Path, res.Check})
		fmt.Fprintln(a.stdout, string(b))
	} else {
		fmt.Fprintln(a.stdout, res.Message)
	}
	return res.ExitCode()
}

// executable is the running rig, or the injected one in tests.
func (a *app) executable() (string, error) {
	if a.exe != nil {
		return a.exe()
	}
	return os.Executable()
}

// cleanupOld removes a stale <rig>.old left by a Windows update. It never
// affects output or the exit code.
func cleanupOld(exe func() (string, error)) {
	p, err := exe()
	if err != nil {
		return
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	selfupdate.CleanupOld(p)
}
