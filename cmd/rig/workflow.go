package main

import (
	"context"
	"fmt"
)

// workflow implements `rig workflow`. It is a stub until its phase-2 task
// lands.
func (a *app) workflow(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig workflow: not built yet")
	return 2
}
