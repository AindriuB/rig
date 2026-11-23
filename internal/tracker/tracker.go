// Package tracker is the port between rig's commands and an issue tracker.
// Its types are neutral: nothing here names Jira, GitHub or GitLab. An adapter
// (tracker/exec, jira, github, gitlab, fake) implements Tracker for one
// tracker and translates that tracker's vocabulary into these types.
//
// Every adapter must guarantee:
//   - Its tests make no network call; they run against a stub, a recorded
//     response or a fake process.
//   - No token, password or Authorization header appears in a returned error,
//     a log line or a test failure message.
//   - A method the tracker lacks returns ErrUnsupported and Capabilities
//     reports false for it.
//   - Errors wrap the port sentinels with %w, so errors.Is works.
//   - It passes trackertest.Run.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tracker is what rig needs from an issue tracker.
type Tracker interface {
	// Capabilities reports which methods and features the tracker supports.
	// It contacts the tracker, so it returns ErrAuth when the login does not
	// work.
	Capabilities(ctx context.Context) (Caps, error)
	// Issue returns one issue. It returns ErrNotFound for an unknown key.
	Issue(ctx context.Context, key string) (Issue, error)
	// Search returns issues matching q. It returns ErrUnsupported when
	// Caps.Search is false.
	Search(ctx context.Context, q Query) ([]IssueRef, error)
	// Children returns the issues directly under key. It returns
	// ErrUnsupported when Caps.Children is false.
	Children(ctx context.Context, key string) ([]IssueRef, error)
	// Comment adds a comment to an issue. It returns ErrUnsupported when
	// Caps.Comment is false.
	Comment(ctx context.Context, key string, c Comment) (CommentResult, error)
	// SetStatus moves an issue to status. It returns ErrUnsupported when
	// Caps.SetStatus is false, and ErrNoTransition for a status the tracker
	// cannot reach.
	SetStatus(ctx context.Context, key, status string) (StatusResult, error)
}

var (
	// ErrUnsupported reports that the tracker lacks the method called.
	ErrUnsupported = errors.New("tracker does not support this")
	// ErrNotFound reports that no such issue exists.
	ErrNotFound = errors.New("issue not found")
	// ErrNoTransition reports a status the tracker cannot move the issue to.
	ErrNoTransition = errors.New("no transition to that status")
	// ErrAuth reports missing or rejected credentials. Its wrapping error
	// must never carry the credential.
	ErrAuth = errors.New("tracker authentication failed")
)

// MaxKeyLen is the longest key, in bytes, that ValidateKey accepts.
const MaxKeyLen = 256

// ValidateKey checks that key is non-empty, is valid UTF-8, is at most
// MaxKeyLen bytes, and has none of: whitespace, control or Unicode format
// characters (category Cf, such as U+200B or U+FEFF), '/', '?', '#', '%', or
// "..". Those would change meaning in a URL path or query. Every adapter must
// call ValidateKey on every key before it uses the key. Adapters add their own
// rules.
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("issue key is empty")
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("issue key is longer than %d bytes", MaxKeyLen)
	}
	if !utf8.ValidString(key) {
		return errors.New("issue key is not valid UTF-8")
	}
	if strings.Contains(key, "..") {
		return errors.New("issue key contains \"..\"")
	}
	for _, r := range key {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return fmt.Errorf("issue key contains whitespace or a control character (%U)", r)
		case unicode.Is(unicode.Cf, r):
			return fmt.Errorf("issue key contains a format character (%U)", r)
		case strings.ContainsRune("/?#%", r):
			return fmt.Errorf("issue key contains %q", r)
		}
	}
	return nil
}

// Issue is one tracker issue in rig's neutral shape.
type Issue struct {
	Key         string    `json:"key"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Acceptance  []string  `json:"acceptance"`
	Priority    string    `json:"priority"`
	Assignee    string    `json:"assignee"`
	Labels      []string  `json:"labels"`
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	Parent      *IssueRef `json:"parent"`
	// Rev changes whenever the issue changes on the tracker, including a
	// status change.
	Rev string `json:"rev"`
}

// IssueRef is a short reference to an issue.
type IssueRef struct {
	Key   string `json:"key"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

// Caps says what a tracker supports.
type Caps struct {
	Parent    bool `json:"parent"`
	Children  bool `json:"children"`
	Types     bool `json:"types"`
	Search    bool `json:"search"`
	Comment   bool `json:"comment"`
	SetStatus bool `json:"setStatus"`
	// Levels names the tracker's issue hierarchy, top first (for example
	// epic, story, task). It is empty for a flat tracker.
	Levels []string `json:"levels"`
}

// Query selects issues for Search.
type Query struct {
	// Text is a tracker-native query, such as JQL.
	Text   string   `json:"text"`
	Labels []string `json:"labels"`
	// Limit caps the result count; 0 means the adapter's default.
	Limit int `json:"limit"`
}

// Comment is a comment to post. Body is Markdown; the adapter converts it to
// the tracker's markup.
type Comment struct {
	Body string `json:"body"`
}

// CommentResult identifies the comment Comment created.
type CommentResult struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// StatusResult is the outcome of SetStatus.
type StatusResult struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Changed bool   `json:"changed"`
}
