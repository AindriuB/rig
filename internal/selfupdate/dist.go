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
// embedded keys, and every name SHA256SUMS lists must exist in dir.
func VerifyDist(dir string) error {
	sums, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, sigName))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || name == sumsName || name == sigName {
			continue
		}
		present[name] = true
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := Verify(name, data, sums, sig); err != nil {
			return err
		}
	}
	if len(present) == 0 {
		return errors.New("selfupdate: dist holds no assets")
	}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		if name := strings.TrimPrefix(f[1], "*"); !present[name] {
			return fmt.Errorf("selfupdate: SHA256SUMS lists %s but dist has no such file", name)
		}
	}
	return nil
}
