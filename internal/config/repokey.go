package config

import (
	"fmt"
	"net/url"
	"strings"
)

// RepoKey normalises a remote URL to host/path, the key of the repository's
// section in the global file. ssh, scp-like, git and https forms of one
// remote give the same key: the user, the port, a trailing slash and a
// ".git" suffix are dropped, and the host is lower-cased. A local path has
// no repo key and is an error.
func RepoKey(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	var host, path string
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", fmt.Errorf("repo key of %q: %w", remote, err)
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:path. A slash before the colon means a path.
		h, p, ok := strings.Cut(remote, ":")
		if ok && !strings.ContainsAny(h, `/\`) {
			if i := strings.LastIndex(h, "@"); i >= 0 {
				h = h[i+1:]
			}
			if len(h) >= 2 { // not a Windows drive letter, C:\repo
				host, path = h, p
			}
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return "", fmt.Errorf("remote %q has no repo key: it names no host and path", remote)
	}
	return strings.ToLower(host) + "/" + path, nil
}
