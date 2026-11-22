package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		status   int
		ok       bool
		rejected bool
	}{{200, true, false}, {401, false, true}, {403, false, true}, {500, false, false}, {302, false, false}} {
		var gotUser, gotPass, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, _ = r.BasicAuth()
			gotPath = r.URL.Path
			if c.status == 302 {
				w.Header().Set("Location", "http://127.0.0.1:1/x")
			}
			w.WriteHeader(c.status)
		}))
		err := Check(context.Background(), srv.Client(), srv.URL+"/", "me@x.io", "TOK")
		srv.Close()
		if gotUser != "me@x.io" || gotPass != "TOK" || gotPath != "/rest/api/3/myself" {
			t.Fatalf("%d: request %q %q %q", c.status, gotUser, gotPass, gotPath)
		}
		if (err == nil) != c.ok || errors.Is(err, ErrRejected) != c.rejected {
			t.Fatalf("%d: err = %v", c.status, err)
		}
		if err != nil && strings.Contains(err.Error(), "TOK") {
			t.Fatalf("error holds the token: %v", err)
		}
	}
}

func TestCheckRefusesBadSites(t *testing.T) {
	for _, s := range []string{"", "acme.atlassian.net", "http://acme.atlassian.net", "ftp://x.io", "https://u:p@x.io"} {
		if err := Check(context.Background(), nil, s, "u", "t"); err == nil || errors.Is(err, ErrRejected) {
			t.Fatalf("%q: err = %v", s, err)
		}
	}
}

func TestHost(t *testing.T) {
	if h, err := Host("https://Acme.Atlassian.net/"); err != nil || h != "acme.atlassian.net" {
		t.Fatalf("Host = %q, %v", h, err)
	}
}
