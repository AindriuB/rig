//go:build linux

package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AindriuB/rig/internal/proc"
)

// Keychain returns the Secret Service store, reached through secret-tool.
// It fails when secret-tool is not installed; rig never falls back silently.
func Keychain() (Store, error) {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return nil, errors.New("secret: secret-tool is not installed; use --store file")
	}
	return keychain{}, nil
}

type keychain struct{}

// Set sends the JSON secret on stdin, which secret-tool store reads.
func (keychain) Set(ctx context.Context, account string, s Secret) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	b, err := encode(s)
	if err != nil {
		return err
	}
	_, err = proc.Run(ctx, proc.Options{Timeout: commandTimeout, Stdin: bytes.NewReader(b)}, "secret-tool",
		"store", "--label=rig", "service", Service, "account", account)
	if err != nil {
		return fmt.Errorf("secret: keychain set: %w", withStderr(err))
	}
	return nil
}

func (keychain) Get(ctx context.Context, account string) (Secret, error) {
	if err := checkAccount(account); err != nil {
		return Secret{}, err
	}
	res, err := proc.Run(ctx, proc.Options{Timeout: commandTimeout}, "secret-tool",
		"lookup", "service", Service, "account", account)
	if err != nil {
		var ee *proc.ExitError
		// lookup exits 1 with no output when nothing matches.
		if errors.As(err, &ee) && ee.Code == 1 && len(bytes.TrimSpace(res.Stdout)) == 0 && len(bytes.TrimSpace(ee.Stderr)) == 0 {
			return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, account)
		}
		return Secret{}, fmt.Errorf("secret: keychain get: %w", withStderr(err))
	}
	if len(bytes.TrimSpace(res.Stdout)) == 0 {
		return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, account)
	}
	return decode(res.Stdout)
}

func (keychain) Delete(ctx context.Context, account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	_, err := proc.Run(ctx, proc.Options{Timeout: commandTimeout}, "secret-tool",
		"clear", "service", Service, "account", account)
	if err != nil {
		return fmt.Errorf("secret: keychain delete: %w", withStderr(err))
	}
	return nil
}

// withStderr adds the program's first error line to an exit error.
func withStderr(err error) error {
	var ee *proc.ExitError
	if errors.As(err, &ee) {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n"); line != "" {
			return fmt.Errorf("%w: %s", err, line)
		}
	}
	return err
}
