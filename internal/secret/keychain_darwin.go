//go:build darwin

package secret

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/AindriuB/rig/internal/proc"
)

// securityNotFound is the exit code security uses for a missing item.
const securityNotFound = 44

// Keychain returns the macOS Keychain store, reached through security(1).
func Keychain() (Store, error) { return keychain{}, nil }

type keychain struct{}

// quote wraps s for a security -i command line.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Set writes the command to security's stdin so the secret is never in an
// argument list. The JSON is base64 encoded to avoid any quoting.
func (keychain) Set(ctx context.Context, account string, s Secret) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	b, err := encode(s)
	if err != nil {
		return err
	}
	cmd := "add-generic-password -U -s " + Service + " -a " + quote(account) +
		" -w " + base64.StdEncoding.EncodeToString(b) + "\n"
	_, err = proc.Run(ctx, proc.Options{Timeout: commandTimeout, Stdin: strings.NewReader(cmd)}, "security", "-i")
	if err != nil {
		return fmt.Errorf("secret: keychain set: %w", err)
	}
	return nil
}

func (keychain) Get(ctx context.Context, account string) (Secret, error) {
	if err := checkAccount(account); err != nil {
		return Secret{}, err
	}
	res, err := proc.Run(ctx, proc.Options{Timeout: commandTimeout}, "security",
		"find-generic-password", "-s", Service, "-a", account, "-w")
	if err != nil {
		if isNotFound(err) {
			return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, account)
		}
		return Secret{}, fmt.Errorf("secret: keychain get: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(res.Stdout)))
	if err != nil {
		return Secret{}, fmt.Errorf("secret: stored value is not valid: %w", err)
	}
	return decode(raw)
}

func (keychain) Delete(ctx context.Context, account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	_, err := proc.Run(ctx, proc.Options{Timeout: commandTimeout}, "security",
		"delete-generic-password", "-s", Service, "-a", account)
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("secret: keychain delete: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	var ee *proc.ExitError
	return errors.As(err, &ee) && ee.Code == securityNotFound
}
