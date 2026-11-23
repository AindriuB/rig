package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/AindriuB/rig/internal/auth/jira"
	"github.com/AindriuB/rig/internal/config"
	"github.com/AindriuB/rig/internal/forge"
	"github.com/AindriuB/rig/internal/git"
	"github.com/AindriuB/rig/internal/proc"
	"github.com/AindriuB/rig/internal/secret"
	"github.com/AindriuB/rig/internal/tty"
)

// Store kinds a login can use.
const (
	storeKeychain = "keychain"
	storeFile     = "file"
	storeEnv      = "env" // RIG_JIRA_TOKEN, reported by `login status` only
)

// jiraTokenEnv overrides the stored Jira token at use time. rig never writes it.
const jiraTokenEnv = "RIG_JIRA_TOKEN"

// toolTimeout bounds a `gh auth status` or `glab auth status` call.
const toolTimeout = 30 * time.Second

// openSecrets is the production app.secrets: the OS keychain, or the file
// ~/.rig/credentials.json. Neither is a fallback for the other.
func (a *app) openSecrets(kind string) (secret.Store, error) {
	switch kind {
	case storeKeychain:
		return secret.Keychain()
	case storeFile:
		home, err := a.homeDir()
		if err != nil {
			return nil, err
		}
		return secret.File(filepath.Join(home, config.Dir, "credentials.json")), nil
	}
	return nil, fmt.Errorf("unknown store %q", kind)
}

