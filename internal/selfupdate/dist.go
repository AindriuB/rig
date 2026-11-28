package selfupdate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	sumsName = "SHA256SUMS"
	sigName  = "SHA256SUMS.sig"
)

// VerifyDist checks a release dist directory the way rig update will: every
// file other than SHA256SUMS and SHA256SUMS.sig must pass Verify against the
// embedded keys, every name SHA256SUMS lists must exist in dir, and any entry
// that is not a regular file is an error. It returns the number of assets
// verified.
func VerifyDist(dir string) (int, error) {
	sums, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		return 0, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, sigName))
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	present := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			return 0, fmt.Errorf("selfupdate: dist entry %s is not a regular file", name)
		}
		if name == sumsName || name == sigName {
			continue
		}
		present[name] = true
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return 0, err
		}
		if err := Verify(name, data, sums, sig); err != nil {
			return 0, err
		}
	}
	if len(present) == 0 {
		return 0, errors.New("selfupdate: dist holds no assets")
	}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		if name := strings.TrimPrefix(f[1], "*"); !present[name] {
			return 0, fmt.Errorf("selfupdate: SHA256SUMS lists %s but dist has no such file", name)
		}
	}
	return len(present), nil
}
