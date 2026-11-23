package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	gitpkg "github.com/AindriuB/rig/internal/git"
)

// ErrLocalCommitted is wrapped by TrackerURL's error when tracker.url would
// come from a .rig/config.json that the repository supplies.
var ErrLocalCommitted = errors.New("tracker.url comes from .rig/config.json, which is committed to this repository")

// trustTimeout bounds the one git call the trust check makes.
const trustTimeout = 30 * time.Second

// LocalCommitted reports whether the local config could have come from the
// repository rather than the user: git tracks anything at or under .rig
// (matched case-insensitively, gitlinks included), or .rig or .rig/config.json
// is a symlink. Any error is returned so the caller fails closed.
func LocalCommitted(ctx context.Context, root string) (bool, error) {
	tracked, err := gitpkg.Repo{Dir: root}.LsFiles(ctx, []string{":(top,icase).rig", ":(top,icase).rig/**"})
	if err != nil {
		return false, err
	}
	if len(tracked) > 0 {
		return true, nil
	}
	for _, p := range []string{filepath.Join(root, Dir), filepath.Join(root, Dir, ConfigFile)} {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

// vetLocalURL runs when the local file sets tracker.url. If the file could be
// the repository's, or that cannot be told, the value is dropped from the
// local layer so no path (Get, GetIn, List, Tracker) can return it, and the
// reason is kept for TrackerURL.
func (l *Layers) vetLocalURL() {
	if _, ok := l.local[KeyTrackerURL]; !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), trustTimeout)
	defer cancel()
	committed, err := LocalCommitted(ctx, l.root)
	switch {
	case err != nil:
		l.urlBlocked = fmt.Errorf("cannot tell whether .rig/config.json is committed: %w", err)
	case committed:
		l.urlBlocked = ErrLocalCommitted
	default:
		return
	}
	delete(l.local, KeyTrackerURL)
	l.warnings = append(l.warnings, fmt.Sprintf("%s: %s ignored: %v", l.localPath, KeyTrackerURL, l.urlBlocked))
}

// TrackerURL resolves tracker.url for a command that will contact the
// tracker. The Value is empty when the key is not set. It is an error, and
// the Value empty, when the local .rig/config.json sets tracker.url and is
// committed to the repository (errors.Is ErrLocalCommitted) or cannot be
// shown not to be. Callers that take --url need not call it when the flag
// is given.
func (l *Layers) TrackerURL() (Value, error) {
	if l.urlBlocked != nil {
		return Value{}, l.urlBlocked
	}
	v, _ := l.Get(KeyTrackerURL)
	return v, nil
}

// TrackerCommand resolves tracker.command, the argument vector of an exec
// tracker. A project file never supplies it. The Value is empty when unset
// and a []string otherwise.
func (l *Layers) TrackerCommand() (Value, error) {
	v, _ := l.Get(KeyTrackerCommand)
	return v, nil
}

// JiraAPI is tracker.jira.api: auto, v2 or v3.
func (l *Layers) JiraAPI() string {
	if s := l.str(KeyTrackerJiraAPI); s != "" {
		return s
	}
	return DefaultJiraAPI
}
