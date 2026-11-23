package main

import (
	"fmt"
	"slices"
)

// outFormat is how a command prints its result.
type outFormat int

const (
	formatText outFormat = iota
	formatMarkdown
	formatJSON
)

// formatFlags is the --format flag and its aliases --json and --markdown.
type formatFlags struct {
	format   *string
	asJSON   *bool
	markdown *bool
}

// addFormatFlags registers --format, --json and --markdown on fs.
func addFormatFlags(fs *flagSet) *formatFlags {
	return &formatFlags{format: fs.str("--format"), asJSON: fs.boolean("--json"), markdown: fs.boolean("--markdown")}
}

// resolve returns the format the flags ask for: text when none is given. An
// unknown value, or two different formats, is an error.
func (f *formatFlags) resolve() (outFormat, error) {
	var got []outFormat
	if *f.format != "" {
		switch *f.format {
		case "text":
			got = append(got, formatText)
		case "markdown":
			got = append(got, formatMarkdown)
		case "json":
			got = append(got, formatJSON)
		default:
			return formatText, fmt.Errorf("unknown --format %q (want text, markdown or json)", *f.format)
		}
	}
	if *f.asJSON {
		got = append(got, formatJSON)
	}
	if *f.markdown {
		got = append(got, formatMarkdown)
	}
	for _, g := range got {
		if g != got[0] {
			return formatText, fmt.Errorf("--format, --json and --markdown name different formats")
		}
	}
	if len(got) == 0 {
		return formatText, nil
	}
	return got[0], nil
}

// resolveArgs is resolve for a command given args: --format repeated with
// different values is an error too, which the flag parser alone would hide
// by keeping the last one.
func (f *formatFlags) resolveArgs(args []string) (outFormat, error) {
	_, vals, err := splitRepeated(args, []string{"--format"}, []string{"--repo", "--budget", "--state"})
	if err != nil {
		return formatText, err
	}
	for _, v := range vals["--format"] {
		if v != vals["--format"][0] {
			return formatText, fmt.Errorf("--format given with different values %s", slices.Compact(slices.Clone(vals["--format"])))
		}
	}
	return f.resolve()
}
