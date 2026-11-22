package main

import (
	"context"
	"fmt"
)

// finding implements `rig finding`. It is a stub until its phase-2 task lands.
func (a *app) finding(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig finding: not built yet")
	return 2
}
