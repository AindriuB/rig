package store

import (
	"bytes"
	"encoding/json"
)

// Merge returns the union of two logs of the same task, keyed by event id and
// ordered by (Seq, ID). It is commutative: Merge(a, b) equals Merge(b, a).
// Two events with the same id should be identical; if they are not, the one
// whose JSON sorts first is kept, so the choice does not depend on order.
func Merge(a, b []Event) []Event {
	byID := make(map[string]Event, len(a)+len(b))
	for _, log := range [][]Event{a, b} {
		for _, e := range log {
			if have, ok := byID[e.ID]; ok && !eventJSONLess(e, have) {
				continue
			}
			byID[e.ID] = e
		}
	}
	out := make([]Event, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	SortEvents(out)
	return out
}

func eventJSONLess(a, b Event) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		// Unmarshallable data cannot be compared; fall back to the raw payload.
		return bytes.Compare(a.Data, b.Data) < 0
	}
	return bytes.Compare(ja, jb) < 0
}
