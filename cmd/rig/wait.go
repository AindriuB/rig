package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/forge"
)

const (
	waitUsage       = "usage: rig pr wait <pr-number | --run <run-id>> [--repo <dir>]"
	tailLines       = 40
	defaultWaitSecs = 1800
	logTimeout      = 2 * time.Minute
)

// prWait implements `rig pr wait`, which blocks until a pull request's checks
// or one CI run finish. It keeps the contract of claude-kit's ci-wait.sh, down to
// the "ci-wait:" prefix on errors, because callers read both.
//
// Exit 0: green, and one line of output. Exit 1: red; the failed check or job
// names, then a 40-line log tail. Exit 2: usage error, forge error, no
// checks, or timeout (CI_WAIT_TIMEOUT seconds, default 1800).
func (a *app) prWait(ctx context.Context, args []string) int {
	die := func(format string, v ...any) int {
		fmt.Fprintf(a.stderr, "ci-wait: %s\n", fmt.Sprintf(format, v...))
		return 2
	}
	fs := newFlags()
	run, repo := fs.str("--run"), fs.str("--repo")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 1 || (len(pos) == 1) == (*run != "") {
		return die("%s", waitUsage)
	}
	var target forge.WaitTarget
	var what string
	if *run != "" {
		if target.Run, err = parseID(*run); err != nil {
			return die("not a number: %s", *run)
		}
		what = "run " + *run
	} else {
		n, err := parseID(pos[0])
		if err != nil || n > 1<<31-1 {
			return die("not a number: %s", pos[0])
		}
		target.PR = int(n)
		what = "PR #" + pos[0]
	}
	secs := defaultWaitSecs
	if v := a.getenv("CI_WAIT_TIMEOUT"); v != "" {
		n, err := parseID(v)
		if err != nil || n > 1<<31-1 {
			return die("CI_WAIT_TIMEOUT must be a positive number of seconds")
		}
		secs = int(n)
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return die("%v", err)
	}
	f, err := a.forge(ctx, dir)
	if err != nil {
		return die("%v", err)
	}

	wctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	defer cancel()
	res, err := f.WaitChecks(wctx, target)
	if errors.Is(err, context.DeadlineExceeded) {
		return die("timed out after %ds waiting on %s", secs, what)
	}
	if err != nil {
		return die("%v", err)
	}
	if res.Green {
		fmt.Fprintf(a.stdout, "CI green: %s\n", what)
		return 0
	}

	if target.Run != 0 {
		fmt.Fprintf(a.stdout, "CI red: %s (%s)\nFailed jobs:\n", what, res.Conclusion)
	} else {
		fmt.Fprintf(a.stdout, "CI red: %s\nFailed checks:\n", what)
	}
	for _, c := range res.Failed {
		fmt.Fprintf(a.stdout, "  %s\n", c.Name)
	}
	lctx, cancel := context.WithTimeout(ctx, logTimeout)
	defer cancel()
	logs, err := f.FailedLogs(lctx, target, res.Failed)
	if errors.Is(err, forge.ErrNoLogs) {
		return 1 // checks from an outside provider: there is no log to show
	}
	fmt.Fprintln(a.stdout, "--- log tail ---")
	tail := logTail(logs)
	if err != nil || len(tail) == 0 {
		tail = []string{"(no failed-job log available from gh yet)"}
	}
	fmt.Fprintln(a.stdout, strings.Join(tail, "\n"))
	return 1
}

// parseID reads a positive decimal number: digits only, no sign.
func parseID(s string) (int64, error) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("not a positive number")
	}
	return n, nil
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// logTail picks at most 40 lines from failed-job logs. The 40 are shared out
// between the failed jobs: a plain tail would show only the last job, often a
// gate job that only says another one failed. Each job's share ends at its
// last `##[error]` line, not at the post-job cleanup that follows it.
// Terminal escapes and carriage returns are stripped.
func logTail(logs []forge.LogLine) []string {
	var order []string
	byJob := map[string][]string{}
	lastErr := map[string]int{}
	for _, l := range logs {
		text := strings.TrimSuffix(ansiRE.ReplaceAllString(l.Text, ""), "\r")
		if _, seen := byJob[l.Job]; !seen {
			order = append(order, l.Job)
		}
		byJob[l.Job] = append(byJob[l.Job], text)
		if strings.Contains(text, "##[error]") {
			lastErr[l.Job] = len(byJob[l.Job])
		}
	}
	if len(order) == 0 {
		return nil
	}
	per := max(tailLines/len(order), 1)
	var out []string
	for _, job := range order {
		lines := byJob[job]
		to := len(lines)
		if e, ok := lastErr[job]; ok {
			to = e
		}
		out = append(out, lines[max(to-per, 0):to]...)
	}
	if len(out) > tailLines {
		out = out[:tailLines]
	}
	return out
}
