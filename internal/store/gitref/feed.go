package gitref

import (
	"context"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/store"
)

var _ store.Feeder = (*Store)(nil)

// Feed lists the events after cur in every log, and returns the cursor to
// save once they are delivered. A nil cur returns no items and a cursor at the
// tips now. Only logs whose tip moved since cur are read. Within a log, items
// come in (seq, id) order; there is no order across logs. A log with lines
// that did not parse is held: it delivers nothing past its cursor, and its
// cursor entry stays as it was.
func (s *Store) Feed(ctx context.Context, cur *store.Cursor) (store.FeedResult, error) {
	if err := ctx.Err(); err != nil {
		return store.FeedResult{}, err
	}
	baseline := cur == nil
	if baseline {
		cur = store.NewCursor()
	}
	refs, err := s.git.ReadRefs(ctx, "refs/rig/")
	if err != nil {
		return store.FeedResult{}, fmt.Errorf("read store: %w", err)
	}
	next := store.NewCursor()
	var changed []string
	tips := map[string]string{}
	for _, r := range refs {
		if r.Name != NotesRef && !strings.HasPrefix(r.Name, TasksPrefix) {
			continue
		}
		if pos, ok := cur.Logs[r.Name]; ok && pos.Tip == r.OID && !baseline {
			next.Logs[r.Name] = pos
			continue
		}
		changed = append(changed, r.Name)
		tips[r.Name] = r.OID
	}
	res := store.FeedResult{Next: next}
	if len(changed) == 0 {
		return res, nil
	}
	specs := make([]string, len(changed))
	for i, name := range changed {
		specs[i] = tips[name] + ":" + LogFile
	}
	blobs, err := s.git.ReadBlobs(ctx, specs)
	if err != nil {
		return store.FeedResult{}, fmt.Errorf("read store: %w", err)
	}
	for i, name := range changed {
		if blobs[i].Missing {
			return store.FeedResult{}, fmt.Errorf("read store: %s has no %s", name, LogFile)
		}
		events, skipped := parseLog(blobs[i].Data)
		old, seen := cur.Logs[name]
		if baseline && skipped == 0 {
			next.Logs[name] = store.LogPos{Tip: tips[name], Count: len(events)}
			continue
		}
		if skipped > 0 {
			label := "notes"
			if name != NotesRef {
				t, _, _, _ := store.Fold(events)
				label = t.Slug
				if label == "" {
					label = strings.TrimPrefix(name, TasksPrefix)
				}
			}
			res.Held = append(res.Held, store.HeldLog{Name: label, Skipped: skipped})
			if baseline {
				pos, ok, err := s.lastReadable(ctx, tips[name])
				if err != nil {
					return store.FeedResult{}, err
				}
				if ok {
					next.Logs[name] = pos
				}
			} else if seen {
				next.Logs[name] = old
			}
			continue
		}
		start := 0
		if seen {
			start = min(old.Count, len(events))
		}
		for j := start; j < len(events); j++ {
			item := store.FeedItem{Event: events[j]}
			if name != NotesRef {
				t, _, _, _ := store.Fold(events[:j+1])
				item.Task = &t
			}
			res.Items = append(res.Items, item)
		}
		next.Logs[name] = store.LogPos{Tip: tips[name], Count: len(events)}
	}
	return res, nil
}

// lastReadable walks the first-parent ancestors of tip and returns the newest
// whose log has no unreadable line, with that log's event count. ok is false
// when there is none.
func (s *Store) lastReadable(ctx context.Context, tip string) (store.LogPos, bool, error) {
	for k := 1; ; k++ {
		oid, found, err := s.git.ResolveRef(ctx, fmt.Sprintf("%s~%d", tip, k))
		if err != nil {
			return store.LogPos{}, false, fmt.Errorf("read store: %w", err)
		}
		if !found {
			return store.LogPos{}, false, nil
		}
		blobs, err := s.git.ReadBlobs(ctx, []string{oid + ":" + LogFile})
		if err != nil {
			return store.LogPos{}, false, fmt.Errorf("read store: %w", err)
		}
		if blobs[0].Missing {
			return store.LogPos{}, false, nil
		}
		events, skipped := parseLog(blobs[0].Data)
		if skipped == 0 {
			return store.LogPos{Tip: oid, Count: len(events)}, true, nil
		}
	}
}
