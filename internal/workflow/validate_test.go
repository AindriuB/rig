package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads and parses testdata/review-loop.json.
func fixture(t *testing.T) *Workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "review-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestParseFixture(t *testing.T) {
	w := fixture(t)
	if w.Name != "review-loop" || w.Version != 1 || w.Initial != "planned" || w.OnNewCommit != "implementing" {
		t.Fatalf("header = %q %d %q %q", w.Name, w.Version, w.Initial, w.OnNewCommit)
	}
	if !w.States["verified"].ShaBound || !w.States["merged"].Final || w.States["ready"].Who != WhoHuman {
		t.Fatalf("states = %+v", w.States)
	}
	if got := w.AllTransitions[8].From; len(got) != 4 || got[3] != "ready" {
		t.Fatalf("list from = %v", got)
	}
	if got := w.AllTransitions[0].From; len(got) != 1 || got[0] != "planned" {
		t.Fatalf("string from = %v", got)
	}
	g := w.AllTransitions[1].Gates[0]
	if g.Kind != GateNoOpenFindings || strings.Join(g.Severities, ",") != "P1,P2,STYLE,SPEC" {
		t.Fatalf("noOpenFindings gate = %+v", g)
	}
	if g := w.AllTransitions[3].Gates[0]; g.Kind != GateCounterBelow || g.Counter != "rounds" || g.Max != 4 {
		t.Fatalf("counterBelow gate = %+v", g)
	}
	if g := w.AllTransitions[5].Gates[0]; g.Kind != GateShaIs || g.State != "verified" {
		t.Fatalf("shaIs gate = %+v", g)
	}
	if f := w.Findings.Fields["route"]; f.Type != TypeEnum || len(f.Values) != 2 {
		t.Fatalf("finding field = %+v", f)
	}
	if ps := w.Validate(); len(ps) != 0 {
		t.Fatalf("fixture problems: %v", ps)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, src := range map[string]string{
		"not json":      `{`,
		"unknown key":   `{"name":"x","states":{},"shabound":true}`,
		"state typo":    `{"states":{"a":{"shaBond":true}}}`,
		"gate not obj":  `{"transitions":[{"from":"a","to":"b","gates":["x"]}]}`,
		"from number":   `{"transitions":[{"from":3,"to":"b"}]}`,
		"trailing":      `{} {}`,
		"stray brace":   `{}}`,
		"stray bracket": `{}]`,
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
}

// valid is a small workflow that breaks no rule; each case below breaks one.
const valid = `{
  "name": "w", "version": 1, "initial": "a", "branch": "t/{short}",
  "states": {"a": {}, "b": {"shaBound": true}, "c": {"final": true}, "s": {}},
  "onNewCommit": "a",
  "transitions": [
    {"from": "a", "to": "b", "takes": "sha", "count": "n", "gates": [{"shaIsHead": true}]},
    {"from": "b", "to": "c"},
    {"from": "*", "to": "s", "takes": "reason"},
    {"from": "s", "to": "$previous"}
  ],
  "fields": {"depends": {"type": "tasks"}, "kind": {"type": "enum", "values": ["x", "y"]}},
  "findings": {"severities": ["hi", "lo"]},
  "counters": ["n"],
  "hooks": [{"on": "enter:s", "run": ["prog", "{short}", "--", "{reason}"]}]
}`

func TestValidate(t *testing.T) {
	base, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if ps := base.Validate(); len(ps) != 0 {
		t.Fatalf("valid workflow has problems: %v", ps)
	}
	keyed, err := Parse([]byte(strings.Replace(valid, `t/{short}`, `t/{key}-{slug}`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if ps := keyed.Validate(); len(ps) != 0 {
		t.Fatalf("branch template with {key} has problems: %v", ps)
	}
	cases := []struct {
		name     string
		old, new string // a replacement in valid
		path     string // the problem expected
	}{
		{"no name", `"name": "w"`, `"name": ""`, "name"},
		{"bad version", `"version": 1`, `"version": 0`, "version"},
		{"initial undeclared", `"initial": "a"`, `"initial": "z"`, "initial"},
		{"bad who", `"a": {}`, `"a": {"who": "robot"}`, "states.a.who"},
		{"final sha-bound", `"c": {"final": true}`, `"c": {"final": true, "shaBound": true}`, "states.c.shaBound"},
		{"onNewCommit missing", `"onNewCommit": "a",`, ``, "onNewCommit"},
		{"onNewCommit undeclared", `"onNewCommit": "a"`, `"onNewCommit": "z"`, "onNewCommit"},
		{"branch placeholder", `t/{short}`, `t/{nope}`, "branch"},
		{"from undeclared", `{"from": "b", "to": "c"}`, `{"from": "z", "to": "c"}`, "transitions[1].from"},
		{"from list undeclared", `{"from": "b", "to": "c"}`, `{"from": ["b", "z"], "to": "c"}`, "transitions[1].from[1]"},
		{"from final", `{"from": "s", "to": "$previous"}`, `{"from": "c", "to": "a"}`, "transitions[3].from"},
		{"to undeclared", `{"from": "b", "to": "c"}`, `{"from": "b", "to": "z"}`, "transitions[1].to"},
		{"bad takes", `"takes": "reason"`, `"takes": "note"`, "transitions[2].takes"},
		{"count undeclared", `"count": "n"`, `"count": "m"`, "transitions[0].count"},
		{"unknown gate", `{"shaIsHead": true}`, `{"shaIsHeadd": true}`, "transitions[0].gates[0]"},
		{"two-key gate", `{"shaIsHead": true}`, `{"shaIsHead": true, "fieldSet": "x"}`, "transitions[0].gates[0]"},
		{"gate bad arg", `{"shaIsHead": true}`, `{"counterBelow": {"counter": "n"}}`, "transitions[0].gates[0]"},
		{"shaIsHead without sha", `"takes": "sha", "count"`, `"count"`, "transitions[0].gates[0].shaIsHead"},
		{"shaIs undeclared", `{"shaIsHead": true}`, `{"shaIs": "z"}`, "transitions[0].gates[0].shaIs"},
		{"severity undeclared", `{"shaIsHead": true}`, `{"noOpenFindings": {"severity": ["hi", "mid"]}}`, "transitions[0].gates[0].noOpenFindings.severity[1]"},
		{"dependsIn undeclared", `{"shaIsHead": true}`, `{"dependsIn": ["c", "z"]}`, "transitions[0].gates[0].dependsIn[1]"},
		{"dependsIn no field", `{"shaIsHead": true}`, `{"shaIsHead": true}, {"dependsIn": ["c"]}`, "transitions[0].gates[1].dependsIn"},
		{"fieldSet undeclared", `{"shaIsHead": true}`, `{"fieldSet": "zz"}`, "transitions[0].gates[0].fieldSet"},
		{"counterBelow undeclared", `{"shaIsHead": true}`, `{"counterBelow": {"counter": "m", "max": 2}}`, "transitions[0].gates[0].counterBelow.counter"},
		{"command placeholder", `{"shaIsHead": true}`, `{"command": ["x", "{who}"]}`, "transitions[0].gates[0].command[1]"},
		{"command embedded", `{"shaIsHead": true}`, `{"command": ["x", "--id={id}"]}`, "transitions[0].gates[0].command[1]"},
		{"bad field type", `{"type": "tasks"}`, `{"type": "task"}`, "fields.depends.type"},
		{"enum no values", `"values": ["x", "y"]`, `"values": []`, "fields.kind.values"},
		{"enum repeated", `"values": ["x", "y"]`, `"values": ["x", "x"]`, "fields.kind.values[1]"},
		{"owns not paths", `{"type": "tasks"}`, `{"type": "tasks", "owns": true}`, "fields.depends.owns"},
		{"builtin field", `"depends": {`, `"title": {"type": "text"}, "depends": {`, "fields.title"},
		{"severity repeated", `["hi", "lo"]`, `["hi", "hi"]`, "findings.severities[1]"},
		{"counter repeated", `"counters": ["n"]`, `"counters": ["n", "n"]`, "counters[1]"},
		{"hook on", `"on": "enter:s"`, `"on": "after:s"`, "hooks[0].on"},
		{"hook state", `"on": "enter:s"`, `"on": "leave:z"`, "hooks[0].on"},
		{"hook empty run", `["prog", "{short}", "--", "{reason}"]`, `[]`, "hooks[0].run"},
		{"hook placeholder", `"{reason}"]`, `"{why}"]`, "hooks[0].run[3]"},
		{"hook name", `"hooks": [{`, `"hooks": [{"name": "Bad_Name", `, "hooks[0].name"},
		{"hook name dup", `"hooks": [{"on": "enter:s", "run": ["prog", "{short}", "--", "{reason}"]}]`, `"hooks": [{"name": "hook-2", "on": "enter:s", "run": ["p"]}, {"on": "enter:s", "run": ["p"]}]`, "hooks[1].name"},
		{"hook mode", `"hooks": [{`, `"hooks": [{"mode": "later", `, "hooks[0].mode"},
		{"hook timeout bad", `"hooks": [{`, `"hooks": [{"timeout": "soon", `, "hooks[0].timeout"},
		{"hook timeout zero", `"hooks": [{`, `"hooks": [{"timeout": "0s", `, "hooks[0].timeout"},
		{"hook timeout long", `"hooks": [{`, `"hooks": [{"timeout": "31s", `, "hooks[0].timeout"},
		{"hook queued timeout long", `"hooks": [{`, `"hooks": [{"mode": "queued", "timeout": "31m", `, "hooks[0].timeout"},
		{"hook env empty", `"hooks": [{`, `"hooks": [{"env": [""], `, "hooks[0].env"},
		{"hook env star", `"hooks": [{`, `"hooks": [{"env": ["*"], `, "hooks[0].env"},
		{"hook from", `"hooks": [{`, `"hooks": [{"from": "then", `, "hooks[0].from"},
		{"hook maxDepth", `"hooks": [{`, `"hooks": [{"maxDepth": 0, `, "hooks[0].maxDepth"},
		{"hook on empty", `"on": "enter:s"`, `"on": []`, "hooks[0].on"},
		{"hook kind unknown", `"on": "enter:s"`, `"on": ["kind:nope"]`, "hooks[0].on"},
		{"hook kind star reason", `"on": "enter:s"`, `"on": "kind:*"`, "hooks[0].run"},
		{"hook kind note sha", `"on": "enter:s", "run": ["prog", "{short}", "--", "{reason}"]`, `"on": ["kind:note"], "run": ["p", "{sha}"]`, "hooks[0].run"},
		{"unreachable", `"s": {}`, `"s": {}, "d": {}`, "states.d"},
		{"duplicate route", `{"from": "b", "to": "c"}`, `{"from": "b", "to": "c"}, {"from": ["a", "b"], "to": "c"}`, "transitions[2]"},
		{"duplicate in one list", `{"from": "b", "to": "c"}`, `{"from": ["b", "b"], "to": "c"}`, "transitions[1]"},
		{"route under *", `{"from": "s", "to": "$previous"}`, `{"from": "s", "to": "$previous"}, {"from": "a", "to": "s", "takes": "reason"}`, "transitions[4]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(valid, c.old) {
				t.Fatalf("%q is not in the valid workflow", c.old)
			}
			src := strings.Replace(valid, c.old, c.new, 1)
			if strings.Contains(c.new, `"dependsIn"`) {
				// Without a depends field of type tasks.
				src = strings.Replace(src, `"depends": {"type": "tasks"}`, `"depends": {"type": "list"}`, 1)
			}
			w, err := Parse([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			ps := w.Validate()
			for _, p := range ps {
				if p.Path == c.path {
					return
				}
			}
			t.Fatalf("no problem at %s; got %v", c.path, ps)
		})
	}
}

func TestTransitions(t *testing.T) {
	w := fixture(t)
	to := func(from string) []string {
		var out []string
		for _, tr := range w.Transitions(from) {
			if len(tr.From) != 1 || tr.From[0] != from {
				t.Fatalf("Transitions(%q) gave from %v", from, tr.From)
			}
			out = append(out, tr.To)
		}
		return out
	}
	cases := map[string]string{
		"planned":  "implementing stopped",
		"reviewed": "verified implementing stopped",
		"ready":    "merged implementing stopped",
		"stopped":  "$previous",
		"merged":   "",
		"gone":     "stopped", // a state the workflow no longer has
	}
	for from, want := range cases {
		if got := strings.Join(to(from), " "); got != want {
			t.Errorf("Transitions(%q) = %q, want %q", from, got, want)
		}
	}
}

func TestBlocking(t *testing.T) {
	w := fixture(t)
	for sev, want := range map[string]bool{"P1": true, "SPEC": true, "P3": false, "NOTE": false, "nope": false} {
		if got := w.Blocking(sev); got != want {
			t.Errorf("Blocking(%q) = %v", sev, got)
		}
	}
}

func TestValidateRefusesOverlappingRoutes(t *testing.T) {
	// The fixture before review round 1: a from-list transition back to
	// implementing that also named reviewed, beside the gated, counted
	// reviewed → implementing.
	data, err := os.ReadFile(filepath.Join("testdata", "review-loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := `{ "from": ["verified", "pr_open", "ci_green", "ready"], "to": "implementing" }`
	old := `{ "from": ["reviewed", "verified", "pr_open", "ci_green", "ready"], "to": "implementing" }`
	if !strings.Contains(string(data), now) {
		t.Fatal("the fixture's from-list transition has changed")
	}
	w, err := Parse([]byte(strings.Replace(string(data), now, old, 1)))
	if err != nil {
		t.Fatal(err)
	}
	ps := w.Validate()
	if len(ps) != 1 || ps[0].Path != "transitions[8]" || !strings.Contains(ps[0].Message, "reviewed → implementing is also taken by transitions[3]") {
		t.Fatalf("problems = %v", ps)
	}
}

func TestQueuedHookNeedsName(t *testing.T) {
	src := strings.Replace(valid, `"hooks": [{"on": "enter:s", "run": ["prog", "{short}", "--", "{reason}"]}]`,
		`"hooks": [{"on": "kind:transition", "run": ["p"], "mode": "queued"}]`, 1)
	w, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ps := w.Validate()
	if len(ps) != 1 || ps[0].Path != "hooks[0].name" {
		t.Fatalf("problems = %v", ps)
	}
}

func TestHookKeysValid(t *testing.T) {
	src := strings.Replace(valid, `"hooks": [{"on": "enter:s", "run": ["prog", "{short}", "--", "{reason}"]}]`,
		`"hooks": [
  {"name": "a-1", "on": ["enter:s", "final", "kind:count", "kind:*"], "run": ["p", "{state}"], "timeout": "20s", "env": ["GH_*", "X"], "from": "now", "maxDepth": 2},
  {"name": "q-1", "on": "kind:transition", "run": ["p", "{sha}"], "mode": "queued", "timeout": "10m"},
  {"on": "leave:s", "run": ["p"]}]`, 1)
	w, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if ps := w.Validate(); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	ws := w.HookWarnings()
	if len(ws) != 2 || !strings.Contains(ws[0], "hooks[1]") || !strings.Contains(ws[0], "not delivered by this version") ||
		!strings.Contains(ws[1], "hook-3") {
		t.Fatalf("warnings = %q", ws)
	}
}
