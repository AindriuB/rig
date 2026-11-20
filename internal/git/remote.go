package git

import (
	"net/url"
	"strings"
)

// HostOf extracts the host from a remote URL: "github.com" from
// https://github.com/o/r.git, ssh://git@github.com:22/o/r and
// git@github.com:o/r.git. It is "" for a local path.
func HostOf(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	// scp-like: [user@]host:path. A slash before the colon means a path.
	host, _, ok := strings.Cut(remote, ":")
	if !ok || host == "" || strings.ContainsAny(host, `/\`) {
		return ""
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if len(host) < 2 { // a Windows drive letter, C:\repo
		return ""
	}
	return strings.ToLower(host)
}
