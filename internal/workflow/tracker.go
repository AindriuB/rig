package workflow

import (
	"fmt"
	"slices"
)

// Neutral attributes a tracker field can map to.
const (
	AttrTitle       = "title"
	AttrDescription = "description"
	AttrAcceptance  = "acceptance"
	AttrPriority    = "priority"
	AttrAssignee    = "assignee"
	AttrLabels      = "labels"
	AttrType        = "type"
)

var trackerAttrs = []string{AttrTitle, AttrDescription, AttrAcceptance, AttrPriority, AttrAssignee, AttrLabels, AttrType}

// Tracker says how issues map to tasks. The whole key is optional.
type Tracker struct {
	Intake TrackerIntake `json:"intake,omitzero"`
	// Map is a rig field name to a neutral attribute. Its keys are the owned
	// set. Nil means the default: title and body.
	Map    map[string]string `json:"map,omitempty"`
	Report TrackerReport     `json:"report,omitzero"`
}

// TrackerIntake holds the states a task may be created in from an issue.
type TrackerIntake struct {
	States []string `json:"states,omitempty"`
}

// TrackerReport holds what `rig tracker report` sends.
type TrackerReport struct {
	// Status is a rig state to the tracker's status name.
	Status map[string]string `json:"status,omitempty"`
}

// IntakeStates returns the states a task may be created in from an issue:
// the workflow's initial state unless the tracker names others.
func (w *Workflow) IntakeStates() []string {
	if w.Tracker != nil && len(w.Tracker.Intake.States) > 0 {
		return slices.Clone(w.Tracker.Intake.States)
	}
	return []string{w.Initial}
}

// OwnedFields returns the rig fields the tracker owns, sorted: the keys of
// the tracker map, or title and body when there is none.
func (w *Workflow) OwnedFields() []string {
	if w.Tracker == nil || w.Tracker.Map == nil {
		return []string{"body", "title"}
	}
	return sortedKeys(w.Tracker.Map)
}

// AttributeFor returns the neutral attribute a rig field maps to, and false
// when the field is not mapped. With no tracker map, title is the title
// attribute and body the description.
func (w *Workflow) AttributeFor(field string) (string, bool) {
	if w.Tracker == nil || w.Tracker.Map == nil {
		switch field {
		case "title":
			return AttrTitle, true
		case "body":
			return AttrDescription, true
		}
		return "", false
	}
	a, ok := w.Tracker.Map[field]
	return a, ok
}

// ReportStatus returns the tracker status name to report for a task that
// entered state, and false when there is none.
func (w *Workflow) ReportStatus(state string) (string, bool) {
	if w.Tracker == nil {
		return "", false
	}
	s, ok := w.Tracker.Report.Status[state]
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// validateTracker reports the tracker key's broken rules.
func (w *Workflow) validateTracker(add addFunc) {
	t := w.Tracker
	if t == nil {
		return
	}
	for i, s := range t.Intake.States {
		path := fmt.Sprintf("tracker.intake.states[%d]", i)
		st, ok := w.States[s]
		switch {
		case !ok:
			add(path, "state %q is not declared", s)
		case st.Final:
			add(path, "state %q is final; intake states cannot be", s)
		}
	}
	if t.Map != nil && len(t.Map) == 0 {
		add("tracker.map", "map is empty; leave it out for the default title and body")
	}
	seen := map[string]string{} // attribute -> first field
	for _, f := range sortedKeys(t.Map) {
		attr := t.Map[f]
		path := "tracker.map." + f
		field, declared := w.Fields[f]
		isText := f == "title" || f == "body"
		switch {
		case !isText && !declared:
			add(path, "%q is not a declared field, title or body", f)
		case declared && field.Owns:
			add(path, "field %q is owns: true, a path glob and not a planning field", f)
		}
		if !slices.Contains(trackerAttrs, attr) {
			add(path, "attribute %q is not one of %v", attr, trackerAttrs)
			continue
		}
		if (attr == AttrLabels || attr == AttrAcceptance) && (isText || (declared && field.Type != TypeList)) {
			add(path, "attribute %q needs a list field; %q is not one", attr, f)
		}
		if attr != AttrTitle && attr != AttrLabels && attr != AttrAcceptance && declared && field.Type != TypeString && field.Type != TypeText {
			add(path, "attribute %q needs a text field; %q is not one", attr, f)
		}
		if prev, dup := seen[attr]; dup {
			add(path, "attribute %q is already mapped from %q", attr, prev)
		} else {
			seen[attr] = f
		}
	}
	for _, s := range sortedKeys(t.Report.Status) {
		path := "tracker.report.status." + s
		if _, ok := w.States[s]; !ok {
			add(path, "state %q is not declared", s)
		}
		if t.Report.Status[s] == "" {
			add(path, "the tracker status name is empty")
		}
	}
}
