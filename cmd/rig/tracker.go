package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	authjira "github.com/AindriuB/rig/internal/auth/jira"
	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/secret"
	"github.com/AindriuB/rig/internal/store"
	"github.com/AindriuB/rig/internal/tracker"
	exectracker "github.com/AindriuB/rig/internal/tracker/exec"
	"github.com/AindriuB/rig/internal/tracker/github"
	"github.com/AindriuB/rig/internal/tracker/gitlab"
	jiratracker "github.com/AindriuB/rig/internal/tracker/jira"
	"github.com/AindriuB/rig/internal/workflow"
)

const trackerUsage = "usage: rig tracker pull|refresh|report|show|status ..."

// Why a tracker cannot be used. Each is exit 2; `rig tracker status` reports
// the last two instead.
var (
	errNoTracker = errors.New("no tracker is configured: tracker.kind is local or unset")
	errNoLogin   = errors.New("no tracker login")
	errUntrusted = errors.New("tracker configuration is not trusted")
)

// trackerLink names the tracker a task's external link points at.
type trackerLink struct {
	Kind, Host, Project string
}

// trackerSpec is what the config says about the tracker, with the login
// resolved: everything needed to build the adapter, and nothing built.
type trackerSpec struct {
	Kind    string
	Trusted bool
	Link    trackerLink
	Dir     string
	Jira    jiratracker.Config
	Exec    []string
	GitHub  github.Options
	GitLab  gitlab.Options
}

// tracker builds the adapter for a spec. It is a field of app so a test can
// put a fake in its place.
type trackerBuilder func(ctx context.Context, spec trackerSpec) (tracker.Tracker, error)

// buildTracker is the production app.tracker.
func (a *app) buildTracker(ctx context.Context, spec trackerSpec) (tracker.Tracker, error) {
	switch spec.Kind {
	case config.TrackerJira:
		spec.Jira.HTTPClient = a.httpClient
		return jiratracker.New(spec.Jira)
	case config.TrackerExec:
		return exectracker.New(spec.Exec, exectracker.Options{Dir: spec.Dir})
	case config.TrackerGitHub:
		return github.New(spec.GitHub)
	case config.TrackerGitLab:
		return gitlab.New(spec.GitLab)
	}
	return nil, fmt.Errorf("no adapter for tracker %q in this build", spec.Kind)
}

// selectTracker reads the config for the repository at dir and returns the
// tracker adapter and what describes it. It makes no tracker call.
func (a *app) selectTracker(ctx context.Context, dir string) (tracker.Tracker, trackerSpec, error) {
	spec, err := a.trackerSpec(ctx, dir)
	if err != nil {
		return nil, spec, err
	}
	build := a.tracker
	if build == nil {
		build = a.buildTracker
	}
	tr, err := build(ctx, spec)
	return tr, spec, err
}

