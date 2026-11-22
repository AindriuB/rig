package git

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CommitInfo is one commit as `git log` lists it.
type CommitInfo struct {
	SHA     string
	Subject string
}

// LsFiles lists the tracked files that match pathspecs, sorted, with forward
// slashes, relative to the top of the working tree whatever Dir is. A pathspec is passed to git as given, so a caller can use
// ":(glob)src/**/*.go". With no pathspecs it returns nil without running git,
// so it never lists the whole tree by accident.
func (r Repo) LsFiles(ctx context.Context, pathspecs []string) ([]string, error) {
	if len(pathspecs) == 0 {
		return nil, nil
	}
	args := append([]string{"ls-files", "-z", "--full-name", "--"}, pathspecs...)
	res, err := r.run(ctx, queryTimeout, args...)
	if err != nil {
		return nil, wrap(args, res, err)
	}
	var files []string
	for _, f := range strings.Split(string(res.Stdout), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, nil
}

// LogPaths lists up to limit commits reachable from rev that touch pathspecs,
// newest first. rev must not start with "-". With no pathspecs or a limit
// below 1 it returns nil without running git.
func (r Repo) LogPaths(ctx context.Context, rev string, pathspecs []string, limit int) ([]CommitInfo, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return nil, fmt.Errorf("git log: invalid rev %q", rev)
	}
	if len(pathspecs) == 0 || limit < 1 {
		return nil, nil
	}
	args := append([]string{"log", "-n", strconv.Itoa(limit), "--format=%H %s", rev, "--"}, pathspecs...)
	out, err := r.out(ctx, args...)
	if err != nil || out == "" {
		return nil, err
	}
	var commits []CommitInfo
	for _, line := range strings.Split(out, "\n") {
		sha, subject, ok := strings.Cut(strings.TrimRight(line, "\r"), " ")
		if !ok {
			return nil, fmt.Errorf("git log: unexpected line %q", line)
		}
		commits = append(commits, CommitInfo{SHA: sha, Subject: subject})
	}
	return commits, nil
}
