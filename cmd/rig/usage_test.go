package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func readGolden(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/usage.golden")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUsageGolden(t *testing.T) {
	golden := readGolden(t)
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

// screenEntry is one command form on the usage screen.
type screenEntry struct {
	head, desc string
	cont       []string
}

// parseScreen checks the layout of the usage text (AC3) and returns its
// entries; layout violations are reported on t.
func parseScreen(t *testing.T, text string) []screenEntry {
	t.Helper()
	if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("usage must end in exactly one newline")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0] != "usage: rig <command>" || lines[1] != "" {
		t.Fatalf("lines 1-2 = %q, %q", lines[0], lines[1])
	}
	col := -1
	// first pass: the description column, from the first command line that carries one.
	split := func(l string) (head, desc string, c int) {
		i := strings.Index(l[2:], "  ")
		if i < 0 {
			return l, "", -1
		}
		i += 2
		rest := strings.TrimLeft(l[i:], " ")
		return l[:i], rest, len(l) - len(rest)
	}
	for _, l := range lines[2:] {
		if strings.HasPrefix(l, "  rig ") {
			if _, d, c := split(l); d != "" {
				col = c
				break
			}
		}
	}
	if col < 0 {
		t.Fatal("no command line carries a description")
	}
	var entries []screenEntry
	// state: what the previous line was.
	const (
		other = iota
		headOnly
		carrying
		descLine
		contLine
	)
	prev := other
	for n, l := range lines[2:] {
		at := n + 3
		if l != strings.TrimRight(l, " ") {
			t.Errorf("line %d has trailing spaces", at)
		}
		switch {
		case l == "":
			prev = other
		case l[0] != ' ':
			if !strings.HasSuffix(l, ":") {
				t.Errorf("line %d: heading %q does not end in a colon", at, l)
			}
			prev = other
		case strings.HasPrefix(l, "  rig "):
			head, desc, c := split(l)
			if desc == "" {
				entries = append(entries, screenEntry{head: l})
				prev = headOnly
				continue
			}
			if c != col {
				t.Errorf("line %d: description at column %d, want %d", at, c, col)
			}
			entries = append(entries, screenEntry{head: head, desc: desc})
			prev = carrying
		case strings.HasPrefix(l, strings.Repeat(" ", col)) && l[col] != ' ':
			if prev != headOnly {
				t.Errorf("line %d: description line does not follow a head-only command line", at)
			}
			if len(entries) > 0 {
				entries[len(entries)-1].desc = l[col:]
			}
			prev = descLine
		case strings.HasPrefix(l, "      ") && l[6] != ' ':
			if prev == headOnly || prev == other {
				t.Errorf("line %d: continuation line follows %d", at, prev)
			}
			if len(entries) > 0 {
				e := &entries[len(entries)-1]
				e.cont = append(e.cont, l[6:])
			}
			prev = contLine
		default:
			t.Errorf("line %d does not fit the layout: %q", at, l)
		}
	}
	if prev == headOnly {
		t.Errorf("the last command line has no description")
	}
	return entries
}

func TestUsageLayout(t *testing.T) {
	parseScreen(t, readGolden(t))
}

func TestUsageDescriptions(t *testing.T) {
	golden := readGolden(t)
	entries := parseScreen(t, golden)
	// 46 synopsis lines before this task's change, plus `rig tracker push`.
	if len(entries) != 47 {
		t.Errorf("%d command lines, want 47", len(entries))
	}
	for _, e := range entries {
		if strings.TrimSpace(e.desc) == "" {
			t.Errorf("%q has no description", e.head)
		}
	}
	for n, l := range strings.Split(golden, "\n") {
		if w := utf8.RuneCountInString(l); w > 100 {
			t.Errorf("line %d is %d columns", n+1, w)
		}
	}
}

