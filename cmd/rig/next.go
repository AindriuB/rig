package main

import (
	"context"
	"fmt"
)

// next implements `rig next`. It is a stub until its phase-2 task lands.
func (a *app) next(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig next: not built yet")
	return 2
}
