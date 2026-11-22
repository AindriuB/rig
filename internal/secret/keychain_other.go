//go:build !darwin && !linux && !windows

package secret

import "fmt"

// Keychain reports that this operating system has no supported store.
func Keychain() (Store, error) {
	return nil, fmt.Errorf("secret: %w: use --store file", ErrUnsupported)
}
