package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/selfupdate"
	"github.com/AindriuB/rig/internal/tty"
)

// runAndAutoUpdate runs the command, then the automatic update pass. The pass
// comes strictly after the command has returned and never changes its exit code.
func (a *app) runAndAutoUpdate(ctx context.Context, args []string) int {
	code := a.run(ctx, args)
	a.autoUpdate(ctx, args)
	return code
}

// machineOutput reports whether args ask for JSON: --json, --json=..., --format
// json or --format=json.
func machineOutput(args []string) bool {
	for i, s := range args {
		switch {
		case s == "--json", strings.HasPrefix(s, "--json="), s == "--format=json":
			return true
		case s == "--format" && i+1 < len(args) && args[i+1] == "json":
			return true
		}
	}
	return false
}

// autoUpdate is the update.auto hook. It prints at most one line to stderr
// and swallows every failure, including a panic.
func (a *app) autoUpdate(ctx context.Context, args []string) {
	defer func() { _ = recover() }()
	if ctx.Err() != nil || len(args) == 0 || args[0] == "update" || args[0] == "version" || args[0] == "--version" ||
		machineOutput(args) || a.getenv("CI") != "" {
		return
	}
	isTTY := a.stderrTTY
	if isTTY == nil {
		isTTY = func() bool { return tty.IsTerminal(os.Stderr) }
	}
	if !isTTY() {
		return
	}
	layers, err := config.Open(config.Options{Home: a.homeDir})
	if err != nil {
		return
	}
	mode := layers.UpdateAuto()
	if mode == config.UpdateAutoOff {
		return
	}
	home, err := a.homeDir()
	if err != nil || home == "" {
		return
	}
	exe, err := a.executable()
	if err != nil {
		return
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	run := a.autoRun
	if run == nil {
		run = selfupdate.Auto
	}
	run(ctx, selfupdate.AutoOptions{
		Mode: mode, Dir: filepath.Join(home, config.Dir), Now: a.now, Stderr: a.stderr,
		Install: selfupdate.Options{
			Exe: exe, Current: version, Client: selfupdate.NewClient(a.updateTransport),
		},
	})
}
