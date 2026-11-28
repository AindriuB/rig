package main

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestHelpScreen: help, -h and --help print the short screen to stdout; bare
// rig prints it to stderr, exit 2; an unknown command adds a line (AC1).
func TestHelpScreen(t *testing.T) {
	golden := readFile(t, "help.golden")
	for _, arg := range []string{"help", "-h", "--help"} {
		r := rig(t, t.TempDir(), nil, nil, arg)
		if r.code != 0 || r.out != golden || r.errOut != "" {
			t.Errorf("rig %s: exit %d, stdout matches %v, stderr %q", arg, r.code, r.out == golden, r.errOut)
		}
	}
	r := rig(t, t.TempDir(), nil, nil)
	if r.code != 2 || r.out != "" || r.errOut != golden {
		t.Errorf("rig: exit %d, stdout %q, stderr matches %v", r.code, r.out, r.errOut == golden)
	}
	r = rig(t, t.TempDir(), nil, nil, "frobnicate")
	if want := "rig: unknown command \"frobnicate\"\n" + golden; r.code != 2 || r.out != "" || r.errOut != want {
		t.Errorf("rig frobnicate: exit %d, stdout %q, stderr matches %v", r.code, r.out, r.errOut == want)
	}
}

// TestHelpScreenLayout: the short screen parses, has 18 entries matching the
// contract and fits in 100 columns (AC2).
func TestHelpScreenLayout(t *testing.T) {
	golden := readFile(t, "help.golden")
	entries := parseScreen(t, golden)
	want := []screenEntry{
		{head: "  rig task <subcommand>", desc: "Create, show, list and move tasks"},
		{head: "  rig finding <subcommand>", desc: "Record and list review findings"},
		{head: "  rig note <subcommand>", desc: "Add and list notes"},
		{head: "  rig workflow <subcommand>", desc: "Show or check the workflow"},
		{head: "  rig next", desc: "List the tasks that are ready to work on"},
		{head: "  rig context <task>", desc: "Print the context an agent needs for a task"},
		{head: "  rig status", desc: "Show a summary of tasks by state"},
		{head: "  rig push <task-id|branch>", desc: "Push a task or record branch to origin"},
		{head: "  rig pr <subcommand>", desc: "View, list, open and wait for pull requests"},
		{head: "  rig tracker <subcommand>", desc: "Pull, push, refresh and report tracker issues"},
		{head: "  rig hooks <subcommand>", desc: "Start, deliver and inspect queued hooks"},
		{head: "  rig events", desc: "List events across all tasks, from a cursor"},
		{head: "  rig config <subcommand>", desc: "Get, set and list config values"},
		{head: "  rig login [<service>]", desc: "Log in to github, gitlab or jira, or show login status"},
		{head: "  rig logout <service>", desc: "Log out of jira; github/gitlab print the gh/glab command"},
		{head: "  rig update", desc: "Update rig to the latest release"},
		{head: "  rig version", desc: "Print the rig version"},
		{head: "  rig help [<command> | --all]", desc: "Show a command's forms, or every command"},
	}
	if len(entries) != 18 || len(want) != 18 {
		t.Fatalf("%d entries, want 18", len(entries))
	}
	for i, w := range want {
		if strings.TrimRight(entries[i].head, " ") != w.head || entries[i].desc != w.desc {
			t.Errorf("entry %d = %q %q, want %q %q", i, entries[i].head, entries[i].desc, w.head, w.desc)
		}
	}
	for n, l := range strings.Split(golden, "\n") {
		if w := utf8.RuneCountInString(l); w > 100 {
			t.Errorf("line %d is %d columns", n+1, w)
		}
	}
}

// firstWords is every first word of the full text.
func firstWords(t *testing.T) []string {
	t.Helper()
	var ws []string
	seen := map[string]bool{}
	for _, l := range strings.Split(readGolden(t), "\n") {
		if strings.HasPrefix(l, "  rig ") {
			if w := strings.Fields(l)[1]; !seen[w] {
				seen[w] = true
				ws = append(ws, w)
			}
		}
	}
	return ws
}

// TestHelpCommand: every first word has command help on all three forms (AC4).
func TestHelpCommand(t *testing.T) {
	ws := firstWords(t)
	if len(ws) != 17 {
		t.Fatalf("%d first words, want 17: %v", len(ws), ws)
	}
	for _, w := range ws {
		want, ok := commandHelp(w)
		if !ok {
			t.Errorf("no command help for %s", w)
			continue
		}
		for _, args := range [][]string{{"help", w}, {w, "--help"}, {w, "-h"}} {
			r := rig(t, t.TempDir(), nil, nil, args...)
			if r.code != 0 || r.out != want || r.errOut != "" {
				t.Errorf("rig %v: exit %d, stdout matches %v, stderr %q", args, r.code, r.out == want, r.errOut)
			}
		}
		// Each entry of the full text with this head appears, in order, with its continuation lines.
		var lines []string
		keep := false
		for _, l := range strings.Split(readGolden(t), "\n")[2:] {
			if strings.HasPrefix(l, "  rig ") {
				keep = strings.Fields(l)[1] == w
			} else if !strings.HasPrefix(l, "      ") {
				keep = false
			}
			if keep {
				lines = append(lines, l)
			}
		}
		prefix := "usage: rig " + w
		if groupCommands[w] {
			prefix += " <subcommand>"
		}
		if exp := prefix + "\n\n" + strings.Join(lines, "\n") + "\n"; want != exp {
			t.Errorf("command help for %s:\n%s\nwant:\n%s", w, want, exp)
		}
	}
	if got := rig(t, t.TempDir(), nil, nil, "help", "hooks").out; got != readFile(t, "help-hooks.golden") {
		t.Errorf("rig help hooks differs from help-hooks.golden:\n%s", got)
	}
}

// TestHelpGroups: a bare group or an unknown subcommand prints the command
// help to stderr, exit 2 (AC5).
func TestHelpGroups(t *testing.T) {
	for g := range groupCommands {
		want, _ := commandHelp(g)
		r := rig(t, t.TempDir(), nil, nil, g)
		if r.code != 2 || r.out != "" || r.errOut != want {
			t.Errorf("rig %s: exit %d, stdout %q, stderr %q", g, r.code, r.out, r.errOut)
		}
		r = rig(t, t.TempDir(), nil, nil, g, "frobnicate")
		if w := "rig " + g + ": unknown subcommand \"frobnicate\"\n" + want; r.code != 2 || r.out != "" || r.errOut != w {
			t.Errorf("rig %s frobnicate: exit %d, stdout %q, stderr %q", g, r.code, r.out, r.errOut)
		}
	}
}

// TestHelpErrors: an unknown command or an extra argument after help exits 2
// with empty stdout (AC6).
func TestHelpErrors(t *testing.T) {
	screen := readFile(t, "help.golden")
	for _, c := range []struct {
		args []string
		bad  string
	}{
		{[]string{"help", "frobnicate"}, "frobnicate"},
		{[]string{"help", "task", "extra"}, "extra"},
		{[]string{"help", "--all", "extra"}, "extra"},
	} {
		r := rig(t, t.TempDir(), nil, nil, c.args...)
		want := "rig help: unknown command \"" + c.bad + "\"\n" + screen
		if r.code != 2 || r.out != "" || r.errOut != want {
			t.Errorf("rig %v: exit %d, stdout %q, stderr %q", c.args, r.code, r.out, r.errOut)
		}
	}
}
