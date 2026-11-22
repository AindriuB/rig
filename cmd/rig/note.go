package main

import (
	"context"
	"fmt"
)

// note implements `rig note`. It is a stub until its phase-2 task lands.
func (a *app) note(_ context.Context, _ []string) int {
	fmt.Fprintln(a.stderr, "rig note: not built yet")
	return 2
}
