package main

import (
	"context"
	"fmt"
)

// context implements `rig context`. It is a stub until its phase-2 task
// lands.
func (a *app) context(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig context: not built yet")
	return 2
}
