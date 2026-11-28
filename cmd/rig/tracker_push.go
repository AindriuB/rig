package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	"github.com/AindriuB/rig/internal/workflow"
)

// pushSettle is how long after an earlier push's intent a retry with no
// search hit waits before it creates, so the tracker's search index can catch
// up with an issue that push made.
const pushSettle = 2 * time.Minute

// errPushBusy means another push holds the task's lock.
var errPushBusy = errors.New("another push of this task is under way")

// pushRepoName is the label-safe name of the repository's main directory:
// lowercased, other characters turned to "-", runs merged, trimmed, at most 24.
func pushRepoName(common string) string {
	dir := filepath.Dir(filepath.Clean(common))
	if filepath.Base(common) != ".git" { // a bare repository
		dir = strings.TrimSuffix(filepath.Base(common), ".git")
	} else {
		dir = filepath.Base(dir)
	}
	return pushLabelPart(dir, 24)
}

func pushLabelPart(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// pushMarker is the label that finds a task's issue again:
// rig/<repo>/<slug>-<h>, where <h> is the first 8 hex characters of the
// SHA-256 of the task's ULID, so two tasks never share a label. A long slug is
// shortened so the whole label stays within GitHub's 50-character limit.
func pushMarker(repo string, t store.Task) string {
	slug := t.Slug
	if slug == "" {
		slug = t.Short
	}
	sum := sha256.Sum256([]byte(t.ID))
	suffix := "-" + hex.EncodeToString(sum[:])[:8]
	prefix := "rig/" + repo + "/"
	keep := 50 - len(prefix) - len(suffix)
	if keep < 0 {
		keep = 0
	}
	if len(slug) > keep {
		slug = slug[:keep]
	}
	return prefix + strings.TrimRight(slug, "-") + suffix
}

// pushLock is a kernel advisory lock on one task's push lock file. The file
// is never deleted; the kernel drops the lock when the holder closes it or dies.
type pushLock struct{ f *os.File }

// tryPushLock takes the lock for task without waiting: errPushBusy when
// another holder has it.
func tryPushLock(ctx context.Context, w *work, task string) (*pushLock, error) {
	common, err := w.repo.CommonDir(ctx)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(common, "rig", "push")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return tryPushLockAt(filepath.Join(dir, task+".lock"))
}

func tryPushLockAt(path string) (*pushLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("opening %s: %w", path, err)
		}
		return nil, err
	}
	if err := pushLockFileNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errPushBusy) {
			return nil, err
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return &pushLock{f: f}, nil
}

// release unlocks and closes. The file stays.
func (l *pushLock) release() {
	if l == nil || l.f == nil {
		return
	}
	pushUnlockFile(l.f)
	_ = l.f.Close()
	l.f = nil
}

const pushUsage = "usage: rig tracker push <task> [--tree] [--dry-run] [--actor <name>] [--json] [--repo <dir>]"

