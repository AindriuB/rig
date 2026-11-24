package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

type version struct {
	num [3]int
	pre []string
}

func parseVersion(s string) (version, error) {
	var v version
	orig := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("selfupdate: %q is not a semantic version", orig)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("selfupdate: %q is not a semantic version", orig)
		}
		v.num[i] = n
	}
	return v, nil
}

// CompareVersions returns -1, 0 or 1 as a is below, equal to or above b. A
// leading "v" is accepted; a prerelease sorts below its release.
func CompareVersions(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if va.num[i] != vb.num[i] {
			if va.num[i] < vb.num[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, nil
	case len(va.pre) == 0:
		return 1, nil
	case len(vb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		x, y := va.pre[i], vb.pre[i]
		if x == y {
			continue
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if nx < ny {
				return -1, nil
			}
			return 1, nil
		case ex == nil:
			return -1, nil
		case ey == nil:
			return 1, nil
		case x < y:
			return -1, nil
		default:
			return 1, nil
		}
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1, nil
	case len(va.pre) > len(vb.pre):
		return 1, nil
	}
	return 0, nil
}

// SignedEra reports whether v is a release that ships signatures (>= 0.5.2).
func SignedEra(v string) (bool, error) {
	c, err := CompareVersions(v, "0.5.2")
	if err != nil {
		return false, err
	}
	return c >= 0, nil
}
