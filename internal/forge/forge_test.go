package forge

import "testing"

func TestRollup(t *testing.T) {
	c := func(states ...CheckState) []Check {
		var out []Check
		for _, s := range states {
			out = append(out, Check{State: s})
		}
		return out
	}
	cases := []struct {
		checks []Check
		want   CI
	}{
		{nil, CINone},
		{c(CheckPass, CheckSkipped), CIGreen},
		{c(CheckSkipped), CIGreen},
		{c(CheckPass, CheckPending), CIRunning},
		{c(CheckPending, CheckFail), CIRed},
		{c(CheckPass, CheckCancel), CIRed},
	}
	for _, tc := range cases {
		if got := Rollup(tc.checks); got != tc.want {
			t.Errorf("Rollup(%v) = %s, want %s", tc.checks, got, tc.want)
		}
	}
}

func TestKindForHost(t *testing.T) {
	cases := map[string]Kind{
		"github.com":         GitHub,
		"GitHub.com":         GitHub,
		"github.example.com": GitHub,
		"ghe.corp.net":       GitHub,
		"gitlab.com":         GitLab,
		"gitlab.example.com": GitLab,
		"git.example.com":    "",
		"notgithub.com":      "",
		"":                   "",
	}
	for host, want := range cases {
		got, ok := KindForHost(host)
		if got != want || ok != (want != "") {
			t.Errorf("KindForHost(%q) = %q, %v; want %q", host, got, ok, want)
		}
	}
}
