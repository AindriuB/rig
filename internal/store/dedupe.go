package store

import "strings"

// DefaultKey is the key two findings share when they describe the same
// problem: severity, the path without any trailing ":line" (or ":line:col"),
// and the summary lower-cased with its whitespace collapsed, joined by "|".
func DefaultKey(severity, path, summary string) string {
	return severity + "|" + stripLine(path) + "|" + strings.ToLower(strings.Join(strings.Fields(summary), " "))
}

// stripLine removes trailing ":<digits>" segments from path.
func stripLine(path string) string {
	for {
		i := strings.LastIndexByte(path, ':')
		if i < 0 || i == len(path)-1 || strings.Trim(path[i+1:], "0123456789") != "" {
			return path
		}
		path = path[:i]
	}
}

// FindOpenByKey returns the open finding with key, if there is one.
func FindOpenByKey(findings []Finding, key string) (Finding, bool) {
	for _, f := range findings {
		if f.Status == StatusOpen && f.Key == key {
			return f, true
		}
	}
	return Finding{}, false
}
