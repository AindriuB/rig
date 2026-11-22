package workflow

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Problem is one rule a workflow breaks. Path is the JSON path of the value
// at fault, such as "transitions[3].gates[0].shaIs".
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string { return p.Path + ": " + p.Message }

// InvalidError reports a workflow that parses but breaks the format's rules.
type InvalidError struct {
	Name     string
	Problems []Problem
}

func (e *InvalidError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.String()
	}
	return fmt.Sprintf("workflow %s is invalid: %s", e.Name, strings.Join(parts, "; "))
}

// argPlaceholders are the placeholders a hook or command gate argument may
// be, each as a whole argument.
var argPlaceholders = []string{"{short}", "{id}", "{slug}", "{state}", "{reason}", "{sha}"}

var fieldTypes = []string{TypeString, TypeText, TypeInt, TypeBool, TypeList, TypeEnum, TypePaths, TypeTasks}

// Validate checks the workflow against the format's rules and returns every
// rule broken, in a stable order. An empty result means the workflow is
// valid.
func (w *Workflow) Validate() []Problem {
	var ps []Problem
	add := func(path, format string, v ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, v...)})
	}
	declared := func(s string) bool { _, ok := w.States[s]; return ok }

	if w.Name == "" {
		add("name", "is empty")
	}
	if w.Version < 1 {
		add("version", "must be a positive integer")
	}

	// States.
	names := sortedKeys(w.States)
	if len(names) == 0 {
		add("states", "declares no states")
	}
	bound := false
	for _, n := range names {
		s := w.States[n]
		path := "states." + n
		if n == "" || n == AnyState || strings.HasPrefix(n, "$") {
			add(path, "%q is not a usable state name", n)
		}
		if s.Who != "" && s.Who != WhoAgent && s.Who != WhoHuman {
			add(path+".who", "is %q; want %q or %q", s.Who, WhoAgent, WhoHuman)
		}
		if s.Final && s.ShaBound {
			add(path+".shaBound", "a final state cannot be sha-bound")
		}
		bound = bound || s.ShaBound
	}
	switch {
	case w.Initial == "":
		add("initial", "is empty")
	case !declared(w.Initial):
		add("initial", "state %q is not declared", w.Initial)
	}
	switch {
	case w.OnNewCommit == "" && bound:
		add("onNewCommit", "is empty, but sha-bound states need it")
	case w.OnNewCommit == "":
	case !declared(w.OnNewCommit):
		add("onNewCommit", "state %q is not declared", w.OnNewCommit)
	case w.States[w.OnNewCommit].Final || w.States[w.OnNewCommit].ShaBound:
		add("onNewCommit", "state %q must be neither final nor sha-bound", w.OnNewCommit)
	}
	for _, p := range placeholders(w.Branch) {
		if !slices.Contains(branchPlaceholders, p) {
			add("branch", "unknown placeholder %s; want one of %s", p, strings.Join(branchPlaceholders, " "))
		}
	}

	// Counters and severities, which transitions and gates refer to.
	counters := map[string]bool{}
	for i, c := range w.Counters {
		path := fmt.Sprintf("counters[%d]", i)
		switch {
		case c == "":
			add(path, "is empty")
		case counters[c]:
			add(path, "counter %q is declared twice", c)
		}
		counters[c] = true
	}
	severities := map[string]bool{}
	for i, s := range w.Findings.Severities {
		path := fmt.Sprintf("findings.severities[%d]", i)
		switch {
		case s == "":
			add(path, "is empty")
		case severities[s]:
			add(path, "severity %q is declared twice", s)
		}
		severities[s] = true
	}

	// Fields.
	validateFields(add, "fields", w.Fields, true)
	validateFields(add, "findings.fields", w.Findings.Fields, false)

	// Transitions.
	for i, tr := range w.AllTransitions {
		path := fmt.Sprintf("transitions[%d]", i)
		if len(tr.From) == 0 {
			add(path+".from", "names no state")
		}
		for j, f := range tr.From {
			fp := fmt.Sprintf("%s.from", path)
			if len(tr.From) > 1 {
				fp = fmt.Sprintf("%s[%d]", fp, j)
			}
			switch {
			case f == AnyState:
			case !declared(f):
				add(fp, "state %q is not declared", f)
			case w.States[f].Final:
				add(fp, "state %q is final and takes no transitions out", f)
			}
		}
		switch {
		case tr.To == Previous:
		case tr.To == "":
			add(path+".to", "is empty")
		case !declared(tr.To):
			add(path+".to", "state %q is not declared", tr.To)
		}
		switch tr.Takes {
		case "", TakesSha, TakesPR, TakesReason:
		default:
			add(path+".takes", "is %q; want %q, %q or %q", tr.Takes, TakesSha, TakesPR, TakesReason)
		}
		if tr.Count != "" && !counters[tr.Count] {
			add(path+".count", "counter %q is not declared", tr.Count)
		}
		for j, g := range tr.Gates {
			gp := fmt.Sprintf("%s.gates[%d]", path, j)
			if g.problem != "" {
				add(gp, "%s", g.problem)
				continue
			}
			gp += "." + g.Kind
			switch g.Kind {
			case GateNoOpenFindings:
				for k, s := range g.Severities {
					if !severities[s] {
						add(fmt.Sprintf("%s.severity[%d]", gp, k), "severity %q is not declared in findings.severities", s)
					}
				}
			case GateShaIsHead:
				if tr.Takes != TakesSha {
					add(gp, "needs the transition to take %q", TakesSha)
				}
			case GateShaIs:
				if !declared(g.State) {
					add(gp, "state %q is not declared", g.State)
				}
				if tr.Takes != TakesSha {
					add(gp, "needs the transition to take %q", TakesSha)
				}
			case GateDependsIn:
				for k, s := range g.States {
					if !declared(s) {
						add(fmt.Sprintf("%s[%d]", gp, k), "state %q is not declared", s)
					}
				}
				if f, ok := w.Fields[DependsField]; !ok || f.Type != TypeTasks {
					add(gp, "needs a %q field of type %q", DependsField, TypeTasks)
				}
			case GateFieldSet:
				if _, ok := w.Fields[g.Field]; !ok && g.Field != "title" && g.Field != "body" {
					add(gp, "field %q is not declared", g.Field)
				}
			case GateCounterBelow:
				if !counters[g.Counter] {
					add(gp+".counter", "counter %q is not declared", g.Counter)
				}
			case GateCommand:
				validateArgv(add, gp, g.Argv)
			}
		}
	}

	// Routes: no two transitions, nor one transition twice, may take the
	// same from-state to the same target, once from-lists and AnyState are
	// expanded. AnyState expands to every declared state that is not final.
	routes := map[[2]string]int{}
	for i, tr := range w.AllTransitions {
		for _, from := range w.expandFrom(tr) {
			key := [2]string{from, tr.To}
			if j, ok := routes[key]; ok {
				add(fmt.Sprintf("transitions[%d]", i), "route %s → %s is also taken by transitions[%d]", from, tr.To, j)
				continue
			}
			routes[key] = i
		}
	}

	// Hooks.
	for i, h := range w.Hooks {
		path := fmt.Sprintf("hooks[%d]", i)
		event, state, _ := strings.Cut(h.On, ":")
		switch {
		case h.On == "transition":
		case (event == "enter" || event == "leave") && state != "":
			if !declared(state) {
				add(path+".on", "state %q is not declared", state)
			}
		default:
			add(path+".on", "is %q; want enter:<state>, leave:<state> or transition", h.On)
		}
		if len(h.Run) == 0 || h.Run[0] == "" {
			add(path+".run", "is not a non-empty argument vector")
			continue
		}
		validateArgv(add, path+".run", h.Run)
	}

	// Reachability, only when the graph is well enough formed to walk.
	if declared(w.Initial) {
		for _, n := range w.unreachable() {
			add("states."+n, "is not reachable from %q", w.Initial)
		}
	}
	return ps
}

