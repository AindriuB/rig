package store

import (
	"fmt"
	"strings"
)

// shortLen is how many trailing ULID characters a short id starts with.
const shortLen = 4

// ShortID returns prefix and the last four characters of ulid in lower case,
// taking five, six and more while taken reports the candidate in use. If
// every length is taken it returns prefix and the whole id.
func ShortID(prefix, ulid string, taken func(string) bool) string {
	id := strings.ToLower(ulid)
	for n := min(shortLen, len(id)); n < len(id); n++ {
		if s := prefix + id[len(id)-n:]; !taken(s) {
			return s
		}
	}
	return prefix + id
}

// Resolve finds the task ref names among tasks: a full ULID (in any case),
// a short id, a slug or an external issue key (any case). A full ULID match wins outright. Otherwise one match
// is returned, none is ErrNotFound and several is an *AmbiguousError listing
// the matching ids in the order tasks gave them.
func Resolve(ref string, tasks []Task) (Task, error) {
	if ref == "" {
		return Task{}, fmt.Errorf("task %q: %w", ref, ErrNotFound)
	}
	upper := strings.ToUpper(ref)
	for _, t := range tasks {
		if t.ID == upper {
			return t, nil
		}
	}
	var matches []Task
	for _, t := range tasks {
		if t.Short == ref || t.Slug == ref || (t.External != nil && strings.EqualFold(t.External.Key, ref)) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return Task{}, fmt.Errorf("task %q: %w", ref, ErrNotFound)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, t := range matches {
		ids[i] = t.ID
	}
	return Task{}, &AmbiguousError{Ref: ref, Matches: ids}
}
