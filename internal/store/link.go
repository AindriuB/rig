package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var nonKeyChars = regexp.MustCompile(`[^a-z0-9-]+`)

// BranchKey lowercases an issue key and replaces every run of characters
// outside [a-z0-9-] with "-".
func BranchKey(key string) string {
	return nonKeyChars.ReplaceAllString(strings.ToLower(key), "-")
}

// ExpandBranch fills the {short}, {slug}, {id} and {key} placeholders of a
// branch template. {key} is BranchKey of the issue key, or empty for a task
// with no link.
func ExpandBranch(template, short, slug, id, key string) string {
	return strings.NewReplacer(
		"{short}", short, "{slug}", slug, "{id}", id, "{key}", BranchKey(key),
	).Replace(template)
}

// Linked returns the task among tasks linked to the same issue as ext: the
// same kind, host (any case) and key (any case, as Resolve matches it).
func Linked(ext *External, tasks []Task) (Task, bool) {
	if ext == nil {
		return Task{}, false
	}
	for _, t := range tasks {
		if x := t.External; x != nil && x.Kind == ext.Kind && strings.EqualFold(x.Host, ext.Host) && strings.EqualFold(x.Key, ext.Key) {
			return t, true
		}
	}
	return Task{}, false
}

// PrepareCreate checks n against the tasks already in the store, as a
// backend's CreateTask must before it writes. If n's issue is already linked
// it returns that task and an error satisfying errors.Is(err, ErrLinked). A
// Parent that does not resolve is an error; otherwise n.Parent becomes the
// parent's id.
func PrepareCreate(n *NewTask, tasks []Task) (Task, error) {
	if t, ok := Linked(n.External, tasks); ok {
		return t, fmt.Errorf("%s %s is task %s: %w", n.External.Kind, n.External.Key, t.Short, ErrLinked)
	}
	if n.Parent != "" {
		p, err := Resolve(n.Parent, tasks)
		if err != nil {
			return Task{}, fmt.Errorf("new task: parent: %w", err)
		}
		n.Parent = p.ID
	}
	return Task{}, nil
}

// CheckRelink checks the stamped events of task id against the other tasks,
// as a backend's Update must before it writes. An external event that links
// an issue another task already links is an error satisfying
// errors.Is(err, ErrLinked); linking the task to its own current issue is not
// a conflict, and tasks may include the task itself.
func CheckRelink(id string, events []Event, tasks []Task) error {
	var others []Task
	for _, e := range events {
		if e.Kind != KindExternal {
			continue
		}
		var d ExternalData
		if json.Unmarshal(e.Data, &d) != nil || d.Kind == "" {
			continue
		}
		if others == nil {
			for _, t := range tasks {
				if t.ID != id {
					others = append(others, t)
				}
			}
		}
		if t, ok := Linked(&External{Kind: d.Kind, Host: d.Host, Key: d.Key}, others); ok {
			return fmt.Errorf("%s %s is task %s: %w", d.Kind, d.Key, t.Short, ErrLinked)
		}
	}
	return nil
}

// CheckParent reports whether task id may have parent as its parent, given
// every task. An empty parent (clearing it) is always allowed. The parent must
// exist, must not be the task itself and must not have the task among its
// ancestors. parent is a task id.
func CheckParent(id, parent string, tasks []Task) error {
	if parent == "" {
		return nil
	}
	if parent == id {
		return errors.New("a task cannot be its own parent")
	}
	byID := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	if _, ok := byID[parent]; !ok {
		return fmt.Errorf("parent %q: %w", parent, ErrNotFound)
	}
	for seen, cur := 0, parent; cur != "" && seen <= len(tasks); seen++ {
		if cur == id {
			return errors.New("parent would make a cycle")
		}
		cur = byID[cur].Parent
	}
	return nil
}