func TestTrackerUsage(t *testing.T) {
	r := rig(t, t.TempDir(), nil, nil, "tracker")
	want := "rig tracker: usage: rig tracker pull|push|refresh|report|show|status ...\n"
	if r.code != 2 || r.out != "" || r.errOut != want {
		t.Errorf("rig tracker: exit %d, stdout %q, stderr %q", r.code, r.out, r.errOut)
	}
}

// TestUsageNamesSubcommands: every subcommand a dispatcher accepts is in that
// group's usage text and in the top-level usage.
func TestUsageNamesSubcommands(t *testing.T) {
	golden := readGolden(t)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	consts := map[string]string{}
	for _, f := range pkgs["main"].Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil {
					funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok && len(v.Names) == len(v.Values) {
						for i, n := range v.Names {
							if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								consts[n.Name], _ = strconv.Unquote(lit.Value)
							}
						}
					}
				}
			}
		}
	}
	lits := func(n ast.Node) string {
		var b strings.Builder
		skip := map[ast.Node]bool{}
		ast.Inspect(n, func(x ast.Node) bool {
			switch x := x.(type) {
			case *ast.CaseClause:
				for _, e := range x.List {
					skip[e] = true
				}
			case *ast.KeyValueExpr:
				skip[x.Key] = true
			}
			if l, ok := x.(*ast.BasicLit); ok && l.Kind == token.STRING && !skip[l] {
				s, _ := strconv.Unquote(l.Value)
				b.WriteString(s + "\n")
			}
			return true
		})
		return b.String()
	}
	type group struct{ fn, name, usageConst string }
	for _, g := range []group{
		{"run", "", ""}, {"task", "task", "taskUsage"}, {"trackerCmd", "tracker", "trackerUsage"},
		{"finding", "finding", "findingUsage"}, {"note", "note", "noteUsage"},
		{"workflow", "workflow", ""}, {"pr", "pr", ""}, {"config", "config", ""},
	} {
		fn := funcs[g.fn]
		if fn == nil {
			t.Errorf("no dispatcher %s", g.fn)
			continue
		}
		var subs []string
		if g.fn == "config" {
			ast.Inspect(fn, func(x ast.Node) bool {
				if cl, ok := x.(*ast.CompositeLit); ok {
					if mt, ok := cl.Type.(*ast.MapType); ok && len(cl.Elts) > 0 {
						if id, ok := mt.Key.(*ast.Ident); ok && id.Name == "string" {
							for _, e := range cl.Elts {
								if kv, ok := e.(*ast.KeyValueExpr); ok {
									s, _ := strconv.Unquote(kv.Key.(*ast.BasicLit).Value)
									subs = append(subs, s)
								}
							}
						}
					}
				}
				return true
			})
		} else {
			ast.Inspect(fn, func(x ast.Node) bool {
				sw, ok := x.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				ix, ok := sw.Tag.(*ast.IndexExpr)
				if !ok {
					return true
				}
				if id, ok := ix.X.(*ast.Ident); !ok || id.Name != "args" {
					return true
				}
				for _, c := range sw.Body.List {
					for _, e := range c.(*ast.CaseClause).List {
						if l, ok := e.(*ast.BasicLit); ok {
							s, _ := strconv.Unquote(l.Value)
							subs = append(subs, s)
						}
					}
				}
				return false
			})
		}
		if len(subs) == 0 {
			t.Errorf("%s: found no subcommands", g.fn)
		}
		text := lits(fn)
		if g.usageConst != "" {
			text += consts[g.usageConst]
		}
		for _, s := range subs {
			if g.fn == "run" {
				switch s {
				case "help", "-h", "--help", "--version":
					continue
				}
				if !strings.Contains(golden, "rig "+s) {
					t.Errorf("top-level usage lacks rig %s", s)
				}
				continue
			}
			if !strings.Contains(text, s) {
				t.Errorf("%s usage lacks %s", g.name, s)
			}
			if !strings.Contains(golden, "rig "+g.name+" "+s) {
				t.Errorf("top-level usage lacks rig %s %s", g.name, s)
			}
		}
	}
}