// pushPayload is the exact set of fields a push sends for one task.
type pushPayload struct {
	Type       string   `json:"type,omitempty"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Acceptance []string `json:"acceptance,omitempty"`
	Labels     []string `json:"labels"`
	ParentKey  string   `json:"parentKey,omitempty"`
}

// pushItem is one task in `rig tracker push --json`. Status is created,
// linked (found by its marker), created-with-error, skipped, would-create or
// not-pushed.
type pushItem struct {
	Task    string       `json:"task"`
	Title   string       `json:"title"`
	Key     string       `json:"key,omitempty"`
	URL     string       `json:"url,omitempty"`
	Type    string       `json:"type,omitempty"`
	Parent  string       `json:"parent,omitempty"`
	Status  string       `json:"status"`
	Error   string       `json:"error,omitempty"`
	Payload *pushPayload `json:"payload,omitempty"`
}

// pushOut is `rig tracker push --json`.
type pushOut struct {
	DryRun    bool       `json:"dryRun,omitempty"`
	Pushed    []pushItem `json:"pushed"`
	Skipped   []pushItem `json:"skipped"`
	NotPushed []pushItem `json:"notPushed"`
}

// pushStep is one unlinked task a push will create an issue for.
type pushStep struct {
	task   store.Task
	depth  int
	typ    string // issue type to send; "" sends none
	marker string
	// parentTask is the id of the parent task whose issue this run creates.
	parentTask string
	// parentKey and parentType are the parent's issue when it is linked.
	parentKey, parentType string
	// resume marks a linked task whose parent attach may be incomplete: it
	// is verified or attached, never created.
	resume bool
}

// attachIncomplete reports whether linked task t has a parent link that push
// has not confirmed, and returns that parent's issue key. It makes no tracker
// call. The latest keyed push record for this tracker must name the current
// link's key (so push wrote that link), the parent must be linked, the tracker
// must support parents, and ParentKey must differ from the parent's key. It
// returns the parent's key and that record's marker.
func attachIncomplete(ctx context.Context, w *work, spec trackerSpec, t store.Task, byID map[string]store.Task) (string, string, bool, error) {
	if t.External == nil || t.Parent == "" || linkMismatch(t.External, spec) != "" {
		return "", "", false, nil
	}
	p, ok := byID[t.Parent]
	if !ok || p.External == nil || linkMismatch(p.External, spec) != "" || p.External.Key == "" {
		return "", "", false, nil
	}
	if strings.EqualFold(t.External.ParentKey, p.External.Key) {
		return "", "", false, nil
	}
	log, err := w.st.Events(ctx, t.ID)
	if err != nil {
		return "", "", false, err
	}
	marker, ok := keyedRecordFor(log, spec.Link, t.External.Key)
	if !ok {
		return "", "", false, nil
	}
	return p.External.Key, marker, true, nil
}

// keyedRecordFor returns the marker of the latest keyed tracker-push record
// for the tracker, when it names issue key (in any case). Push wrote the
// current link only if that record exists; a hand link has none.
func keyedRecordFor(log []store.Event, l trackerLink, key string) (string, bool) {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind != store.KindTrackerPush || log[i].V != store.Version {
			continue
		}
		var d store.TrackerPushData
		if json.Unmarshal(log[i].Data, &d) != nil || d.Marker == "" || d.Key == "" {
			continue
		}
		if d.Kind == l.Kind && strings.EqualFold(d.Host, l.Host) && strings.EqualFold(d.Project, l.Project) {
			return d.Marker, strings.EqualFold(d.Key, key)
		}
	}
	return "", false
}

// pushSelect returns the tasks to consider, parents first, with each one's
// depth from the pushed root: the task alone, or with tree its subtree.
func pushSelect(root store.Task, all []store.Task, tree bool) ([]store.Task, []int) {
	byID := map[string]store.Task{root.ID: root}
	set := []store.Task{root}
	if tree {
		kids := map[string][]store.Task{}
		for i := len(all) - 1; i >= 0; i-- { // oldest first
			kids[all[i].Parent] = append(kids[all[i].Parent], all[i])
		}
		for q := []string{root.ID}; len(q) > 0; q = q[1:] {
			for _, c := range kids[q[0]] {
				if _, seen := byID[c.ID]; seen {
					continue
				}
				byID[c.ID] = c
				set = append(set, c)
				q = append(q, c.ID)
			}
		}
	}
	views := make([]workflow.TaskView, len(set))
	for i, t := range set {
		views[i] = workflow.TaskView{ID: t.ID, Parent: t.Parent}
	}
	views[0].Parent = ""
	ordered, depths := treeViews(views)
	out := make([]store.Task, len(ordered))
	for i, v := range ordered {
		out[i] = byID[v.ID]
	}
	return out, depths
}

// acceptanceOf is the task's acceptance criteria: the field the workflow's
// tracker map sends as the acceptance attribute.
func acceptanceOf(wf *workflow.Workflow, t store.Task) []string {
	if wf.Tracker == nil {
		return nil
	}
	for _, name := range sortedKeys(wf.Tracker.Map) {
		if wf.Tracker.Map[name] != workflow.AttrAcceptance {
			continue
		}
		switch v := t.Fields[name].(type) {
		case string:
			var out []string
			for _, l := range strings.Split(v, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					out = append(out, l)
				}
			}
			return out
		case []string:
			return slices.Clone(v)
		case []any:
			var out []string
			for _, x := range v {
				if s, ok := x.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// pushIntent returns the latest tracker-push intent (a record without Key) in
// log for the tracker, and the time it was recorded.
func pushIntent(log []store.Event, l trackerLink) (store.TrackerPushData, time.Time, bool) {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind != store.KindTrackerPush || log[i].V != store.Version {
			continue
		}
		var d store.TrackerPushData
		if json.Unmarshal(log[i].Data, &d) != nil || d.Marker == "" || d.Key != "" {
			continue
		}
		if d.Kind == l.Kind && strings.EqualFold(d.Host, l.Host) && strings.EqualFold(d.Project, l.Project) {
			return d, log[i].Time, true
		}
	}
	return store.TrackerPushData{}, time.Time{}, false
}

// trackerPush implements `rig tracker push`. Exit codes: 0 pushed (or nothing
// to push); 1 refused before any write (a type, depth or tracker-type rule);
// 2 any other error, including a create that failed part way.
func (a *app) trackerPush(ctx context.Context, args []string) int {
	const cmd = "tracker push"
	fs := newFlags()
	actorOK := a.actorFlags(fs)
	repo := fs.str("--repo")
	tree, dry, asJSON := fs.boolean("--tree"), fs.boolean("--dry-run"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail(cmd, "%s", pushUsage)
	}
	if err := actorOK(); err != nil {
		return a.fail(cmd, "%v", err)
	}
	w, err := a.openWorkPlain(ctx, *repo, true)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	root, err := w.st.Task(ctx, pos[0])
	if err != nil {
		return a.storeFail(cmd, err)
	}
	all, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return a.storeFail(cmd, err)
	}
	byID := map[string]store.Task{}
	for _, t := range all {
		byID[t.ID] = t
	}

	// Step 1: select.
	selected, depths := pushSelect(root, all, *tree)
	out := pushOut{DryRun: *dry, Pushed: []pushItem{}, Skipped: []pushItem{}, NotPushed: []pushItem{}}
	var steps []*pushStep
	inRun := map[string]*pushStep{}
	var refusals []string
	refuse := func(t store.Task, format string, args ...any) {
		refusals = append(refusals, fmt.Sprintf("task %s: %s", t.Short, fmt.Sprintf(format, args...)))
	}
	levels := w.wf.PushLevels()
	common, err := w.repo.CommonDir(ctx)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	repoName := pushRepoName(common)
	// One level rule for every task: its own type field, then its own linked
	// issue's type, then one below its parent's level (decided by this same
	// rule), else the first level. idx is the position in levels; -1 with a
	// message means the level must come from a type outside levels.
	inSel := map[string]bool{}
	for _, t := range selected {
		inSel[t.ID] = true
	}
	type lvl struct {
		idx int
		bad string
	}
	memo := map[string]lvl{}
	levelIndex := func(name string) int {
		for li, l := range levels {
			if l == name {
				return li
			}
		}
		return -1
	}
	var levelOf func(t store.Task, seen map[string]bool) lvl
	levelOf = func(t store.Task, seen map[string]bool) lvl {
		if r, ok := memo[t.ID]; ok {
			return r
		}
		var r lvl
		own, _ := t.Fields["type"].(string)
		switch {
		case own != "":
			r = lvl{idx: levelIndex(own)}
		case t.External != nil && t.External.Type != "":
			r = lvl{idx: levelIndex(t.External.Type)}
		default:
			p, ok := byID[t.Parent]
			if !ok || t.Parent == "" || seen[p.ID] || (p.External == nil && !inSel[p.ID]) {
				r = lvl{}
				break
			}
			seen[t.ID] = true
			pl := levelOf(p, seen)
			switch {
			case pl.bad != "":
				r = pl
			case pl.idx < 0:
				pt, _ := p.Fields["type"].(string)
				if pt == "" && p.External != nil {
					pt = p.External.Type
				}
				r = lvl{idx: -1, bad: fmt.Sprintf("parent task %s has type %q, which is not in tracker.push.levels", p.Short, pt)}
			default:
				r = lvl{idx: pl.idx + 1}
			}
		}
		memo[t.ID] = r
		return r
	}
	for i, t := range selected {
		if t.External != nil {
			pk, marker, resume, err := attachIncomplete(ctx, w, spec, t, byID)
			if err != nil {
				return a.storeFail(cmd, err)
			}
			if resume {
				steps = append(steps, &pushStep{task: t, depth: depths[i], marker: marker, parentKey: pk, resume: true})
				continue
			}
			out.Skipped = append(out.Skipped, pushItem{Task: t.Short, Title: t.Title, Key: t.External.Key, Type: t.External.Type, Status: "skipped"})
			continue
		}
		s := &pushStep{task: t, depth: depths[i], marker: pushMarker(repoName, t)}
		// Steps 2 and 3: the type.
		own, _ := t.Fields["type"].(string)
		switch {
		case own != "":
			if w.wf.PushTypeAllowed(own) {
				s.typ = own
			} else {
				refuse(t, "type %q is not in tracker.push.types", own)
			}
		default:
			lv := levelOf(t, map[string]bool{})
			switch {
			case lv.bad != "":
				refuse(t, "%s", lv.bad)
			case lv.idx < len(levels) && lv.idx >= 0:
				s.typ = levels[lv.idx]
			case len(levels) == 0:
				refuse(t, "the workflow has no tracker.push.levels to choose a type from")
			default:
				refuse(t, "depth %d is beyond tracker.push.levels (%d levels)", lv.idx, len(levels))
			}
		}
		// Step 5: the parent.
		if p, ok := byID[t.Parent]; ok && t.Parent != "" {
			switch {
			case p.External != nil:
				if msg := linkMismatch(p.External, spec); msg != "" {
					refuse(t, "parent %s is %s", p.Short, msg)
				}
				s.parentKey, s.parentType = p.External.Key, p.External.Type
			case inRun[p.ID] != nil:
				s.parentTask = p.ID
			default:
				refuse(t, "its parent task %s is not linked to an issue; push it first, or use --tree", p.Short)
			}
		}
		steps = append(steps, s)
		inRun[t.ID] = s
	}
	if len(steps) == 0 {
		return a.pushReport(cmd, out, *asJSON, false)
	}

	// Step 4: check every type against the tracker, before any write.
	caps, err := tr.Capabilities(ctx)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	if !caps.Parent {
		// No parent links here, so nothing to resume.
		kept := steps[:0]
		for _, s := range steps {
			if s.resume {
				x := s.task.External
				out.Skipped = append(out.Skipped, pushItem{Task: s.task.Short, Title: s.task.Title, Key: x.Key, Type: x.Type, Status: "skipped"})
				continue
			}
			kept = append(kept, s)
		}
		steps = kept
		if len(steps) == 0 {
			return a.pushReport(cmd, out, *asJSON, false)
		}
	}
	creating := 0
	for _, s := range steps {
		if !s.resume {
			creating++
		}
	}
	if creating > 0 && !caps.Create {
		return a.refusePush(cmd, []string{"this tracker cannot create issues"})
	}
	var types []tracker.IssueType
	typesKnown := true
	if creating > 0 {
		var err error
		types, err = tr.IssueTypes(ctx)
		switch {
		case errors.Is(err, tracker.ErrUnsupported):
			typesKnown = false
			fmt.Fprintf(a.stderr, "rig %s: warning: this tracker lists no issue types; sending no type\n", cmd)
		case err != nil:
			return a.fail(cmd, "issue types: %v", err)
		}
	}
	level := map[string]*int{}
	for _, it := range types {
		level[it.Name] = it.Level
	}
	for _, s := range steps {
		if s.resume {
			continue
		}
		if !typesKnown {
			s.typ = ""
			continue
		}
		if s.typ == "" {
			continue // already refused
		}
		lv, known := level[s.typ]
		if !known {
			refuse(s.task, "the tracker has no issue type %q", s.typ)
			continue
		}
		parentType := s.parentType
		if p := inRun[s.parentTask]; p != nil {
			parentType = p.typ
		}
		if plv, ok := level[parentType]; ok && parentType != "" && lv != nil && plv != nil && *lv >= *plv {
			refuse(s.task, "type %q is not below its parent's type %q", s.typ, parentType)
		}
	}
	if len(refusals) > 0 {
		return a.refusePush(cmd, refusals)
	}

	withAcceptance := spec.Jira.AcceptanceField != ""
	payloadOf := func(s *pushStep, parentKey string) tracker.NewIssue {
		ni := tracker.NewIssue{Type: s.typ, Title: s.task.Title, Body: s.task.Body, Labels: []string{s.marker}, ParentKey: parentKey}
		if withAcceptance {
			ni.Acceptance = acceptanceOf(w.wf, s.task)
		}
		return ni
	}
	toPayload := func(ni tracker.NewIssue) *pushPayload {
		return &pushPayload{Type: ni.Type, Title: ni.Title, Body: ni.Body, Acceptance: ni.Acceptance, Labels: ni.Labels, ParentKey: ni.ParentKey}
	}

	if *dry {
		for _, s := range steps {
			if s.resume {
				x := s.task.External
				out.Pushed = append(out.Pushed, pushItem{Task: s.task.Short, Title: s.task.Title, Key: x.Key, Type: x.Type, Parent: s.parentKey, Status: "would-verify"})
				continue
			}
			item := pushItem{Task: s.task.Short, Title: s.task.Title, Type: s.typ, Status: "would-create", Parent: s.parentKey}
			if p := inRun[s.parentTask]; p != nil {
				item.Parent = "(new issue of " + p.task.Short + ")"
			}
			item.Payload = toPayload(payloadOf(s, s.parentKey))
			out.Pushed = append(out.Pushed, item)
		}
		return a.pushReport(cmd, out, *asJSON, false)
	}

	// Steps 6 to 9, one task at a time, parents first. The first failure stops
	// the run; what was created stays linked.
	keys := map[string]string{}
	failed := false
	stop := func(i int, s *pushStep, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(a.stderr, "rig %s: task %s: %s\n", cmd, s.task.Short, msg)
		failed = true
		for _, r := range steps[i:] {
			item := pushItem{Task: r.task.Short, Title: r.task.Title, Type: r.typ, Status: "not-pushed"}
			if r == s {
				item.Error = msg
			}
			out.NotPushed = append(out.NotPushed, item)
		}
	}
	var held *pushLock
	release := func() { held.release(); held = nil }
	defer release()
	for i, s := range steps {
		release() // the previous task's lock, held through its step 9
		parentKey := s.parentKey
		if s.parentTask != "" {
			parentKey = keys[s.parentTask]
		}
		item := pushItem{Task: s.task.Short, Title: s.task.Title, Type: s.typ, Parent: parentKey}

		// Step 5b: the task's lock, so a push that finds an intent without a
		// link knows its writer is dead.
		lk, err := tryPushLock(ctx, w, s.task.ID)
		if errors.Is(err, errPushBusy) {
			stop(i, s, "another push of this task is under way; not creating a second issue")
			break
		}
		if err != nil {
			stop(i, s, "lock: %v", err)
			break
		}
		held = lk
		cur, err := w.st.Task(ctx, s.task.ID)
		if err != nil {
			stop(i, s, "%v", err)
			break
		}
		if s.resume {
			x := cur.External
			if x == nil || !strings.EqualFold(x.Key, s.task.External.Key) || strings.EqualFold(x.ParentKey, s.parentKey) {
				if x != nil {
					keys[s.task.ID] = x.Key
					out.Skipped = append(out.Skipped, pushItem{Task: s.task.Short, Title: s.task.Title, Key: x.Key, Type: x.Type, Status: "skipped"})
				}
				continue
			}
			keys[s.task.ID] = x.Key
			item.Key, item.URL, item.Type, item.Parent = x.Key, x.URL, x.Type, s.parentKey
			status, rerr := a.pushResume(ctx, w, tr, s, cur)
			item.Status = status
			out.Pushed = append(out.Pushed, item)
			if rerr != nil {
				out.Pushed[len(out.Pushed)-1].Error = rerr.Error()
				fmt.Fprintf(a.stderr, "rig %s: task %s: %s: %v\n", cmd, s.task.Short, status, rerr)
				failed = true
				for _, r := range steps[i+1:] {
					out.NotPushed = append(out.NotPushed, pushItem{Task: r.task.Short, Title: r.task.Title, Type: r.typ, Status: "not-pushed"})
				}
				break
			}
			continue
		}
		if cur.External != nil {
			keys[s.task.ID] = cur.External.Key
			out.Skipped = append(out.Skipped, pushItem{Task: s.task.Short, Title: s.task.Title, Key: cur.External.Key, Type: cur.External.Type, Status: "skipped"})
			continue
		}

		// Steps 6 and 7: the intent, and a retry's search for what it made.
		log, err := w.st.Events(ctx, s.task.ID)
		if err != nil {
			stop(i, s, "%v", err)
			break
		}
		intent, intentAt, retry := pushIntent(log, spec.Link)
		// Every Create attempt first appends a fresh intent, under the task's
		// lock, so pushSettle runs from the latest attempt.
		appendIntent := func() error {
			ev, err := store.NewEvent(store.KindTrackerPush, store.TrackerPushData{
				Marker: s.marker, Kind: spec.Link.Kind, Host: spec.Link.Host, Project: spec.Link.Project,
			})
			if err != nil {
				return err
			}
			_, err = w.st.Update(ctx, s.task.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
				if cur.External != nil {
					return nil, errors.New("the task was linked during the push")
				}
				return []store.Event{ev}, nil
			})
			return err
		}
		if !retry {
			if err := appendIntent(); err != nil {
				stop(i, s, "record the intent: %v", err)
				break
			}
		} else {
			refs, err := tr.Search(ctx, tracker.Query{Labels: []string{intent.Marker}, Limit: 3})
			if errors.Is(err, tracker.ErrUnsupported) {
				stop(i, s, "an earlier push may have created an issue, and this tracker cannot search for it by %s; not creating another", intent.Marker)
				break
			}
			if err != nil {
				stop(i, s, "search for %s: %v", intent.Marker, err)
				break
			}
			if len(refs) > 1 {
				stop(i, s, "%d issues carry %s; link the right one with rig task link", len(refs), intent.Marker)
				break
			}
			if len(refs) == 1 {
				found := tracker.Issue{Key: refs[0].Key, URL: refs[0].URL, Type: refs[0].Type}
				if err := checkIssueKey("search result", found.Key); err != nil {
					stop(i, s, "%v", err)
					break
				}
				if is, err := tr.Issue(ctx, found.Key); err == nil && strings.EqualFold(is.Key, found.Key) {
					found = is
				} else {
					found.Rev = ""
				}
				if err := a.pushRecord(ctx, w, spec, s, found, found.Rev != "", ""); err != nil {
					stop(i, s, "found %s but could not link it: %v", found.Key, err)
					break
				}
				keys[s.task.ID] = found.Key
				item.Key, item.URL, item.Type, item.Status = found.Key, found.URL, found.Type, "linked"
				out.Pushed = append(out.Pushed, item)
				continue
			}
			// Zero hits: the earlier push made nothing, or the tracker does
			// not list it yet.
			if age := a.now().Sub(intentAt); age < pushSettle {
				stop(i, s, "an earlier push ended %s ago; the tracker may not list its issue yet; re-run after %s",
					age.Round(time.Second), intentAt.Add(pushSettle).UTC().Format(time.RFC3339))
				break
			}
			fmt.Fprintf(a.stderr, "rig %s: warning: task %s: no issue carries %s, so creating one; a duplicate is possible if the tracker was slow to list the earlier one\n", cmd, s.task.Short, intent.Marker)
			if err := appendIntent(); err != nil {
				stop(i, s, "record the intent: %v", err)
				break
			}
		}

		// Step 8: create.
		ni := payloadOf(s, parentKey)
		issue, cerr := tr.Create(ctx, ni)
		if cerr != nil {
			if issue.Key == "" {
				stop(i, s, "create: %v", cerr)
				break
			}
			// The issue exists. Link it, so a re-run does not make another.
			item.Key, item.URL, item.Status, item.Error = issue.Key, issue.URL, "created-with-error", cerr.Error()
			if errors.Is(cerr, tracker.ErrNotAttached) {
				item.Status = "created-not-attached"
			}
			if err := checkIssueKey("created issue", issue.Key); err != nil {
				stop(i, s, "%v", err)
				break
			}
			if err := a.pushRecord(ctx, w, spec, s, issue, issue.Rev != "", ""); err != nil {
				stop(i, s, "created %s (%v) but could not link it: %v", issue.Key, cerr, err)
				break
			}
			keys[s.task.ID] = issue.Key
			out.Pushed = append(out.Pushed, item)
			fmt.Fprintf(a.stderr, "rig %s: task %s: created %s, but: %v\n", cmd, s.task.Short, issue.Key, cerr)
			failed = true
			for _, r := range steps[i+1:] {
				out.NotPushed = append(out.NotPushed, pushItem{Task: r.task.Short, Title: r.task.Title, Type: r.typ, Status: "not-pushed"})
			}
			break
		}
		// Step 9: record.
		if err := checkIssueKey("created issue", issue.Key); err != nil {
			stop(i, s, "%v", err)
			break
		}
		if err := a.pushRecord(ctx, w, spec, s, issue, true, parentKey); err != nil {
			stop(i, s, "created %s but could not link it: %v; a re-run finds it by %s", issue.Key, err, s.marker)
			break
		}
		keys[s.task.ID] = issue.Key
		item.Key, item.URL, item.Status = issue.Key, issue.URL, "created"
		if issue.Type != "" {
			item.Type = issue.Type
		}
		out.Pushed = append(out.Pushed, item)
	}
	return a.pushReport(cmd, out, *asJSON, failed)
}

// refusePush prints the refusals, one per line, and returns exit 1.
func (a *app) refusePush(cmd string, refusals []string) int {
	for _, r := range refusals {
		fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, r)
	}
	fmt.Fprintf(a.stderr, "rig %s: nothing was created\n", cmd)
	return 1
}

// pushRecord links the task to the issue and, with snapshot, records the
// tracker-sync snapshot with the issue's revision, in one write. parentKey is
// recorded only when the parent link is known complete.
func (a *app) pushRecord(ctx context.Context, w *work, spec trackerSpec, s *pushStep, issue tracker.Issue, snapshot bool, parentKey string) error {
	owned := w.wf.OwnedFields()
	typ := issue.Type
	if typ == "" {
		typ = s.typ
	}
	link, err := store.NewEvent(store.KindExternal, store.ExternalData{
		Kind: spec.Link.Kind, Host: spec.Link.Host, Project: spec.Link.Project, Key: issue.Key,
		URL: issue.URL, Type: typ, Owned: owned, ParentKey: parentKey,
	})
	if err != nil {
		return err
	}
	// The keyed record follows the link in the same update: it marks the link
	// as push's own, and with Parent sets ParentKey on fold.
	rec, err := store.NewEvent(store.KindTrackerPush, store.TrackerPushData{
		Marker: s.marker, Kind: spec.Link.Kind, Host: spec.Link.Host, Project: spec.Link.Project,
		Key: issue.Key, Parent: parentKey,
	})
	if err != nil {
		return err
	}
	events := []store.Event{link, rec}
	if snapshot {
		sync, err := store.NewEvent(store.KindTrackerSync, store.TrackerSyncData{Rev: issue.Rev, Values: ownedValues(w.wf, issue, owned)})
		if err != nil {
			return err
		}
		events = append(events, sync)
	}
	_, err = w.st.Update(ctx, s.task.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
		if cur.External != nil {
			return nil, errors.New("the task was linked during the push")
		}
		return events, nil
	})
	return err
}

// pushResume finishes the parent link of a linked task whose attach is not
// recorded, without creating anything. It returns the item status and, for a
// failure, the error.
func (a *app) pushResume(ctx context.Context, w *work, tr tracker.Tracker, s *pushStep, cur store.Task) (string, error) {
	key, parent := cur.External.Key, s.parentKey
	issue, err := tr.Issue(ctx, key)
	if err != nil {
		return "not-verified", fmt.Errorf("read %s: %w", key, err)
	}
	status := "verified"
	switch {
	case issue.Parent != nil && strings.EqualFold(issue.Parent.Key, parent):
	case issue.Parent != nil:
		return "wrong-parent", fmt.Errorf("%s is under %s on the tracker, not %s; not moving it", key, issue.Parent.Key, parent)
	default:
		att, ok := tr.(tracker.ParentAttacher)
		if !ok {
			return "attach-failed", fmt.Errorf("this tracker cannot attach issues to a parent; attach %s to %s on the tracker, then re-run", key, parent)
		}
		if err := att.AttachParent(ctx, key, parent); err != nil {
			return "attach-failed", fmt.Errorf("attach %s to %s: %w", key, parent, err)
		}
		issue, err = tr.Issue(ctx, key)
		if err != nil {
			return "not-verified", fmt.Errorf("attached %s to %s, but reading it back failed: %w", key, parent, err)
		}
		if issue.Parent == nil || !strings.EqualFold(issue.Parent.Key, parent) {
			return "not-verified", fmt.Errorf("attached %s to %s, but the tracker does not show that parent", key, parent)
		}
		status = "attached"
	}
	// No external event: a link event would reset the history `rig tracker
	// report` checks for duplicates. The keyed record sets ParentKey on fold.
	ext := cur.External
	rec, err := store.NewEvent(store.KindTrackerPush, store.TrackerPushData{
		Marker: s.marker, Kind: ext.Kind, Host: ext.Host, Project: ext.Project, Key: ext.Key, Parent: parent,
	})
	if err != nil {
		return status, err
	}
	events := []store.Event{rec}
	if issue.Rev != "" {
		sync, err := store.NewEvent(store.KindTrackerSync, store.TrackerSyncData{Rev: issue.Rev, Values: ownedValues(w.wf, issue, ext.Owned)})
		if err != nil {
			return status, err
		}
		events = append(events, sync)
	}
	_, err = w.st.Update(ctx, s.task.ID, func(c store.Task, _ []store.Finding) ([]store.Event, error) {
		if c.External == nil || !strings.EqualFold(c.External.Key, key) {
			return nil, errors.New("the task's link changed during the push")
		}
		return events, nil
	})
	if err != nil {
		return status, fmt.Errorf("%s is attached to %s, but recording it failed: %w", key, parent, err)
	}
	return status, nil
}

