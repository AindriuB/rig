package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The keys a config file may hold.
const (
	KeyForgeKind      = "forge.kind"
	KeyForgeHost      = "forge.host"
	KeyTrackerKind    = "tracker.kind"
	KeyTrackerURL     = "tracker.url"
	KeyTrackerProject = "tracker.project"
	KeyWorkflow       = "workflow"
	KeyPushPrefixes   = "push.prefixes"

	KeyTrackerCommand       = "tracker.command"
	KeyTrackerJiraAPI       = "tracker.jira.api"
	KeyTrackerJiraEpicField = "tracker.jira.epicField"
	// KeyTrackerJiraAcceptanceField is the id of the Jira field that holds an
	// issue's acceptance criteria. Unset, the issue has none.
	KeyTrackerJiraAcceptanceField = "tracker.jira.acceptanceField"

	// KeyWorktreesRoot is where `rig task start` puts worktrees: relative to
	// the repository's top level, or absolute.
	KeyWorktreesRoot = "worktrees.root"
)

// DefaultWorktreesRoot is the worktrees.root used when none is configured.
const DefaultWorktreesRoot = ".worktrees"

// Tracker kinds.
const (
	TrackerLocal  = "local"
	TrackerJira   = "jira"
	TrackerGitHub = "github"
	TrackerGitLab = "gitlab"
	TrackerExec   = "exec"
)

// DefaultJiraAPI is the Jira REST dialect used when none is configured.
const DefaultJiraAPI = "auto"

// DefaultWorkflow is the workflow preset used when none is configured.
const DefaultWorkflow = "basic"

// keys is the allow-list, sorted.
var keys = []string{
	KeyForgeHost, KeyForgeKind, KeyPushPrefixes,
	KeyTrackerCommand, KeyTrackerJiraAcceptanceField, KeyTrackerJiraAPI, KeyTrackerJiraEpicField,
	KeyTrackerKind, KeyTrackerProject, KeyTrackerURL, KeyWorkflow, KeyWorktreesRoot,
}

// WorktreesRoot is worktrees.root, DefaultWorktreesRoot when unset. A value
// from a repository-supplied file (.rig/config.json) must be a relative path
// without a '..' element, so it stays under the checkout; anything else is an
// error naming the file. Only the global file may point elsewhere.
func (l *Layers) WorktreesRoot() (string, error) {
	v, ok := l.Get(KeyWorktreesRoot)
	s, _ := v.Value.(string)
	if !ok || s == "" {
		return DefaultWorktreesRoot, nil
	}
	if v.Scope == ScopeLocal || v.Scope == ScopeRigJSON {
		if !insideCheckout(s) {
			return "", fmt.Errorf("%s: %s %q must be a relative path without '..': a project file may not point outside the checkout; set it in %s", l.localPath, KeyWorktreesRoot, s, l.globalPath)
		}
	}
	return s, nil
}

// insideCheckout says whether p is relative and never leaves its base.
func insideCheckout(p string) bool {
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return false
	}
	for _, e := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if e == ".." {
			return false
		}
	}
	return true
}

// Keys returns the allow-list, sorted.
func Keys() []string { return slices.Clone(keys) }

// globalOnly are the keys that widen what rig does without asking: read only
// from the global file and never written by Set or Unset.
var globalOnly = []string{KeyPushPrefixes, KeyTrackerCommand, KeyWorkflow}

var (
	credentialRE = regexp.MustCompile(`(?i)token|secret|password|apikey|key|auth|credential`)
	hostRE       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	fieldRE      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	presetRE     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	jiraRE       = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)
	segmentRE    = `[A-Za-z0-9_.][A-Za-z0-9_.-]*`
	githubRE     = regexp.MustCompile(`^` + segmentRE + `/` + segmentRE + `$`)
	gitlabRE     = regexp.MustCompile(`^` + segmentRE + `(/` + segmentRE + `)+$`)
)

