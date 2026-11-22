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

// Check asks site who the user is: GET <site>/rest/api/3/myself with basic
// auth. A 200 is nil, a 401 or 403 is ErrRejected, and anything else (a
// redirect, a server error, no answer) is another error. Redirects are not
// followed, so the credentials go only to site.
func Check(ctx context.Context, client *http.Client, site, user, token string) error {
	u, err := parse(site)
	if err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	endpoint := strings.TrimRight(u.String(), "/") + "/rest/api/3/myself"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("jira check: %w", err)
	}
	req.SetBasicAuth(user, token)
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