// pushReport prints the outcome and returns the exit code.
func (a *app) pushReport(cmd string, out pushOut, asJSON, failed bool) int {
	if asJSON {
		if code := a.printJSON(out); code != 0 {
			return code
		}
	} else {
		if out.DryRun {
			fmt.Fprintln(a.stdout, "dry run: nothing is created or written")
		}
		line := func(it pushItem) {
			s := fmt.Sprintf("%s  %s", it.Task, it.Status)
			if it.Key != "" {
				s += "  " + mdLine(it.Key)
			}
			if it.Type != "" {
				s += "  " + mdLine(it.Type)
			}
			if it.Parent != "" {
				s += "  under " + mdLine(it.Parent)
			}
			fmt.Fprintf(a.stdout, "%s  %s\n", s, mdLine(it.Title))
			if it.Error != "" {
				fmt.Fprintf(a.stdout, "  error: %s\n", mdLine(it.Error))
			}
			if it.Payload != nil {
				b, _ := json.Marshal(it.Payload)
				fmt.Fprintf(a.stdout, "  payload: %s\n", b)
			}
		}
		for _, group := range [][]pushItem{out.Pushed, out.Skipped, out.NotPushed} {
			for _, it := range group {
				line(it)
			}
		}
		if len(out.Pushed)+len(out.Skipped)+len(out.NotPushed) > 0 && len(out.Pushed)+len(out.NotPushed) == 0 {
			fmt.Fprintln(a.stdout, "nothing to push")
		}
	}
	if failed {
		return 2
	}
	return 0
}
