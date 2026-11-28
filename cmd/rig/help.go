package main

import (
	"fmt"
	"strings"
)

// shortEntry is one line of the short help screen.
type shortEntry struct{ head, desc string }

// shortSection is one topic heading of the short help screen.
type shortSection struct {
	title   string
	entries []shortEntry
}

var shortSections = []shortSection{
	{"Tasks:", []shortEntry{
		{"rig task <subcommand>", "Create, show, list and move tasks"},
	}},
	{"Findings and notes:", []shortEntry{
		{"rig finding <subcommand>", "Record and list review findings"},
		{"rig note <subcommand>", "Add and list notes"},
	}},
	{"Planning:", []shortEntry{
		{"rig workflow <subcommand>", "Show or check the workflow"},
		{"rig next", "List the tasks that are ready to work on"},
		{"rig context <task>", "Print the context an agent needs for a task"},
		{"rig status", "Show a summary of tasks by state"},
	}},
	{"Branches and pull requests:", []shortEntry{
		{"rig push <task-id|branch>", "Push a task or record branch to origin"},
		{"rig pr <subcommand>", "View, list, open and wait for pull requests"},
	}},
	{"Tracker:", []shortEntry{
		{"rig tracker <subcommand>", "Pull, push, refresh and report tracker issues"},
	}},
	{"Hooks:", []shortEntry{
		{"rig hooks <subcommand>", "Start, deliver and inspect queued hooks"},
		{"rig events", "List events across all tasks, from a cursor"},
	}},
	{"Setup:", []shortEntry{
		{"rig config <subcommand>", "Get, set and list config values"},
		{"rig login [<service>]", "Log in to github, gitlab or jira, or show login status"},
		{"rig logout <service>", "Log out of jira; github/gitlab print the gh/glab command"},
		{"rig update", "Update rig to the latest release"},
		{"rig version", "Print the rig version"},
	}},
	{"Help:", []shortEntry{
		{"rig help [<command> | --all]", "Show a command's forms, or every command"},
	}},
}

// shortUsage is the short help screen: one line per top-level command.
func shortUsage() string {
	var b strings.Builder
	b.WriteString("usage: rig <command>\n")
	for _, s := range shortSections {
		b.WriteString("\n" + s.title + "\n")
		for _, e := range s.entries {
			fmt.Fprintf(&b, "  %-42s%s\n", e.head, e.desc)
		}
	}
	return b.String()
}

// groupCommands are the commands that take a subcommand.
var groupCommands = map[string]bool{
	"task": true, "finding": true, "note": true, "workflow": true,
	"pr": true, "tracker": true, "hooks": true, "config": true,
}

// commandHelp is the help for one command, derived from usage: a usage line,
// a blank line, then every entry of the full text whose head is `rig <w>` or
// starts `rig <w> `. It reports false when w heads no entry.
func commandHelp(w string) (string, bool) {
	var entries []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			entries = append(entries, strings.Join(cur, "\n")+"\n")
		}
		cur = nil
	}
	keep := false
	for _, l := range strings.Split(usage, "\n")[2:] {
		if strings.HasPrefix(l, "  rig ") {
			flush()
			f := strings.Fields(l)
			keep = f[1] == w
		} else if !strings.HasPrefix(l, "      ") {
			flush()
			keep = false
			continue
		}
		if keep {
			cur = append(cur, l)
		}
	}
	flush()
	if len(entries) == 0 {
		return "", false
	}
	head := "usage: rig " + w
	if groupCommands[w] {
		head += " <subcommand>"
	}
	return head + "\n\n" + strings.Join(entries, ""), true
}

// groupHelp prints a group's command help to stderr and returns 2, naming bad
// when the subcommand was not one the group has.
func (a *app) groupHelp(group, bad string) int {
	if bad != "" {
		fmt.Fprintf(a.stderr, "rig %s: unknown subcommand %q\n", group, bad)
	}
	h, _ := commandHelp(group)
	fmt.Fprint(a.stderr, h)
	return 2
}

// help implements `rig help`, `rig -h` and `rig --help`; args follow the word.
func (a *app) help(args []string) int {
	switch {
	case len(args) == 0:
		fmt.Fprint(a.stdout, shortUsage())
		return 0
	case len(args) == 1 && args[0] == "--all":
		fmt.Fprint(a.stdout, usage)
		return 0
	case len(args) == 1:
		if h, ok := commandHelp(args[0]); ok {
			fmt.Fprint(a.stdout, h)
			return 0
		}
	}
	bad := args[0]
	if len(args) > 1 {
		bad = args[1]
	}
	fmt.Fprintf(a.stderr, "rig help: unknown command %q\n%s", bad, shortUsage())
	return 2
}