// urlHost is the host of a tracker.url value, with or without a scheme.
func urlHost(s string) string {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// trackerSpec resolves tracker.kind and what the adapter for it needs.
// Returned with an error, the spec still holds what could be read (the kind,
// whether it is trusted).
func (a *app) trackerSpec(ctx context.Context, dir string) (trackerSpec, error) {
	b := *a
	b.cwd = dir
	layers, root, _, err := b.openConfig(ctx)
	if err != nil {
		return trackerSpec{}, err
	}
	if root == "" {
		root = dir
	}
	spec := trackerSpec{Kind: config.TrackerLocal, Dir: root}
	if v, ok := layers.Get(config.KeyTrackerKind); ok {
		spec.Kind, _ = v.Value.(string)
	}
	if spec.Kind == "" || spec.Kind == config.TrackerLocal {
		spec.Kind = config.TrackerLocal
		spec.Trusted = true
		return spec, errNoTracker
	}
	tc, err := layers.Tracker()
	if err != nil {
		return spec, err
	}
	// The trust check: a tracker.url the repository itself supplies is refused.
	uv, err := layers.TrackerURL()
	if err != nil {
		return spec, fmt.Errorf("%w: %v", errUntrusted, err)
	}
	spec.Trusted = true
	site, _ := uv.Value.(string)
	spec.Link = trackerLink{Kind: spec.Kind}

	switch spec.Kind {
	case config.TrackerJira:
		host, err := authjira.Host(site)
		if err != nil {
			return spec, err
		}
		spec.Link.Host = host
		user, token, err := a.jiraLogin(ctx, host, layers.RepoKey(), layers.JiraAPI())
		if err != nil {
			return spec, err
		}
		spec.Jira = jiratracker.Config{
			Site: site, User: user, Token: token, API: layers.JiraAPI(), Project: tc.Project,
		}
		spec.Jira.EpicField = layerStr(layers, config.KeyTrackerJiraEpicField)
		spec.Jira.AcceptanceField = layerStr(layers, config.KeyTrackerJiraAcceptanceField)
	case config.TrackerExec:
		v, err := layers.TrackerCommand()
		if err != nil {
			return spec, err
		}
		argv, _ := v.Value.([]string)
		if len(argv) == 0 || v.Scope == config.ScopeLocal || v.Scope == config.ScopeRigJSON {
			spec.Trusted = false
			return spec, fmt.Errorf("%w: tracker.exec needs tracker.command set in the global config", errUntrusted)
		}
		spec.Exec = argv
	case config.TrackerGitHub, config.TrackerGitLab:
		host, project := "", tc.Project
		if site != "" {
			host = urlHost(site)
		}
		def := "github.com"
		if spec.Kind == config.TrackerGitLab {
			def = "gitlab.com"
		}
		// forge.host stands for this tracker's server only when forge.kind
		// is unset or names the same kind. It may carry a port, which the
		// origin's repo key does not, so the two are compared by host name.
		forgeHost, forgeName := "", ""
		if v, ok := layers.Get(config.KeyForgeHost); ok {
			forgeHost, _ = v.Value.(string)
			forgeHost = strings.ToLower(forgeHost)
		}
		cfgKind := ""
		if v, ok := layers.Get(config.KeyForgeKind); ok {
			cfgKind, _ = v.Value.(string)
		}
		switch {
		case cfgKind != "" && cfgKind != spec.Kind:
			forgeHost = ""
		case cfgKind == "" && forgeHost != "":
			// As rig login does: with no forge.kind, a forge.host that
			// names the other forge is not this tracker's server.
			if k, ok := forge.KindForHost(forgeHost); ok && string(k) != spec.Kind {
				forgeHost = ""
			}
		}
		if forgeHost != "" {
			forgeName = forgeHost
			if h, _, err := net.SplitHostPort(forgeHost); err == nil {
				forgeName = h
			}
		}
		if key := layers.RepoKey(); key != "" {
			h, path, _ := strings.Cut(key, "/")
			// Only the forge's own host is taken from the remote. Any other
			// host (an SSH alias such as github-work, or a self-hosted
			// server nobody configured) is refused rather than guessed:
			// a guess could read and link an issue on the wrong server.
			if lh := strings.ToLower(h); host == "" {
				switch {
				case lh == def:
					host = lh
				case forgeName != "" && lh == forgeName:
					host = forgeHost
				default:
					return spec, fmt.Errorf("origin host %q is not %s or the configured forge.host: set tracker.url to the tracker's address", h, def)
				}
			}
			if project == "" {
				project = path
			}
		}
		if project == "" {
			return spec, errors.New("no repository for the tracker: set tracker.project or add an origin remote")
		}
		if host == "" {
			host = def
			if forgeHost != "" {
				host = forgeHost
			}
		}
		spec.Link.Host, spec.Link.Project = host, project
		if host == def {
			host = ""
		}
		if spec.Kind == config.TrackerGitHub {
			spec.GitHub = github.Options{Repo: project, Host: host, Dir: root}
		} else {
			spec.GitLab = gitlab.Options{Project: project, Host: host, Dir: root}
		}
	default:
		return spec, fmt.Errorf("no adapter for tracker %q", spec.Kind)
	}
	return spec, nil
}

func layerStr(l *config.Layers, key string) string {
	v, _ := l.Get(key)
	s, _ := v.Value.(string)
	return s
}

// jiraLogin finds the Jira login: RIG_JIRA_TOKEN if set, else the first
// secret under the repository's account, then the global one. A secret with
// no user is a personal access token, and gives the user "". An environment
// token with no stored secret is a personal access token only on API v2.
func (a *app) jiraLogin(ctx context.Context, host, repoKey, api string) (user, token string, err error) {
	var found *secret.Secret
	var lookupErr error
search:
	for _, account := range authjira.Accounts(host, repoKey) {
		for _, kind := range storeKinds() {
			st, err := a.secrets(kind)
			if err == nil {
				var sec secret.Secret
				if sec, err = st.Get(ctx, account); err == nil {
					found = &sec
					break search
				}
			}
			if !errors.Is(err, secret.ErrNotFound) {
				lookupErr = fmt.Errorf("%s store: %w", kind, err)
			}
		}
	}
	if found != nil {
		user, token = found.User, found.Token
	}
	if env := a.getenv(jiraTokenEnv); env != "" {
		if found == nil && api != "v2" {
			return "", "", fmt.Errorf("%w: %s is set but no user is stored; run rig login jira", errNoLogin, jiraTokenEnv)
		}
		token = env
	}
	if token == "" {
		msg := "no Jira login: run rig login jira"
		if lookupErr != nil {
			msg += " (" + lookupErr.Error() + ")"
		}
		return "", "", fmt.Errorf("%w: %s", errNoLogin, msg)
	}
	return user, token, nil
}

// trackerCmd implements `rig tracker`: pull, refresh, report, show and status. Exit
// codes: 0 done; 1 refused (a state outside the intake states); 2 usage, no
// tracker, a failed read, or any item that failed.
func (a *app) trackerCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.fail("tracker", "%s", trackerUsage)
	}
	rest := args[1:]
	switch args[0] {
	case "pull":
		return a.trackerPull(ctx, rest)
	case "refresh":
		return a.trackerRefresh(ctx, rest)
	case "report":
		return a.trackerReport(ctx, rest)
	case "show":
		return a.trackerShow(ctx, rest)
	case "status":
		return a.trackerStatus(ctx, rest)
	}
	return a.fail("tracker", "unknown subcommand %q\n%s", args[0], trackerUsage)
}