// loginStatus is one service in `rig login status --json`. It never holds a
// token.
type loginStatus struct {
	Service  string `json:"service"`
	LoggedIn bool   `json:"loggedIn"`
	User     string `json:"user,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Store    string `json:"store,omitempty"`
	Error    string `json:"error,omitempty"`
}

// loginOpts are the flags of login and logout.
type loginOpts struct {
	url, user, store *string
	repo, asJSON     *bool
	pat              *bool // a Server/Data Center personal access token, no user
}

func newLoginFlags() (*flagSet, loginOpts) {
	f := newFlags()
	return f, loginOpts{
		url: f.str("--url"), user: f.str("--user"), store: f.str("--store"),
		repo: f.boolean("--repo"), asJSON: f.boolean("--json"), pat: f.boolean("--pat"),
	}
}

// login implements `rig login [github|gitlab|jira|status]`.
func (a *app) login(ctx context.Context, args []string) int {
	f, o := newLoginFlags()
	pos, err := f.parse(args)
	if err != nil {
		return a.fail("login", "%v", err)
	}
	if len(pos) > 1 {
		return a.fail("login", "expected at most one service, got %d arguments", len(pos))
	}
	service := ""
	if len(pos) == 1 {
		service = pos[0]
	}
	switch service {
	case "", "github", "gitlab", "jira", "status":
	default:
		return a.fail("login", "unknown service %q: use github, gitlab, jira or status", service)
	}
	jiraFlags := *o.url != "" || *o.user != "" || *o.store != "" || *o.repo || *o.pat
	if (service == "github" || service == "gitlab" || service == "status") && jiraFlags {
		return a.fail("login", "--url, --user, --repo, --store and --pat apply to jira only")
	}
	if *o.pat && *o.user != "" {
		return a.fail("login", "--pat stores a token with no user; drop --user")
	}
	if *o.asJSON && service != "status" {
		return a.fail("login", "--json applies to `rig login status` only")
	}

	layers, root, remote, err := a.openConfig(ctx)
	if err != nil {
		return a.fail("login", "%v", err)
	}
	switch service {
	case "status":
		return a.loginStatus(ctx, layers, remote, *o.asJSON)
	case "jira":
		return a.loginJira(ctx, layers, root, o)
	case "github", "gitlab":
		return a.loginForge(ctx, layers, remote, service)
	}

	// No service: the forge, then the tracker when it is Jira.
	withJira := false
	if v, _ := layers.Get(config.KeyTrackerKind); v.Value == config.TrackerJira {
		withJira = true
	} else if jiraFlags {
		return a.fail("login", "--url, --user, --repo, --store and --pat apply to jira, which tracker.kind does not select")
	}
	failed := a.loginForge(ctx, layers, remote, "") != 0
	if withJira && a.loginJira(ctx, layers, root, o) != 0 {
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}

// logout implements `rig logout jira|github|gitlab`.
func (a *app) logout(ctx context.Context, args []string) int {
	f, o := newLoginFlags()
	pos, err := f.parse(args)
	if err != nil {
		return a.fail("logout", "%v", err)
	}
	if len(pos) != 1 {
		return a.fail("logout", "usage: rig logout jira|github|gitlab")
	}
	if *o.user != "" || *o.store != "" || *o.asJSON || *o.pat {
		return a.fail("logout", "only --url and --repo are accepted")
	}
	switch pos[0] {
	case "github", "gitlab":
		if *o.url != "" || *o.repo {
			return a.fail("logout", "--url and --repo apply to jira only")
		}
		layers, _, remote, err := a.openConfig(ctx)
		if err != nil {
			return a.fail("logout", "%v", err)
		}
		t := forgeTargetFor(layers, remote, pos[0])
		return a.fail("logout", "that login belongs to %s and every other tool that uses it; run: %s auth logout --hostname %s", t.tool, t.tool, t.host)
	case "jira":
	default:
		return a.fail("logout", "unknown service %q: use jira, github or gitlab", pos[0])
	}

	layers, root, _, err := a.openConfig(ctx)
	if err != nil {
		return a.fail("logout", "%v", err)
	}
	_, _, accounts, err := a.jiraAccounts(layers, root, *o.url, *o.repo)
	if err != nil {
		return a.fail("logout", "%v", err)
	}
	account := accounts[0]
	host := strings.TrimPrefix(account, "jira:")
	var deleted, absent, unknown []string
	failed := false
	for _, kind := range storeKinds() {
		st, err := a.secrets(kind)
		if err != nil {
			// A store that cannot be opened cannot be checked.
			fmt.Fprintf(a.stderr, "rig logout: skipped the %s store: %v\n", kind, err)
			unknown = append(unknown, kind)
			continue
		}
		if _, err := st.Get(ctx, account); errors.Is(err, secret.ErrNotFound) {
			absent = append(absent, kind)
			continue
		} else if err != nil {
			fmt.Fprintf(a.stderr, "rig logout: skipped the %s store: %v\n", kind, err)
			unknown = append(unknown, kind)
			continue
		}
		if err := st.Delete(ctx, account); err != nil {
			fmt.Fprintf(a.stderr, "rig logout: %s: %v\n", kind, err)
			failed = true
			continue
		}
		deleted = append(deleted, kind)
	}
	if failed {
		return 2
	}
	if len(deleted) > 0 {
		fmt.Fprintf(a.stdout, "Logged out of %s\n", host)
		return 0
	}
	if len(unknown) > 0 {
		return a.fail("logout", "could not check the %s store; no entry found in %s", strings.Join(unknown, ", "), strings.Join(absent, ", "))
	}
	if !*o.repo && root != "" && layers.RepoKey() != "" {
		repoAcct := account + ":" + layers.RepoKey()
		for _, kind := range storeKinds() {
			st, err := a.secrets(kind)
			if err != nil {
				continue
			}
			if _, err := st.Get(ctx, repoAcct); err == nil {
				fmt.Fprintf(a.stdout, "No global login for %s, but this repository has its own; run: rig logout jira --repo\n", host)
				return 0
			}
		}
	}
	fmt.Fprintf(a.stdout, "Not logged in to %s\n", host)
	return 0
}

// storeKinds are the stores rig reads and clears: the file store does not
// exist on Windows.
func storeKinds() []string {
	if runtime.GOOS == "windows" {
		return []string{storeKeychain}
	}
	return []string{storeKeychain, storeFile}
}

// jiraSite is the site named by --url, else tracker.url.
// source says where it came from: "--url" or "tracker.url in <scope> config".
func jiraSite(layers *config.Layers, flagURL string) (site, host, source string, err error) {
	site, source = flagURL, "--url"
	if site == "" {
		v, _ := layers.Get(config.KeyTrackerURL)
		site, _ = v.Value.(string)
		source = "tracker.url in " + v.Scope + " config"
	}
	if site == "" {
		return "", "", "", errors.New("no Jira site: give --url or set tracker.url")
	}
	host, err = jira.Host(site)
	return site, host, source, err
}

// jiraAccounts returns the site and the accounts to look in, most specific
// first: with repo, only the repository's own; otherwise only the global one.
// accounts[0] is where a login writes and the one logout deletes.
func (a *app) jiraAccounts(layers *config.Layers, root, flagURL string, repo bool) (site, source string, accounts []string, err error) {
	site, host, source, err := jiraSite(layers, flagURL)
	if err != nil {
		return "", "", nil, err
	}
	if repo {
		if root == "" {
			return "", "", nil, errors.New("--repo needs a git repository")
		}
		key := layers.RepoKey()
		if key == "" {
			return "", "", nil, errors.New("--repo needs a repository with an origin remote")
		}
		return site, source, []string{jira.Account(host, key)}, nil
	}
	return site, source, []string{jira.Account(host, "")}, nil
}

// loginJira implements `rig login jira`.
func (a *app) loginJira(ctx context.Context, layers *config.Layers, root string, o loginOpts) int {
	store := *o.store
	switch store {
	case "":
		store = storeKeychain
	case storeKeychain, storeFile:
	default:
		return a.fail("login", "--store must be keychain or file")
	}
	if store == storeFile && runtime.GOOS == "windows" {
		return a.fail("login", "--store file is not offered on Windows, which has Credential Manager")
	}
	// A tracker.url the repository supplies is never used: the shared check in
	// internal/config hides it, and says why here before any site is named.
	if *o.url == "" {
		if _, err := layers.TrackerURL(); errors.Is(err, config.ErrLocalCommitted) {
			fmt.Fprintln(a.stderr, "rig login: tracker.url comes from .rig/config.json, which is committed to this repository; pass --url or set it with rig config set tracker.url <url> --global (or --repo)")
			return 2
		} else if err != nil {
			return a.fail("login", "%v", err)
		}
	}
	site, source, accounts, err := a.jiraAccounts(layers, root, *o.url, *o.repo)
	if err != nil {
		return a.fail("login", "%v", err)
	}
	account := accounts[0]
	// Name the site before any prompt: it may come from a committed project
	// file, and the user should see where the token is about to go.
	host, _ := jira.Host(site)
	fmt.Fprintf(a.stderr, "Jira site: %s (from %s)\n", host, source)

	user := strings.TrimSpace(*o.user)
	if user == "" && !*o.pat {
		if tty.IsTerminal(a.stdin) {
			fmt.Fprint(a.stderr, "Jira user (email): ")
		}
		line, err := readLine(a.stdin)
		if err != nil {
			return a.fail("login", "read user: %v (give --user to pipe only the token)", err)
		}
		user = strings.TrimSpace(line)
	}
	if user == "" && !*o.pat {
		return a.fail("login", "no user")
	}
	prompt := "Jira API token: "
	if *o.pat {
		prompt = "Jira personal access token: "
	}
	token, err := tty.ReadSecret(prompt, a.stdin, a.stderr)
	if err != nil {
		return a.fail("login", "%v", err)
	}

	check := func() error { return jira.Check(ctx, a.httpClient, site, user, token) }
	if *o.pat {
		check = func() error { return jira.CheckPAT(ctx, a.httpClient, site, token) }
	}
	switch err := check(); {
	case errors.Is(err, jira.ErrRejected):
		fmt.Fprintf(a.stderr, "rig login: %v; nothing stored\n", err)
		return 1
	case err != nil:
		return a.fail("login", "%v", err)
	}
	st, err := a.secrets(store)
	if err == nil {
		err = st.Set(ctx, account, secret.Secret{User: user, Token: token})
	}
	if err != nil {
		if store == storeKeychain && runtime.GOOS == "linux" {
			return a.fail("login", "%v (with no Secret Service running, use --store file)", err)
		}
		return a.fail("login", "%v", err)
	}
	if *o.pat {
		fmt.Fprintf(a.stdout, "Logged in to %s with a personal access token (%s)\n", strings.TrimPrefix(account, "jira:"), store)
		return 0
	}
	fmt.Fprintf(a.stdout, "Logged in to %s as %s (%s)\n", strings.TrimPrefix(account, "jira:"), user, store)
	return 0
}

// readLine reads one line a byte at a time so nothing past it is consumed
// from a pipe the token follows.
func readLine(in *os.File) (string, error) {
	if in == nil {
		return "", errors.New("no input")
	}
	var line []byte
	var b [1]byte
	for {
		n, err := in.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				return strings.TrimRight(string(line), "\r"), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return string(line), nil
			}
			return "", err
		}
	}
}

// forgeTarget is the forge CLI that logs in to one host.
type forgeTarget struct {
	tool string // gh or glab
	kind forge.Kind
	host string
}

// forgeTargetFor picks the forge and host. service "" detects them from
// config and the origin remote, as the adapters do; "github" or "gitlab"
// forces the kind. A forced kind takes forge.host or the remote's host only
// when forge.kind names that kind, or, with forge.kind unset, when the host is
// recognised as that kind or cannot be placed; else the kind's public host.
func forgeTargetFor(layers *config.Layers, remote, service string) forgeTarget {
	t, _ := forgeTargetSource(layers, remote, service)
	return t
}

// forgeTargetSource is forgeTargetFor plus where the host came from:
// "forge.host in <scope> config", "the origin remote" or "default".
func forgeTargetSource(layers *config.Layers, remote, service string) (forgeTarget, string) {
	str := func(key string) string {
		v, _ := layers.Get(key)
		s, _ := v.Value.(string)
		return s
	}
	cfgKind, cfgHost, remoteHost := str(config.KeyForgeKind), str(config.KeyForgeHost), git.HostOf(remote)
	kind := forge.Kind(service)
	if service == "" {
		switch {
		case cfgKind == string(forge.GitLab):
			kind = forge.GitLab
		case cfgKind != "":
			kind = forge.GitHub
		default:
			kind = forge.GitHub
			h := cfgHost
			if h == "" {
				h = remoteHost
			}
			if k, ok := forge.KindForHost(h); ok {
				kind = k
			}
		}
	}
	host, source := "", "default"
	remoteKind, remotePlaced := forge.KindForHost(remoteHost)
	cfgHostKind, cfgHostPlaced := forge.KindForHost(cfgHost)
	switch {
	case cfgHost != "" && (cfgKind == "" || cfgKind == string(kind)) && (service == "" || cfgKind != "" || !cfgHostPlaced || cfgHostKind == kind):
		host = cfgHost
		v, _ := layers.Get(config.KeyForgeHost)
		source = "forge.host in " + v.Scope + " config"
	case remoteHost != "" && (cfgKind == "" || cfgKind == string(kind)) && (remoteKind == kind || (!remotePlaced && (service == "" || cfgKind != ""))):
		host, source = remoteHost, "the origin remote"
	case remoteHost != "" && remotePlaced && remoteKind == kind:
		host, source = remoteHost, "the origin remote"
	case kind == forge.GitLab:
		host = "gitlab.com"
	default:
		host = "github.com"
	}
	tool := "gh"
	if kind == forge.GitLab {
		tool = "glab"
	}
	return forgeTarget{tool: tool, kind: kind, host: host}, source
}

// checkForge runs `<tool> auth status --hostname <host>`. It reports whether the
// tool is logged in, as whom, and a failure to run it at all.
func (a *app) checkForge(ctx context.Context, t forgeTarget) (loggedIn bool, user string, err error) {
	if err := validHost(t.host); err != nil {
		return false, "", err
	}
	res, err := proc.Run(ctx, proc.Options{Dir: a.cwd, Timeout: toolTimeout, ScrubTokens: true}, t.tool, "auth", "status", "--hostname", t.host)
	var ee *proc.ExitError
	switch {
	case err == nil:
		out := string(res.Stdout) + "\n" + string(res.Stderr)
		if m := forgeUserRE.FindStringSubmatch(out); m != nil {
			user = m[1]
		}
		return true, user, nil
	case errors.As(err, &ee):
		return false, "", nil
	}
	return false, "", err
}

var forgeUserRE = regexp.MustCompile(`(?:account|as) (\S+)`)

// validHost refuses a host that could be read as an option or split an argument.
func validHost(h string) error {
	if h == "" || strings.HasPrefix(h, "-") || strings.ContainsAny(h, " \t\r\n") {
		return fmt.Errorf("unusable host %q", h)
	}
	return nil
}

// loginForge implements `rig login github|gitlab` and the forge half of `rig login`.
func (a *app) loginForge(ctx context.Context, layers *config.Layers, remote, service string) int {
	t, source := forgeTargetSource(layers, remote, service)
	ok, user, err := a.checkForge(ctx, t)
	if err != nil {
		fmt.Fprintf(a.stderr, "rig login: %s: %v\n", t.tool, err)
		return 1
	}
	if ok {
		if user != "" {
			fmt.Fprintf(a.stdout, "Already logged in to %s as %s (%s)\n", t.host, user, t.tool)
		} else {
			fmt.Fprintf(a.stdout, "Already logged in to %s (%s)\n", t.host, t.tool)
		}
		return 0
	}
	// Name the host before the tool runs: the origin remote of a cloned
	// repository can steer it.
	fmt.Fprintf(a.stderr, "%s login for %s (from %s)\n", t.tool, t.host, source)
	code, err := a.interactive(ctx, proc.Options{Dir: a.cwd, ScrubTokens: true}, t.tool, "auth", "login", "--hostname", t.host)
	if err != nil {
		fmt.Fprintf(a.stderr, "rig login: %s: %v\n", t.tool, err)
		return 1
	}
	if code != 0 {
		fmt.Fprintf(a.stderr, "rig login: %s auth login exited %d\n", t.tool, code)
		return 1
	}
	return 0
}

// loginStatus implements `rig login status`: one check per service, never
// the token.
func (a *app) loginStatus(ctx context.Context, layers *config.Layers, remote string, asJSON bool) int {
	out := []loginStatus{}

	t := forgeTargetFor(layers, remote, "")
	fs := loginStatus{Service: string(t.kind)}
	if ok, user, err := a.checkForge(ctx, t); err != nil {
		fs.Error = err.Error()
	} else {
		fs.LoggedIn, fs.User = ok, user
	}
	out = append(out, fs)

	kind, _ := layers.Get(config.KeyTrackerKind)
	url, _ := layers.Get(config.KeyTrackerURL)
	if kind.Value == config.TrackerJira || url.Value != nil {
		out = append(out, a.jiraStatus(ctx, layers))
	}

	if asJSON {
		return a.printJSON(out)
	}
	for _, s := range out {
		line := s.Service + ": "
		if s.LoggedIn {
			line += "logged in"
			if s.User != "" {
				line += " as " + s.User
			}
			if s.Scope != "" || s.Store != "" {
				line += fmt.Sprintf(" (%s)", strings.TrimSpace(s.Scope+" "+s.Store))
			}
		} else {
			line += "not logged in"
		}
		if s.Error != "" {
			line += ": " + s.Error
		}
		fmt.Fprintln(a.stdout, line)
	}
	return 0
}

// jiraStatus checks the stored (or RIG_JIRA_TOKEN) login against the site.
func (a *app) jiraStatus(ctx context.Context, layers *config.Layers) loginStatus {
	s := loginStatus{Service: "jira"}
	site, host, _, err := jiraSite(layers, "")
	if err != nil {
		s.Error = err.Error()
		return s
	}
	accounts := jira.Accounts(host, layers.RepoKey())

	var found *secret.Secret
	var lookupErr error
search:
	for i, account := range accounts {
		for _, kind := range storeKinds() {
			st, err := a.secrets(kind)
			if err == nil {
				var sec secret.Secret
				if sec, err = st.Get(ctx, account); err == nil {
					found = &sec
					s.Store = kind
					s.Scope = "repo"
					if i == len(accounts)-1 {
						s.Scope = "global"
					}
					break search
				}
			}
			if !errors.Is(err, secret.ErrNotFound) {
				lookupErr = fmt.Errorf("%s: %w", kind, err)
			}
		}
	}

	user, token := "", ""
	if found != nil {
		user, token = found.User, found.Token
	}
	if env := a.getenv(jiraTokenEnv); env != "" {
		token, s.Store = env, storeEnv
		if found == nil {
			s.Error = jiraTokenEnv + " is set but no user is stored: run rig login jira"
			return s
		}
	}
	if token == "" {
		if lookupErr != nil {
			s.Error = lookupErr.Error()
		}
		return s
	}
	s.User = user
	// A stored login with no user is a personal access token (rig login jira
	// --pat): it is checked against the v2 API as a Bearer credential.
	check := func() error { return jira.Check(ctx, a.httpClient, site, user, token) }
	if user == "" {
		check = func() error { return jira.CheckPAT(ctx, a.httpClient, site, token) }
	}
	switch err := check(); {
	case err == nil:
		s.LoggedIn = true
	default:
		s.Error = err.Error()
	}
	return s
}