// expandFrom returns the states tr leaves: its from-list, with AnyState
// replaced by every declared non-final state other than tr's target, in
// sorted order.
func (w *Workflow) expandFrom(tr Transition) []string {
	var out []string
	for _, f := range tr.From {
		if f != AnyState {
			out = append(out, f)
			continue
		}
		for _, n := range sortedKeys(w.States) {
			if !w.States[n].Final && n != tr.To {
				out = append(out, n)
			}
		}
	}
	return out
}

type addFunc func(path, format string, v ...any)

// validateFields checks a fields object. owns is allowed only on task fields.
func validateFields(add addFunc, base string, fields map[string]Field, task bool) {
	owners := 0
	for _, n := range sortedKeys(fields) {
		f := fields[n]
		path := base + "." + n
		if n == "" {
			add(path, "field name is empty")
		}
		if task && (n == "title" || n == "body") {
			add(path, "%q is a built-in task field", n)
		}
		if !slices.Contains(fieldTypes, f.Type) {
			add(path+".type", "is %q; want one of %s", f.Type, strings.Join(fieldTypes, ", "))
		}
		if f.Type == TypeEnum {
			if len(f.Values) == 0 {
				add(path+".values", "an enum needs values")
			}
			seen := map[string]bool{}
			for i, v := range f.Values {
				if v == "" || seen[v] {
					add(fmt.Sprintf("%s.values[%d]", path, i), "value %q is empty or repeated", v)
				}
				seen[v] = true
			}
		} else if len(f.Values) > 0 {
			add(path+".values", "only an enum takes values")
		}
		if f.Owns {
			owners++
			switch {
			case !task:
				add(path+".owns", "only a task field can be the owns field")
			case f.Type != TypePaths:
				add(path+".owns", "the owns field must be of type %q", TypePaths)
			case owners > 1:
				add(path+".owns", "only one field can be the owns field")
			}
		}
	}
}

// validateArgv checks that every placeholder in argv is known and is a whole
// argument.
func validateArgv(add addFunc, base string, argv []string) {
	for i, a := range argv {
		ph := placeholders(a)
		if len(ph) == 0 {
			continue
		}
		path := fmt.Sprintf("%s[%d]", base, i)
		if len(ph) > 1 || ph[0] != a {
			add(path, "placeholders are substituted only as whole arguments")
			continue
		}
		if !slices.Contains(argPlaceholders, a) {
			add(path, "unknown placeholder %s; want one of %s", a, strings.Join(argPlaceholders, " "))
		}
	}
}

// unreachable returns the declared states no path of transitions reaches
// from the initial state, sorted.
func (w *Workflow) unreachable() []string {
	seen := map[string]bool{w.Initial: true}
	queue := []string{w.Initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, tr := range w.Transitions(s) {
			// A Previous target returns to a state already reached.
			if _, ok := w.States[tr.To]; ok && !seen[tr.To] {
				seen[tr.To] = true
				queue = append(queue, tr.To)
			}
		}
	}
	var out []string
	for _, n := range sortedKeys(w.States) {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