// takeChildren removes --children and --children=<depth> from args and
// returns the depth: 0 when absent, 1 when bare.
func takeChildren(args []string) (rest []string, depth int, err error) {
	for _, arg := range args {
		name, val, hasVal := strings.Cut(arg, "=")
		if name != "--children" {
			rest = append(rest, arg)
			continue
		}
		depth = 1
		if hasVal {
			n, perr := strconv.Atoi(val)
			if perr != nil || n < 1 {
				return nil, 0, fmt.Errorf("--children wants a depth of 1 or more, not %q", val)
			}
			depth = n
		}
	}
	return rest, depth, nil
}

// pullItem is one entry of `rig tracker pull --json`.
type pullItem struct {
	Task     workflow.TaskView `json:"task"`
	Key      string            `json:"key"`
	Existing bool              `json:"existing"`
	Parent   string            `json:"parent"`
	Warnings []string          `json:"warnings"`
}

// pullOut is `rig tracker pull --json`.
type pullOut struct {
	Items   []pullItem `json:"items"`
	Skipped int        `json:"skipped"`
}

func (a *app) trackerPull(ctx context.Context, args []string) int {
	const cmd = "tracker pull"
	const use = "usage: rig tracker pull <key> | --query <q> | --label <l> [--children[=<depth>]] [--limit <n>] [--state <s>] [--actor <name>] [--json] [--repo <dir>]"
	args, depth, err := takeChildren(args)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	fs := newFlags()
	actorOK := a.actorFlags(fs)
	query, label, limit, state, repo := fs.str("--query"), fs.str("--label"), fs.str("--limit"), fs.str("--state"), fs.str("--repo")
	asJSON := fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 1 {
		return a.fail(cmd, "%s", use)
	}
	if err := actorOK(); err != nil {
		return a.fail(cmd, "%v", err)
	}
	sources := len(pos)
	for _, s := range []string{*query, *label} {
		if s != "" {
			sources++
		}
	}
	if sources != 1 {
		return a.fail(cmd, "give exactly one of <key>, --query and --label\n%s", use)
	}
	lim := 50
	if *limit != "" {
		if lim, err = strconv.Atoi(*limit); err != nil || lim < 1 {
			return a.fail(cmd, "--limit must be a number of 1 or more")
		}
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	// Before the tracker is read.
	startState, err := intakeState(w.wf, *state)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}

	failed, skipped := false, 0
	fail := func(format string, args ...any) {
		fmt.Fprintf(a.stderr, "rig %s: %s\n", cmd, fmt.Sprintf(format, args...))
		failed = true
		skipped++
	}
	type entry struct {
		key   string
		depth int
	}
	var queue []entry
	switch {
	case len(pos) == 1:
		queue = append(queue, entry{pos[0], 0})
	default:
		q := tracker.Query{Text: *query, Limit: lim}
		if *label != "" {
			q.Labels = []string{*label}
		}
		refs, err := tr.Search(ctx, q)
		switch {
		case errors.Is(err, tracker.ErrUnsupported):
			fmt.Fprintf(a.stderr, "rig %s: warning: this tracker cannot search\n", cmd)
		case err != nil:
			return a.fail(cmd, "search: %v", err)
		}
		if len(refs) > lim {
			refs = refs[:lim]
		}
		for _, r := range refs {
			if err := checkIssueKey("search result", r.Key); err != nil {
				fail("%v", err)
				continue
			}
			queue = append(queue, entry{r.Key, 0})
		}
	}

	out := pullOut{Items: []pullItem{}}
	seen := map[string]bool{}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if seen[strings.ToLower(e.key)] {
			continue
		}
		seen[strings.ToLower(e.key)] = true
		item, err := intake(ctx, a, w.st, tr, w.wf, e.key, intakeOpts{State: startState, Link: spec.Link})
		if err != nil {
			fail("%s: %v", e.key, err)
			continue
		}
		v, err := w.view(ctx, item.Task)
		if err != nil {
			fail("%s: %v", e.key, err)
			continue
		}
		pi := pullItem{Task: v, Key: item.Key, Existing: item.Existing, Parent: item.Parent, Warnings: item.Warnings}
		if pi.Warnings == nil {
			pi.Warnings = []string{}
		}
		if e.depth < depth {
			refs, err := tr.Children(ctx, e.key)
			switch {
			case errors.Is(err, tracker.ErrUnsupported):
				pi.Warnings = append(pi.Warnings, "this tracker cannot list children")
			case err != nil:
				fail("%s: children: %v", e.key, err)
			default:
				for _, r := range refs {
					if err := checkIssueKey("child", r.Key); err != nil {
						fail("%s: %v", e.key, err)
						continue
					}
					queue = append(queue, entry{r.Key, e.depth + 1})
				}
			}
		}
		out.Items = append(out.Items, pi)
	}
	out.Skipped = skipped
	if *asJSON {
		if code := a.printJSON(out); code != 0 {
			return code
		}
	} else {
		for _, it := range out.Items {
			mark := ""
			if it.Existing {
				mark = "  (existing)"
			}
			fmt.Fprintf(a.stdout, "%s  %s  %s  %s%s\n", mdLine(it.Key), it.Task.Short, it.Task.State, mdLine(it.Task.Title), mark)
			for _, w := range it.Warnings {
				fmt.Fprintf(a.stdout, "  warning: %s\n", w)
			}
		}
		if skipped > 0 {
			fmt.Fprintf(a.stdout, "skipped %d\n", skipped)
		}
	}
	if failed {
		return 2
	}
	return 0
}

