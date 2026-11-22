package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckValue(t *testing.T) {
	abs, _ := filepath.Abs(filepath.Join("wf", "flow.json"))
	good := map[string][]string{
		KeyForgeKind:      {"github", "gitlab"},
		KeyForgeHost:      {"github.com", "git.example.com:8443", "localhost"},
		KeyTrackerKind:    {"local", "jira", "github", "gitlab"},
		KeyTrackerURL:     {"https://acme.atlassian.net", "https://jira.example.com/base"},
		KeyTrackerProject: {"RIG", "B2B", "acme/rig", "group/sub/name"},
		KeyWorkflow:       {"basic", "two-phase", abs},
	}
	bad := map[string][]string{
		KeyForgeKind:      {"", "bitbucket", "GitHub"},
		KeyForgeHost:      {"", "-x.com", "a b", "https://github.com", "host/path"},
		KeyTrackerKind:    {"", "plain", "Jira"},
		KeyTrackerURL:     {"", "http://acme.atlassian.net", "https://", "acme.atlassian.net", "https://u:p@acme.atlassian.net", "ftp://x"},
		KeyTrackerProject: {"", "rig", "R", "acme/", "/rig", "a b"},
		KeyWorkflow:       {"", "Basic", "rel/path.json", "../x"},
	}
	for key, vs := range good {
		for _, v := range vs {
			if err := checkValue(key, v); err != nil {
				t.Errorf("checkValue(%s, %q) = %v", key, v, err)
			}
		}
	}
	for key, vs := range bad {
		for _, v := range vs {
			if err := checkValue(key, v); err == nil {
				t.Errorf("checkValue(%s, %q) = nil", key, v)
			}
		}
	}
}

func TestCheckProjectByKind(t *testing.T) {
	cases := []struct {
		kind, project string
		ok            bool
	}{
		{"jira", "RIG", true},
		{"jira", "acme/rig", false},
		{"github", "acme/rig", true},
		{"github", "a/b/c", false},
		{"github", "RIG", false},
		{"gitlab", "group/sub/name", true},
		{"gitlab", "RIG", false},
		{"local", "RIG", true},
		{"local", "acme/rig", true},
	}
	for _, c := range cases {
		if err := checkProject(c.kind, c.project); (err == nil) != c.ok {
			t.Errorf("checkProject(%s, %s) = %v", c.kind, c.project, err)
		}
	}
}

func TestCheckSettableRefusesCredentials(t *testing.T) {
	for _, key := range []string{
		"tracker.token", "jira.Token", "tracker.password", "x.SECRET",
		"tracker.apikey", "tracker.apiKey", "forge.key", "jira.auth",
		"tracker.credential", "tracker.credentials", "token",
	} {
		err := checkSettable(key)
		if err == nil || !strings.Contains(err.Error(), "rig login") {
			t.Errorf("checkSettable(%q) = %v, want a refusal naming rig login", key, err)
		}
	}
}

func TestCheckSettableRefusesGlobalOnlyAndUnknown(t *testing.T) {
	for _, key := range []string{
		"push.prefixes", "pushPrefixes", "pushprefixes", "push_prefixes",
		"Push.Prefixes", "PUSH-PREFIXES", "workflow", "Workflow",
	} {
		err := checkSettable(key)
		if err == nil || !strings.Contains(err.Error(), "global file") {
			t.Errorf("checkSettable(%q) = %v, want global-only refusal", key, err)
		}
	}
	for _, key := range []string{"forge", "forge.kinds", "tracker", "repos", "", "tracker.url.x"} {
		if err := checkSettable(key); err == nil {
			t.Errorf("checkSettable(%q) = nil", key)
		}
	}
	for _, key := range []string{KeyForgeKind, KeyForgeHost, KeyTrackerKind, KeyTrackerURL, KeyTrackerProject} {
		if err := checkSettable(key); err != nil {
			t.Errorf("checkSettable(%q) = %v", key, err)
		}
	}
}
