package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AindriuB/rig/internal/proc"
)

const (
	hooksUsage        = "usage: rig hooks start|deliver ..."
	hooksDeliverUsage = "usage: rig hooks deliver [--follow] [--repo <dir>]"
	hooksStartUsage   = "usage: rig hooks start [--json] [--] <cmd> [args...]"
)

func (a *app) hooks(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("hooks", "%s", hooksUsage)
	}
	switch args[0] {
	case "deliver":
		return a.hooksDeliver(ctx, args[1:])
	case "start":
		return a.hooksStart(args[1:])
	}
	return a.fail("hooks", "unknown subcommand %q\n%s", args[0], hooksUsage)
}

// hooksStart starts a detached program without the RIG_HOOK_* variables and
// prints its pid. Flags before the command are rig's; everything after the
// command name belongs to the program.
func (a *app) hooksStart(args []string) int {
	asJSON := false
	for len(args) > 0 {
		if args[0] == "--json" {
			asJSON = true
			args = args[1:]
		} else if args[0] == "--" {
			args = args[1:]
			break
		} else {
			break
		}
	}
	if len(args) == 0 {
		return a.fail("hooks start", "no command given; %s", hooksStartUsage)
	}
	pid, refused, err := proc.Start(proc.Options{Dir: a.cwd}, args[0], args[1:]...)
	if err != nil {
		if errors.Is(err, proc.ErrNotFound) {
			return a.fail("hooks start", "cannot start %s: program not found", args[0])
		}
		return a.fail("hooks start", "cannot start %s: %v", args[0], err)
	}
	if refused {
		fmt.Fprintln(a.stderr, "rig hooks start: warning: the hook's job forbids breakaway, so the child will not outlive the hook job")
	}
	if asJSON {
		b, _ := json.Marshal(struct {
			Pid int `json:"pid"`
		}{pid})
		fmt.Fprintln(a.stdout, string(b))
		return 0
	}
	fmt.Fprintln(a.stdout, pid)
	return 0
}