// replacedField is a local value a refresh overwrote.
type replacedField struct {
	Field string `json:"field"`
	Lost  any    `json:"lost"`
}

// refreshTask is one entry of `rig tracker refresh --json`.
type refreshTask struct {
	Task     string          `json:"task"`
	Key      string          `json:"key"`
	Status   string          `json:"status"`
	Changed  []string        `json:"changed"`
	Replaced []replacedField `json:"replaced"`
	Error    string          `json:"error,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

// refreshOut is `rig tracker refresh --json`.
type refreshOut struct {
	Tasks []refreshTask `json:"tasks"`
}

// refreshPlan is what a refresh would write for a task as it is now.
type refreshPlan struct {
	changed  []string
	replaced []replacedField
	events   []store.Event
}

// planRefresh compares the issue's owned values with the task's. The tracker
// wins: a changed value is written, and one the task had edited since the
// last snapshot is also reported as replaced. The snapshot is written when
// any field changed or the snapshot no longer matches the issue.
func (a *app) planRefresh(t store.Task, issue tracker.Issue, values map[string]any) (refreshPlan, error) {
	ext := t.External
	var p refreshPlan
	changes := map[string]any{}
	for _, name := range sortedNames(values) {
		cur, v := currentValue(t, name), values[name]
		if sameValue(cur, v) {
			continue
		}
		changes[name] = v
		p.changed = append(p.changed, name)
		if snap, ok := ext.Snapshot[name]; ok && !sameValue(cur, snap) {
			p.replaced = append(p.replaced, replacedField{Field: name, Lost: cur})
		}
	}
	snapSame := ext.Rev == issue.Rev && len(ext.Snapshot) == len(values)
	for name, v := range values {
		if snap, ok := ext.Snapshot[name]; !ok || !sameValue(snap, v) {
			snapSame = false
		}
	}
	if len(changes) == 0 && snapSame {
		return p, nil
	}
	evs, err := fieldEvents(changes)
	if err != nil {
		return p, err
	}
	sync, err := store.NewEvent(store.KindTrackerSync, store.TrackerSyncData{At: a.now().UTC(), Rev: issue.Rev, Values: values})
	if err != nil {
		return p, err
	}
	p.events = append(evs, sync)
	return p, nil
}

// resolveParent links t to the task for its issue's parent, once that parent
// is in the store. A parent that would make a cycle is a warning, and no
// parent is set. With dry it writes nothing. It adds "parent" to res.Changed
// when the parent is, or would be, set.
func (a *app) resolveParent(ctx context.Context, w *work, t store.Task, dry bool, res *refreshTask) error {
	ext := t.External
	find := func(tasks []store.Task) (string, string) {
		pt, ok := store.Linked(&store.External{Kind: ext.Kind, Host: ext.Host, Project: ext.Project, Key: ext.ParentKey}, tasks)
		if !ok {
			return "", ""
		}
		if err := store.CheckParent(t.ID, pt.ID, tasks); err != nil {
			return "", fmt.Sprintf("parent %s of %s is not linked: %v", ext.ParentKey, ext.Key, err)
		}
		return pt.ID, ""
	}
	tasks, err := w.st.Tasks(ctx, store.TaskFilter{})
	if err != nil {
		return err
	}
	parent, warn := find(tasks)
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	if parent == "" {
		return nil
	}
	if !dry {
		var applied bool
		_, err = w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
			if cur.Parent != "" || cur.External == nil || cur.External.ParentKey != ext.ParentKey {
				return nil, nil
			}
			all, err := w.st.Tasks(ctx, store.TaskFilter{})
			if err != nil {
				return nil, err
			}
			id, _ := find(all)
			if id == "" {
				return nil, nil
			}
			ev, err := store.NewEvent(store.KindParent, store.ParentData{Task: id})
			applied = err == nil
			return []store.Event{ev}, err
		})
		if err != nil {
			return err
		}
		if !applied {
			return nil
		}
	}
	res.Changed = append(res.Changed, "parent")
	return nil
}

// linkMismatch says how a task's link differs from the configured tracker, or
// "" when it is the same one. The kind and host always count; for GitHub and
// GitLab so does the project.
func linkMismatch(ext *store.External, spec trackerSpec) string {
	l := spec.Link
	same := ext.Kind == l.Kind && strings.EqualFold(ext.Host, l.Host)
	if same && (l.Kind == config.TrackerGitHub || l.Kind == config.TrackerGitLab) {
		same = strings.EqualFold(ext.Project, l.Project)
	}
	if same {
		return ""
	}
	return fmt.Sprintf("linked to %s, but the tracker is %s", linkName(ext.Kind, ext.Host, ext.Project), linkName(l.Kind, l.Host, l.Project))
}

func linkName(kind, host, project string) string {
	return strings.Join(slices.DeleteFunc([]string{kind, host, project}, func(s string) bool { return s == "" }), " ")
}

// refreshOne re-reads the issue t is linked to and brings its owned fields up
// to date. With dry it writes nothing.
func (a *app) refreshOne(ctx context.Context, w *work, tr tracker.Tracker, spec trackerSpec, t store.Task, dry bool) refreshTask {
	ext := t.External
	res := refreshTask{Task: t.Short, Key: ext.Key, Status: "error", Changed: []string{}, Replaced: []replacedField{}}
	if msg := linkMismatch(ext, spec); msg != "" {
		res.Error = msg
		return res
	}
	issue, err := tr.Issue(ctx, ext.Key)
	if errors.Is(err, tracker.ErrNotFound) {
		res.Status = "gone"
		return res
	}
	if err != nil {
		res.Error = err.Error()
		var moved *jiratracker.MovedError
		if errors.As(err, &moved) {
			res.Error += fmt.Sprintf("; re-link with rig task link %s %s", t.Short, moved.Got)
		}
		return res
	}
	if err := checkIssueKey("issue", issue.Key); err != nil {
		res.Error = err.Error()
		return res
	}
	if !strings.EqualFold(issue.Key, ext.Key) {
		res.Error = fmt.Sprintf("the tracker answered %s for %s; re-link with rig task link %s %s", issue.Key, ext.Key, t.Short, issue.Key)
		return res
	}
	values := ownedValues(w.wf, issue, ext.Owned)
	plan, err := a.planRefresh(t, issue, values)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if !dry && len(plan.events) > 0 {
		_, err = w.st.Update(ctx, t.ID, func(cur store.Task, _ []store.Finding) ([]store.Event, error) {
			if cur.External == nil || !strings.EqualFold(cur.External.Key, ext.Key) || cur.External.Kind != ext.Kind {
				return nil, errors.New("the task's link changed during the refresh")
			}
			var err error
			if plan, err = a.planRefresh(cur, issue, values); err != nil {
				return nil, err
			}
			return plan.events, nil
		})
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}
	if ext.ParentKey != "" && t.Parent == "" {
		if err := a.resolveParent(ctx, w, t, dry, &res); err != nil {
			res.Error = err.Error()
			return res
		}
	}
	res.Status = "unchanged"
	if len(plan.changed) > 0 || len(res.Changed) > 0 {
		res.Status = "updated"
		res.Changed = append(plan.changed, res.Changed...)
	}
	if plan.replaced != nil {
		res.Replaced = plan.replaced
	}
	return res
}

func (a *app) trackerRefresh(ctx context.Context, args []string) int {
	const cmd = "tracker refresh"
	const use = "usage: rig tracker refresh [<task> | --all] [--dry-run] [--actor <name>] [--json] [--repo <dir>]"
	fs := newFlags()
	actorOK := a.actorFlags(fs)
	repo := fs.str("--repo")
	all, dry, asJSON := fs.boolean("--all"), fs.boolean("--dry-run"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) > 1 || (len(pos) == 1) == *all {
		return a.fail(cmd, "give exactly one of <task> and --all\n%s", use)
	}
	if err := actorOK(); err != nil {
		return a.fail(cmd, "%v", err)
	}
	w, err := a.openWork(ctx, *repo, true)
	if err != nil {
		return a.storeFail(cmd, err)
	}
	tr, spec, err := a.selectTracker(ctx, w.dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	var tasks []store.Task
	if *all {
		every, err := w.st.Tasks(ctx, store.TaskFilter{})
		if err != nil {
			return a.storeFail(cmd, err)
		}
		for _, t := range every {
			if t.External != nil {
				tasks = append(tasks, t)
			}
		}
	} else {
		t, err := w.st.Task(ctx, pos[0])
		if err != nil {
			return a.storeFail(cmd, err)
		}
		if t.External == nil {
			return a.fail(cmd, "task %s is not linked to an issue", t.Short)
		}
		tasks = append(tasks, t)
	}
	out := refreshOut{Tasks: []refreshTask{}}
	failed := false
	for _, t := range tasks {
		r := a.refreshOne(ctx, w, tr, spec, t, *dry)
		if r.Status == "error" {
			failed = true
			fmt.Fprintf(a.stderr, "rig %s: %s: %s\n", cmd, r.Key, r.Error)
		}
		out.Tasks = append(out.Tasks, r)
	}
	if *asJSON {
		if code := a.printJSON(out); code != 0 {
			return code
		}
	} else {
		for _, r := range out.Tasks {
			line := fmt.Sprintf("%s  %s  %s", r.Task, r.Key, r.Status)
			if len(r.Changed) > 0 {
				line += "  " + strings.Join(r.Changed, ",")
			}
			for _, x := range r.Replaced {
				line += "  replaced " + x.Field
			}
			fmt.Fprintln(a.stdout, line)
			for _, w := range r.Warnings {
				fmt.Fprintf(a.stdout, "  warning: %s\n", w)
			}
		}
	}
	if failed {
		return 2
	}
	return 0
}

func (a *app) trackerShow(ctx context.Context, args []string) int {
	const cmd = "tracker show"
	fs := newFlags()
	repo, asJSON := fs.str("--repo"), fs.boolean("--json")
	pos, err := fs.parse(args)
	if err != nil || len(pos) != 1 {
		return a.fail(cmd, "usage: rig tracker show <key> [--json] [--repo <dir>]")
	}
	if err := tracker.ValidateKey(pos[0]); err != nil {
		return a.fail(cmd, "%v", err)
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	tr, _, err := a.selectTracker(ctx, dir)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	is, err := tr.Issue(ctx, pos[0])
	if err != nil {
		return a.fail(cmd, "%s: %v", pos[0], err)
	}
	if err := checkIssueKey("issue", is.Key); err != nil {
		return a.fail(cmd, "%v", err)
	}
	if is.Acceptance == nil {
		is.Acceptance = []string{}
	}
	if is.Labels == nil {
		is.Labels = []string{}
	}
	if *asJSON {
		return a.printJSON(is)
	}
	fmt.Fprintf(a.stdout, "%s  %s\n", mdLine(is.Key), mdLine(is.Title))
	for _, kv := range [][2]string{
		{"url", is.URL}, {"status", is.Status}, {"type", is.Type}, {"priority", is.Priority},
		{"assignee", is.Assignee}, {"labels", strings.Join(is.Labels, ", ")}, {"rev", is.Rev},
	} {
		if kv[1] != "" {
			fmt.Fprintf(a.stdout, "%s: %s\n", kv[0], mdLine(kv[1]))
		}
	}
	if is.Parent != nil {
		fmt.Fprintf(a.stdout, "parent: %s\n", mdLine(is.Parent.Key))
	}
	if is.Description != "" {
		fmt.Fprintf(a.stdout, "\n%s\n", is.Description)
	}
	for _, c := range is.Acceptance {
		fmt.Fprintf(a.stdout, "- %s\n", mdLine(c))
	}
	return 0
}

// trackerLogin is the login part of `rig tracker status --json`.
type trackerLogin struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// trackerStatusOut is `rig tracker status --json`.
type trackerStatusOut struct {
	Kind         string        `json:"kind"`
	Trusted      bool          `json:"trusted"`
	Capabilities *tracker.Caps `json:"capabilities"`
	Login        trackerLogin  `json:"login"`
	// Project is the configured project's check, for a tracker that can.
	Project *tracker.ProjectInfo `json:"project,omitempty"`
}

func (a *app) trackerStatus(ctx context.Context, args []string) int {
	const cmd = "tracker status"
	fs := newFlags()
	repo, asJSON := fs.str("--repo"), fs.boolean("--json")
	if pos, err := fs.parse(args); err != nil || len(pos) > 0 {
		return a.fail(cmd, "usage: rig tracker status [--json] [--repo <dir>]")
	}
	dir, err := a.repoDir(*repo)
	if err != nil {
		return a.fail(cmd, "%v", err)
	}
	tr, spec, err := a.selectTracker(ctx, dir)
	out := trackerStatusOut{Kind: spec.Kind, Trusted: spec.Trusted}
	var projectErr error // the project could not be checked: a report, not a failure
	switch {
	case errors.Is(err, errNoLogin), errors.Is(err, errUntrusted):
		out.Login.Error = err.Error()
	case err != nil:
		return a.fail(cmd, "%v", err)
	default:
		caps, cerr := tr.Capabilities(ctx)
		if cerr != nil && !errors.Is(cerr, tracker.ErrAuth) {
			return a.fail(cmd, "%v", cerr)
		}
		if cerr != nil {
			out.Login.Error = cerr.Error()
		} else {
			out.Capabilities, out.Login.OK = &caps, true
			if pc, ok := tr.(tracker.ProjectChecker); ok {
				info, perr := pc.CheckProject(ctx)
				switch {
				case perr == nil:
					out.Project = &info
				case !errors.Is(perr, tracker.ErrUnsupported):
					projectErr = perr
				}
			}
		}
	}
	if *asJSON {
		return a.printJSON(out)
	}
	fmt.Fprintf(a.stdout, "kind: %s\ntrusted: %t\n", out.Kind, out.Trusted)
	if c := out.Capabilities; c != nil {
		fmt.Fprintf(a.stdout, "capabilities: parent=%t children=%t types=%t search=%t comment=%t setStatus=%t\n",
			c.Parent, c.Children, c.Types, c.Search, c.Comment, c.SetStatus)
	}
	if out.Login.OK {
		fmt.Fprintln(a.stdout, "login: ok")
	} else {
		fmt.Fprintf(a.stdout, "login: failed: %s\n", out.Login.Error)
	}
	switch p := out.Project; {
	case p != nil && !p.Exists:
		fmt.Fprintf(a.stdout, "project: %s not found\n", p.Key)
	case p != nil && p.Empty:
		fmt.Fprintf(a.stdout, "project: %s ok (no issues yet)\n", p.Key)
	case p != nil:
		fmt.Fprintf(a.stdout, "project: %s ok\n", p.Key)
	case projectErr != nil:
		fmt.Fprintf(a.stdout, "project: not checked: %v\n", projectErr)
	}
	return 0
}
