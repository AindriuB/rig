// Package workflow reads a workflow file and applies it to tasks: which
// states exist, which transitions they allow, the gates a transition must
// pass, the hooks that run after one, and the typed fields tasks and findings
// carry.
//
// The package knows store types only. It reads no git repository and no store
// backend: the branch head and other tasks reach it through Env.
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Who may act in a state.
const (
	WhoAgent = "agent"
	WhoHuman = "human"
)

// Arguments a transition can take.
const (
	TakesSha    = "sha"
	TakesPR     = "pr"
	TakesReason = "reason"
)

// AnyState, as a transition's from, matches every non-final state.
const AnyState = "*"

// Previous, as a transition's target, is the state the task was in before
// its latest transition. The caller resolves it from store.Task.Previous.
const Previous = "$previous"

// Workflow is one parsed workflow file.
type Workflow struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Initial string `json:"initial"`
	// Branch is the branch name template: {short}, {slug} and {id} are
	// replaced by the task's.
	Branch string           `json:"branch,omitempty"`
	States map[string]State `json:"states"`
	// OnNewCommit is the effective state of a task in a sha-bound state
	// whose branch head has moved.
	OnNewCommit    string           `json:"onNewCommit,omitempty"`
	AllTransitions []Transition     `json:"transitions"`
	Fields         map[string]Field `json:"fields,omitempty"`
	Findings       Findings         `json:"findings"`
	Counters       []string         `json:"counters,omitempty"`
	Hooks          []Hook           `json:"hooks,omitempty"`
	// Tracker says how issues map to tasks; nil when the file has none.
	Tracker *Tracker `json:"tracker,omitempty"`
}

// State is one state of a workflow.
type State struct {
	// Action labels what to do next in this state, for rig next.
	Action string `json:"action,omitempty"`
	// Who is WhoAgent (the default when empty) or WhoHuman.
	Who string `json:"who,omitempty"`
	// Final states take no transitions out.
	Final bool `json:"final,omitempty"`
	// ShaBound states are tied to the sha they were entered at.
	ShaBound bool `json:"shaBound,omitempty"`
}

// Transition moves a task from one of From to To.
type Transition struct {
	// From lists the states the transition leaves; AnyState matches every
	// non-final state. In the file it is a string or a list of strings.
	From StateList `json:"from"`
	// To is a state, or Previous.
	To string `json:"to"`
	// Takes names the argument the transition records: TakesSha, TakesPR or
	// TakesReason. Empty takes none.
	Takes string `json:"takes,omitempty"`
	// Count names a counter the transition adds one to.
	Count string `json:"count,omitempty"`
	// Gates are checked, in order, before the transition is written.
	Gates []Gate `json:"gates,omitempty"`
}

// StateList is a list of state names that also reads from a single string.
type StateList []string

// UnmarshalJSON reads a string or a list of strings.
func (l *StateList) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*l = StateList{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return errors.New("want a string or a list of strings")
	}
	*l = list
	return nil
}

// Field types.
const (
	TypeString = "string"
	TypeText   = "text"
	TypeInt    = "int"
	TypeBool   = "bool"
	TypeList   = "list"
	TypeEnum   = "enum"
	TypePaths  = "paths"
	TypeTasks  = "tasks"
)

// Field declares a typed field on tasks or findings.
type Field struct {
	Type string `json:"type"`
	// Context puts the field in rig context's fixed part.
	Context bool `json:"context,omitempty"`
	// Owns marks the paths field rig context keys on.
	Owns bool `json:"owns,omitempty"`
	// Values are an enum's allowed values.
	Values []string `json:"values,omitempty"`
}

// Findings declares the severities and extra fields findings take.
type Findings struct {
	Severities []string         `json:"severities"`
	Fields     map[string]Field `json:"fields,omitempty"`
}

// Hook modes.
const (
	ModeInline = "inline"
	ModeQueued = "queued"
)

// Hook runs a program after an event is written. On lists selectors, any of
// which fires the hook: "transition", "enter:<state>", "leave:<state>",
// "kind:<kind>" (or "kind:*") and "final". Run is an argument vector. Name,
// Mode, Timeout, Env, From and MaxDepth are optional. A hook with Mode
// "queued" is delivered by rig hooks deliver, and From and MaxDepth apply to it.
type Hook struct {
	Name     string    `json:"name,omitempty"`
	On       StateList `json:"on"`
	Run      []string  `json:"run"`
	Mode     string    `json:"mode,omitempty"`
	Timeout  string    `json:"timeout,omitempty"`
	Env      []string  `json:"env,omitempty"`
	From     string    `json:"from,omitempty"`
	MaxDepth *int      `json:"maxDepth,omitempty"`
}

// HookName returns the name of hook i: its own, else "hook-<i+1>".
func (w *Workflow) HookName(i int) string {
	if n := w.Hooks[i].Name; n != "" {
		return n
	}
	return fmt.Sprintf("hook-%d", i+1)
}

// HookWarnings lists what is worth telling the user about the hooks without
// being wrong: unnamed hooks, whose state and messages go by a made-up name.
func (w *Workflow) HookWarnings() []string {
	var out []string
	for i, h := range w.Hooks {
		if h.Name == "" {
			out = append(out, fmt.Sprintf("hooks[%d] has no name; it is called %s", i, w.HookName(i)))
		}
	}
	return out
}

