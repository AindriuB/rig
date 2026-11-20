package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/AindriuB/workspace/cli/internal/forge"
	"github.com/AindriuB/workspace/cli/internal/git"
)

func (a *app) pr(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("pr", "expected view, list, checks, open or wait")
	}
	switch args[0] {
	case "view":
		return a.prView(ctx, args[1:])
	case "list":
		return a.prList(ctx, args[1:])
	case "checks":
		return a.prChecks(ctx, args[1:])
	case "open":
		return a.prOpen(ctx, args[1:])
	case "wait":
		return a.prWait(ctx, args[1:])
	}
	return a.fail("pr", "unknown subcommand %q: expected view, list, checks, open or wait", args[0])
}

// repoDir is where a pr command runs: --repo if given, else the cwd.
func (a *app) repoDir(repo string) (string, error) {
	if repo == "" {
		return a.cwd, nil
	}
	if fi, err := os.Stat(repo); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no such directory: %s", repo)
	}
	return repo, nil
}

// prLine is the one-line form /status prints:
// #N  red|running|green|none  branch  title  [conflict]  [draft]
func prLine(pr forge.PullRequest) string {
	title := []rune(pr.Title)
	if len(title) > 50 {
		title = title[:50]
	}
	line := fmt.Sprintf("#%d  %s  %s  %s", pr.Number, forge.Rollup(pr.Checks), pr.Head, string(title))
	if pr.Mergeable == forge.MergeableNo {
		line += "  conflict"
	}
	if pr.Draft {
		line += "  draft"
	}
	return line
}

// prOut is a pull request as --json prints it: the neutral type plus the
// rolled-up CI state.
type prOut struct {
	forge.PullRequest
	CI forge.CI `json:"ci"`
}

func (a *app) printJSON(v any) int {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return a.fail("pr", "%v", err)
	}
	return 0
}

// lookup parses the flags shared by view and checks and fetches the pull
// request.
func (a *app) lookup(ctx context.Context, cmd string, args []string) (pr forge.PullRequest, asJSON bool, code int) {
	fs := newFlags()
	jsonOut, repo, task := fs.boolean("--json"), fs.str("--repo"), fs.str("--task")
	pos, err := fs.parse(args)
	if err != nil {
		return pr, false, a.fail(cmd, "%v", err)
	}
	if len(pos) > 1 || (len(pos) == 1 && *task != "") {
		return pr, false, a.fail(cmd, "expected one pull request: a number, a URL, a branch, or --task <id>")
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return pr, false, a.fail(cmd, "%v", err)
	}
	// A bare number is always a pull request number, so a task is named
	// with --task: task 07 and pull request 7 are different things.
	ref := ""
	if len(pos) == 1 {
		ref = pos[0]
	} else if *task != "" {
		if ref, err = taskBranch(ctx, git.Repo{Dir: dir}, *task); err != nil {
			return pr, false, a.fail(cmd, "%v", err)
		}
	}
	f, err := a.forge(ctx, dir)
	if err != nil {
		return pr, false, a.fail(cmd, "%v", err)
	}
	pr, err = f.ViewPR(ctx, ref)
	if errors.Is(err, forge.ErrNotFound) {
		if ref == "" {
			ref = "the current branch"
		}
		return pr, false, a.fail(cmd, "no pull request for %s", ref)
	}
	if err != nil {
		return pr, false, a.fail(cmd, "%v", err)
	}
	return pr, *jsonOut, 0
}

