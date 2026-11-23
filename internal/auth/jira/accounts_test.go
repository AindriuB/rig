package jira

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccounts(t *testing.T) {
	if got := Account("h.io", ""); got != "jira:h.io" {
		t.Errorf("Account = %q", got)
	}
	if got := Account("h.io", "github.com/a/b"); got != "jira:h.io:github.com/a/b" {
		t.Errorf("Account(repo) = %q", got)
	}
	if got := strings.Join(Accounts("h.io", "k"), " "); got != "jira:h.io:k jira:h.io" {
		t.Errorf("Accounts = %q", got)
	}
	if got := strings.Join(Accounts("h.io", ""), " "); got != "jira:h.io" {
		t.Errorf("Accounts(global) = %q", got)
	}
}

func TestCheckPAT(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer PATX" {
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	if err := CheckPAT(context.Background(), srv.Client(), srv.URL, "PATX"); err != nil {
		t.Fatalf("CheckPAT: %v", err)
	}
	if gotPath != "/rest/api/2/myself" {
		t.Errorf("path %q", gotPath)
	}
	err := CheckPAT(context.Background(), srv.Client(), srv.URL, "WRONG")
	if !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "WRONG") {
		t.Errorf("err = %v", err)
	}
}

func TestSiteRefusesPlainHTTPOffLoopback(t *testing.T) {
	if _, err := Site("http://jira.example.com"); err == nil {
		t.Error("http to a non-loopback host accepted")
	}
	if _, err := Site("http://127.0.0.1:8080"); err != nil {
		t.Errorf("loopback http: %v", err)
	}
}