// Gate kinds.
const (
	GateNoOpenFindings = "noOpenFindings"
	GateShaIsHead      = "shaIsHead"
	GateShaIs          = "shaIs"
	GateDependsIn      = "dependsIn"
	GateFieldSet       = "fieldSet"
	GateCounterBelow   = "counterBelow"
	GateCommand        = "command"
)

// DependsField is the tasks field a dependsIn gate reads.
const DependsField = "depends"

// Gate is one check a transition must pass. In the file it is an object with
// one key, the gate's kind, whose value is the gate's argument.
type Gate struct {
	Kind string
	// Severities: noOpenFindings.
	Severities []string
	// State: shaIs.
	State string
	// States: dependsIn.
	States []string
	// Field: fieldSet.
	Field string
	// Counter and Max: counterBelow.
	Counter string
	Max     int
	// Argv: command.
	Argv []string

	raw     json.RawMessage
	problem string // why the gate is malformed; reported by Validate
}

// UnmarshalJSON reads a gate object. A gate of an unknown kind or with a
// malformed argument parses, and Validate reports it; only a value that is
// not an object is an error here.
func (g *Gate) UnmarshalJSON(data []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return errors.New("a gate is an object")
	}
	*g = Gate{raw: append(json.RawMessage(nil), data...)}
	if len(obj) != 1 {
		g.problem = fmt.Sprintf("a gate has exactly one key, got %d", len(obj))
		return nil
	}
	var arg json.RawMessage
	for k, v := range obj {
		g.Kind, arg = k, v
	}
	bad := func(want string) error {
		g.problem = fmt.Sprintf("%s takes %s", g.Kind, want)
		return nil
	}
	switch g.Kind {
	case GateNoOpenFindings:
		var v struct {
			Severity StateList `json:"severity"`
		}
		if strict(arg, &v) != nil || len(v.Severity) == 0 {
			return bad(`{"severity": [severities]}`)
		}
		g.Severities = v.Severity
	case GateShaIsHead:
		var v bool
		if strict(arg, &v) != nil || !v {
			return bad("true")
		}
	case GateShaIs:
		if strict(arg, &g.State) != nil || g.State == "" {
			return bad("a state name")
		}
	case GateDependsIn:
		var v StateList
		if strict(arg, &v) != nil || len(v) == 0 {
			return bad("a list of states")
		}
		g.States = v
	case GateFieldSet:
		if strict(arg, &g.Field) != nil || g.Field == "" {
			return bad("a field name")
		}
	case GateCounterBelow:
		var v struct {
			Counter string `json:"counter"`
			Max     int    `json:"max"`
		}
		if strict(arg, &v) != nil || v.Counter == "" || v.Max < 1 {
			return bad(`{"counter": name, "max": a positive integer}`)
		}
		g.Counter, g.Max = v.Counter, v.Max
	case GateCommand:
		if strict(arg, &g.Argv) != nil || len(g.Argv) == 0 || g.Argv[0] == "" {
			return bad("a non-empty argument vector")
		}
	default:
		g.problem = fmt.Sprintf("unknown gate %q", g.Kind)
	}
	return nil
}

// MarshalJSON writes the gate as it was read.
func (g Gate) MarshalJSON() ([]byte, error) {
	if g.raw == nil {
		return nil, errors.New("gate: nothing to marshal")
	}
	return g.raw, nil
}

// Parse reads a workflow file. It fails on malformed JSON and on keys the
// format does not have; the format's rules are Validate's.
func Parse(data []byte) (*Workflow, error) {
	var w Workflow
	if err := strict(data, &w); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	return &w, nil
}

// strict decodes exactly one JSON value into v, refusing unknown keys.
func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// More is false before a stray ']' or '}', so ask for a second value and
	// require end of input.
	if err := dec.Decode(&json.RawMessage{}); err != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// Blocking reports whether findings of severity sev can block a transition:
// true when any noOpenFindings gate names it.
func (w *Workflow) Blocking(sev string) bool {
	for _, tr := range w.AllTransitions {
		for _, g := range tr.Gates {
			if g.Kind == GateNoOpenFindings && g.problem == "" {
				for _, s := range g.Severities {
					if s == sev {
						return true
					}
				}
			}
		}
	}
	return false
}

// Transitions returns the transitions out of from, in file order. Each has
// From set to just from. AnyState matches every state that is not final,
// including one the workflow does not declare, but never yields a transition
// from a state to itself. A Previous target is left for the caller.
func (w *Workflow) Transitions(from string) []Transition {
	if w.States[from].Final {
		return nil
	}
	var out []Transition
	for _, tr := range w.AllTransitions {
		match := false
		for _, f := range tr.From {
			if f == from || (f == AnyState && tr.To != from) {
				match = true
				break
			}
		}
		if match {
			tr.From = StateList{from}
			out = append(out, tr)
		}
	}
	return out
}

// branchPlaceholders are the placeholders a branch template may use.
var branchPlaceholders = []string{"{short}", "{slug}", "{id}", "{key}"}

// isPlaceholderName reports whether s is made of the characters a
// placeholder name uses.
func isPlaceholderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// placeholders returns every {name} in s.
func placeholders(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return out
		}
		if isPlaceholderName(s[i+1 : i+j]) {
			out = append(out, s[i:i+j+1])
		}
		s = s[i+1:]
	}
}
