package main

import (
	"context"
	"fmt"
)

// task implements `rig task`. It is a stub until its phase-2 task lands.
func (a *app) task(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig task: not built yet")
	return 2
}
