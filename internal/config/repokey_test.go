package config

import "testing"

func TestRepoKeyNormalises(t *testing.T) {
	for _, in := range []string{
		"git@github.com:acme/rig.git",
		"git@github.com:acme/rig",
		"github.com:acme/rig.git",
		"https://github.com/acme/rig",
		"https://github.com/acme/rig.git",
		"https://user@GitHub.com/acme/rig.git/",
		"https://github.com:443/acme/rig",
		"ssh://git@github.com/acme/rig.git",
		"ssh://git@github.com:22/acme/rig",
		"git://github.com/acme/rig.git",
		" https://github.com/acme/rig\n",
	} {
		got, err := RepoKey(in)
		if err != nil || got != "github.com/acme/rig" {
			t.Errorf("RepoKey(%q) = %q, %v", in, got, err)
		}
	}
	got, err := RepoKey("git@gitlab.example.com:group/sub/name.git")
	if err != nil || got != "gitlab.example.com/group/sub/name" {
		t.Errorf("nested group = %q, %v", got, err)
	}
}

func TestRepoKeyRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"/srv/git/rig.git",
		"../rig.git",
		`C:\repos\rig`,
		"./a:b",
		"file:///srv/git/rig.git",
		"https://github.com/",
		"git@github.com:",
		"git@github.com:.git",
	} {
		if got, err := RepoKey(in); err == nil {
			t.Errorf("RepoKey(%q) = %q, want error", in, got)
		}
	}
}