func (a *app) prView(ctx context.Context, args []string) int {
	pr, asJSON, code := a.lookup(ctx, "pr view", args)
	if code != 0 {
		return code
	}
	if asJSON {
		return a.printJSON(prOut{pr, forge.Rollup(pr.Checks)})
	}
	fmt.Fprintln(a.stdout, prLine(pr))
	fmt.Fprintf(a.stdout, "  %s\n  state %s  base %s  head %s  %s\n", pr.URL, pr.State, pr.Base, short(pr.HeadSHA), pr.Mergeable)
	printChecks(a, pr.Checks, true)
	return 0
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// printChecks lists checks one per line. With brief set, passed and skipped
// checks are counted, not listed.
func printChecks(a *app, checks []forge.Check, brief bool) {
	quiet := 0
	for _, c := range checks {
		if brief && (c.State == forge.CheckPass || c.State == forge.CheckSkipped) {
			quiet++
			continue
		}
		line := fmt.Sprintf("  %-7s  %s", c.State, c.Name)
		if c.URL != "" && c.State.Failed() {
			line += "  " + c.URL
		}
		fmt.Fprintln(a.stdout, line)
	}
	if brief && quiet > 0 {
		fmt.Fprintf(a.stdout, "  %d passed or skipped\n", quiet)
	}
}

func (a *app) prChecks(ctx context.Context, args []string) int {
	pr, asJSON, code := a.lookup(ctx, "pr checks", args)
	if code != 0 {
		return code
	}
	if asJSON {
		return a.printJSON(struct {
			Number int           `json:"number"`
			CI     forge.CI      `json:"ci"`
			Checks []forge.Check `json:"checks"`
		}{pr.Number, forge.Rollup(pr.Checks), pr.Checks})
	}
	fmt.Fprintf(a.stdout, "#%d  %s  %s\n", pr.Number, forge.Rollup(pr.Checks), pr.Head)
	printChecks(a, pr.Checks, false)
	return 0
}

func (a *app) prList(ctx context.Context, args []string) int {
	fs := newFlags()
	jsonOut, mine := fs.boolean("--json"), fs.boolean("--mine")
	repo, head, state, limit := fs.str("--repo"), fs.str("--head"), fs.str("--state"), fs.str("--limit")
	pos, err := fs.parse(args)
	if err != nil {
		return a.fail("pr list", "%v", err)
	}
	if len(pos) > 0 {
		return a.fail("pr list", "unexpected argument %q", pos[0])
	}
	opts := forge.ListOptions{Mine: *mine, Head: *head}
	switch forge.PRState(*state) {
	case "", forge.StateOpen, forge.StateClosed, forge.StateMerged:
		opts.State = forge.PRState(*state)
	default:
		return a.fail("pr list", "--state must be open, closed or merged")
	}
	if *limit != "" {
		if opts.Limit, err = strconv.Atoi(*limit); err != nil || opts.Limit < 1 {
			return a.fail("pr list", "--limit must be a positive number")
		}
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return a.fail("pr list", "%v", err)
	}
	f, err := a.forge(ctx, dir)
	if err != nil {
		return a.fail("pr list", "%v", err)
	}
	prs, err := f.ListPRs(ctx, opts)
	if err != nil {
		return a.fail("pr list", "%v", err)
	}
	if *jsonOut {
		out := make([]prOut, 0, len(prs))
		for _, pr := range prs {
			out = append(out, prOut{pr, forge.Rollup(pr.Checks)})
		}
		return a.printJSON(out)
	}
	for _, pr := range prs {
		fmt.Fprintln(a.stdout, prLine(pr))
	}
	return 0
}

// prOpen opens a pull request for a branch that is already on the forge. The
// head branch is always named, never inferred, and nothing is pushed.
//
// The body is only ever the --body argument. There is deliberately no flag
// that reads it from a file: `kit pr` runs without a permission prompt, and
// such a flag would publish any file the user can read.
func (a *app) prOpen(ctx context.Context, args []string) int {
	fs := newFlags()
	head, base, title := fs.str("--head"), fs.str("--base"), fs.str("--title")
	body, repo := fs.str("--body"), fs.str("--repo")
	draft := fs.boolean("--draft")
	pos, err := fs.parse(args)
	if err != nil {
		return a.fail("pr open", "%v", err)
	}
	if len(pos) > 0 {
		return a.fail("pr open", "unexpected argument %q", pos[0])
	}
	if *head == "" {
		return a.fail("pr open", "--head <branch> is required; kit pr open never guesses the branch and never pushes")
	}
	if *title == "" {
		return a.fail("pr open", "--title is required")
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return a.fail("pr open", "%v", err)
	}
	f, err := a.forge(ctx, dir)
	if err != nil {
		return a.fail("pr open", "%v", err)
	}
	pr, err := f.CreatePR(ctx, forge.CreatePRInput{Head: *head, Base: *base, Title: *title, Body: *body, Draft: *draft})
	if err != nil {
		return a.fail("pr open", "%v", err)
	}
	fmt.Fprintf(a.stdout, "#%d  %s\n", pr.Number, pr.URL)
	return 0
}
