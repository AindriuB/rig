// Command rigsign generates the release signing key and signs SHA256SUMS.
// It is run with go run and is never shipped as a release asset.
//
// Formats: the private key file holds the standard base64 of a 32-byte
// Ed25519 seed; the public key is the standard base64 of the 32-byte key; a
// signature file holds the 64 raw signature bytes and nothing else.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AindriuB/rig/internal/selfupdate"
)

const keyEnv = "RIG_SIGNING_KEY"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: rigsign keygen <dir> | rigsign sign <file> | rigsign verify <dir>")
		return 1
	}
	var err error
	switch args[0] {
	case "verify":
		err = selfupdate.VerifyDist(args[1])
		if err == nil {
			entries, _ := os.ReadDir(args[1])
			fmt.Fprintf(stdout, "verified %d assets\n", len(entries)-2)
		}
	case "keygen":
		err = keygen(args[1], stdout)
	case "sign":
		err = sign(args[1], getenv(keyEnv))
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, "rigsign:", err)
		return 1
	}
	return 0
}

func keygen(dir string, stdout io.Writer) error {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "rig-signing.key"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create key file: %w", err)
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(seed) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func sign(file, key string) error {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s is unset or not a base64 32-byte seed", keyEnv)
	}
	msg, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg)
	return os.WriteFile(file+".sig", sig, 0o644)
}
