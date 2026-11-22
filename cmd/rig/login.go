package main

import (
	"context"
	"fmt"
)

// login implements `rig login`. It is a stub until its phase-2.5 task lands.
func (a *app) login(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig login: not built yet")
	return 2
}

// logout implements `rig logout`. It is a stub until its phase-2.5 task lands.
func (a *app) logout(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig logout: not built yet")
	return 2
}
