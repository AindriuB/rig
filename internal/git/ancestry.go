package git

import (
	"context"
	"errors"

	"github.com/AindriuB/rig/internal/proc"
)

// IsAncestor reports whether a is an ancestor of b (or the same commit).
// git exits 1 for "no"; any other failure, such as an unknown rev, is an
// error.
func (r Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	args := []string{"merge-base", "--is-ancestor", a, b}
	res, err := r.run(ctx, queryTimeout, args...)
	if err == nil {
		return true, nil
	}
	var ee *proc.ExitError
	if errors.As(err, &ee) && ee.Code == 1 {
		return false, nil
	}
	return false, wrap(args, res, err)
}