// checkKey refuses a key no file may hold: one that looks like a credential,
// or one not on the allow-list.
func checkKey(key string) error {
	parts := strings.Split(key, ".")
	if credentialRE.MatchString(parts[len(parts)-1]) {
		return fmt.Errorf("%q looks like a credential; config never holds one: use `rig login`", key)
	}
	if !slices.Contains(keys, key) {
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

// checkSettable refuses what Set and Unset may not touch in any scope:
// credentials, the global-only keys in any spelling, and unknown keys.
func checkSettable(key string) error {
	parts := strings.Split(key, ".")
	if credentialRE.MatchString(parts[len(parts)-1]) {
		return checkKey(key)
	}
	norm := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return -1
	}, key)
	if norm == "pushprefixes" || norm == "workflow" || norm == "trackercommand" {
		return fmt.Errorf("%q is read only from the global file ~/.rig/config.json; edit that file by hand", key)
	}
	return checkKey(key)
}

// parseCommand checks tracker.command's value: a non-empty list of strings,
// the first one the program.
func parseCommand(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty list: the program, then its arguments", KeyTrackerCommand)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok || s == "" && len(out) == 0 {
			return nil, fmt.Errorf("%s must be a list of strings starting with the program", KeyTrackerCommand)
		}
		out = append(out, s)
	}
	if p := out[0]; !filepath.IsAbs(p) && strings.ContainsAny(p, `/`+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s program %q is a relative path; use an absolute path or a bare name found on PATH", KeyTrackerCommand, p)
	}
	return out, nil
}

// parseValue turns a decoded JSON value into the key's type and checks it.
func parseValue(key string, v any) (any, error) {
	if key == KeyTrackerCommand {
		return parseCommand(v)
	}
	if key == KeyPushPrefixes {
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("%s must be a non-empty list of branch prefixes", key)
		}
		out := make([]string, 0, len(list))
		for _, e := range list {
			s, ok := e.(string)
			if !ok || !prefixRE.MatchString(s) {
				return nil, fmt.Errorf("%s entry %v is not a plain branch prefix", key, e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be a string", key)
	}
	if err := checkValue(key, s); err != nil {
		return nil, err
	}
	return s, nil
}

// checkValue checks one string value on its own. tracker.project must take
// one of the shapes a tracker accepts; whether it fits the tracker in effect
// is checked by checkProject.
func checkValue(key, v string) error {
	bad := func(want string) error { return fmt.Errorf("%s must be %s, not %q", key, want, v) }
	switch key {
	case KeyForgeKind:
		if v != "github" && v != "gitlab" {
			return bad(`"github" or "gitlab"`)
		}
	case KeyForgeHost:
		if !hostRE.MatchString(v) {
			return bad("a host name")
		}
	case KeyTrackerKind:
		switch v {
		case TrackerLocal, TrackerJira, TrackerGitHub, TrackerGitLab, TrackerExec:
		default:
			return bad(`"local", "jira", "github", "gitlab" or "exec"`)
		}
	case KeyTrackerJiraAPI:
		if v != "auto" && v != "v2" && v != "v3" {
			return bad(`"auto", "v2" or "v3"`)
		}
	case KeyTrackerJiraEpicField, KeyTrackerJiraAcceptanceField:
		if !fieldRE.MatchString(v) {
			return bad("a Jira field id such as customfield_10014")
		}
	case KeyTrackerURL:
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return bad("an https:// URL without a user")
		}
	case KeyTrackerProject:
		if !jiraRE.MatchString(v) && !gitlabRE.MatchString(v) {
			return bad("a Jira project key, owner/name or group/.../name")
		}
	case KeyWorkflow:
		if !presetRE.MatchString(v) && !filepath.IsAbs(v) {
			return bad("a preset name or an absolute path")
		}
	case KeyWorktreesRoot:
		if v == "" || strings.ContainsRune(v, 0) {
			return bad("a non-empty directory path")
		}
	default:
		return checkKey(key)
	}
	return nil
}

// checkProject checks tracker.project against the tracker kind in effect.
// Under "local" any shape checkValue accepts is allowed, so the keys can be
// set in either order.
func checkProject(kind, project string) error {
	var re *regexp.Regexp
	var want string
	switch kind {
	case TrackerJira:
		re, want = jiraRE, "a Jira project key such as RIG"
	case TrackerGitHub:
		re, want = githubRE, "owner/name"
	case TrackerGitLab:
		re, want = gitlabRE, "group/.../name"
	default:
		return nil
	}
	if !re.MatchString(project) {
		return fmt.Errorf("%s for tracker %q must be %s, not %q", KeyTrackerProject, kind, want, project)
	}
	return nil
}
