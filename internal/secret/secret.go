// Package secret stores one login secret per account.
//
// Keychain returns the operating system's store and File an explicit file
// store; neither is chosen for the caller, so a missing keychain is an error
// and never a silent fallback to a file. Every subprocess goes through
// internal/proc as an argument vector, and a token never appears in one.
package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service is the keychain service name every entry is stored under.
const Service = "rig"

// ErrNotFound reports that no secret is stored for the account.
var ErrNotFound = errors.New("secret not found")

// ErrUnsupported reports that no store exists for this operating system.
var ErrUnsupported = errors.New("secret store not supported on this OS")

// commandTimeout bounds each keychain subprocess.
const commandTimeout = 30 * time.Second

// Secret is the login secret kept for one account.
type Secret struct {
	User  string `json:"user"`
	Token string `json:"token"`
}

// Store keeps one Secret per account.
type Store interface {
	// Get returns the account's secret, or ErrNotFound.
	Get(ctx context.Context, account string) (Secret, error)
	// Set stores the secret, replacing any earlier one.
	Set(ctx context.Context, account string, s Secret) error
	// Delete removes the account's secret. A missing account is not an error.
	Delete(ctx context.Context, account string) error
}

// checkAccount rejects an account name no adapter can carry safely.
func checkAccount(account string) error {
	if account == "" {
		return errors.New("secret: empty account")
	}
	if strings.ContainsFunc(account, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("secret: account %q has a control character", account)
	}
	return nil
}

func encode(s Secret) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("secret: encode: %w", err)
	}
	return b, nil
}

func decode(b []byte) (Secret, error) {
	var s Secret
	if err := json.Unmarshal(b, &s); err != nil {
		return Secret{}, fmt.Errorf("secret: stored value is not valid: %w", err)
	}
	return s, nil
}
