// Package jira checks a Jira Cloud login. It makes the one call rig needs to
// know a user and token are good, and never reports either in an error.
package jira

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrRejected reports that Jira answered 401 or 403: the user or token is
// wrong, or lacks access.
var ErrRejected = errors.New("jira rejected the user or token")

// Host returns the host (with a port, if any) that names a site in a secret
// account, or an error if site is not a usable Jira URL.
func Host(site string) (string, error) {
	u, err := parse(site)
	if err != nil {
		return "", err
	}
	return strings.ToLower(u.Host), nil
}

// parse validates site: an http(s) URL with a host and no embedded
// credentials. Plain http is accepted only for a loopback host, so the token
// never crosses a network in the clear.
func parse(site string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(site))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("jira site %q is not a URL", site)
	}
	if u.User != nil {
		return nil, errors.New("jira site must not contain a user or password")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback(u.Hostname()) {
			return nil, fmt.Errorf("jira site %q must use https", site)
		}
	default:
		return nil, fmt.Errorf("jira site %q must be an https URL", site)
	}
	return u, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Site validates site as Host does and returns the parsed URL: the one place
// that decides which hosts a Jira token may go to. It is https, or http for a
// loopback host only, with no embedded credentials.
func Site(site string) (*url.URL, error) { return parse(site) }

// Account names the secret entry for a Jira site: "jira:<host>", or
// "jira:<host>:<repoKey>" for a repository's own login when repoKey is not
// empty. host is what Host returns.
func Account(host, repoKey string) string {
	if repoKey == "" {
		return "jira:" + host
	}
	return "jira:" + host + ":" + repoKey
}

// Accounts returns the entries to look in for a site, most specific first: the
// repository's own when repoKey is not empty, then the global one. A login
// writes to the first.
func Accounts(host, repoKey string) []string {
	if repoKey == "" {
		return []string{Account(host, "")}
	}
	return []string{Account(host, repoKey), Account(host, "")}
}

// Check asks site who the user is: GET <site>/rest/api/3/myself with basic
// auth. A 200 is nil, a 401 or 403 is ErrRejected, and anything else (a
// redirect, a server error, no answer) is another error. Redirects are not
// followed, so the credentials go only to site.
func Check(ctx context.Context, client *http.Client, site, user, token string) error {
	return check(ctx, client, site, "/rest/api/3/myself", func(r *http.Request) { r.SetBasicAuth(user, token) })
}

// CheckPAT is Check for a Server/Data Center personal access token:
// GET <site>/rest/api/2/myself with the token as a Bearer credential and no
// user.
func CheckPAT(ctx context.Context, client *http.Client, site, token string) error {
	return check(ctx, client, site, "/rest/api/2/myself", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
}

func check(ctx context.Context, client *http.Client, site, path string, auth func(*http.Request)) error {
	u, err := parse(site)
	if err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	endpoint := strings.TrimRight(u.String(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("jira check: %w", err)
	}
	auth(req)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		// url.Error carries the URL, which holds no credential.
		return fmt.Errorf("jira check: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrRejected, resp.StatusCode)
	}
	return fmt.Errorf("jira check: unexpected HTTP %d from %s", resp.StatusCode, u.Host)
}
