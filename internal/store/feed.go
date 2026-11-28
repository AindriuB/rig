package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CursorFormat is the cursor file format this rig writes and reads.
const CursorFormat = 1

// Cursor is where a feed reader stopped: per log ref, the tip last read and
// how many events were delivered from it. It is opaque to callers: they load
// it, hand it to Feeder.Feed and save what comes back.
type Cursor struct {
	Format int               `json:"format"`
	Logs   map[string]LogPos `json:"logs"`
}

// LogPos is one log's place in a Cursor.
type LogPos struct {
	Tip   string `json:"tip"`
	Count int    `json:"count"`
}

// NewCursor returns a cursor that has read nothing: every log is new to it.
func NewCursor() *Cursor {
	return &Cursor{Format: CursorFormat, Logs: map[string]LogPos{}}
}

// LoadCursor reads the cursor at path. A file that does not exist returns
// nil and no error.
func LoadCursor(path string) (*Cursor, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cursor: %w", err)
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("read cursor %s: %w", path, err)
	}
	if c.Format != CursorFormat {
		return nil, fmt.Errorf("read cursor %s: format %d is not one this rig reads", path, c.Format)
	}
	if c.Logs == nil {
		c.Logs = map[string]LogPos{}
	}
	return &c, nil
}

// Save writes the cursor to path through a temporary file and a rename, so a
// reader never sees half of it.
func (c *Cursor) Save(path string) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr == nil {
			werr = cerr
		}
		return fmt.Errorf("write cursor: %w", werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write cursor: %w", err)
	}
	return nil
}

// FeedItem is one event the feed delivers. Task is the task folded up to and
// including the event; it is nil for an event of the repository's notes log.
type FeedItem struct {
	Event Event
	Task  *Task
}

// HeldLog is a log the feed did not advance because some of its lines could
// not be read.
type HeldLog struct {
	Name    string // the task's slug, or "notes"
	Skipped int
}

// FeedResult is one scan: the items to deliver, the cursor to save once they
// are delivered, and the logs held back.
type FeedResult struct {
	Items []FeedItem
	Next  *Cursor
	Held  []HeldLog
}

// Feeder is a Store that can list events across all tasks. Feed with a nil
// cursor returns no items and records each readable log at its tip, and a log
// with unreadable lines at its newest readable ancestor, or not at all.
type Feeder interface {
	Feed(ctx context.Context, cur *Cursor) (FeedResult, error)
}
