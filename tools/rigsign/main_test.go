package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// vectorSig is the signature over vectorMsg with the seed 0x01..0x20. Task 02
// asserts the same value.
const vectorSig = "23aa8a6bb7d7a3f3fc482ef3b7a0f51d2bd035d2e8f7f6aa01f587d87c51bb6a63a7a064b5f7936eec223f970dcf8ba9bc5c9c7e49680c049f8e8ba9e7e40506"

const vectorMsg = "abc  rig-linux-amd64\n"

func vectorSeed() []byte {
	s := make([]byte, 32)
	for i := range s {
		s[i] = byte(i + 1)
	}
	return s
}

func TestKeygenWritesKeyAndPrintsPublic(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"keygen", dir}, func(string) string { return "" }, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	path := filepath.Join(dir, "rig-signing.key")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != 32 {
		t.Fatalf("key file is not a base64 32-byte seed: %v", err)
	}
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if strings.TrimSpace(out.String()) != pub {
		t.Fatalf("stdout %q, want only the public key", out.String())
	}
	if strings.Contains(out.String()+errb.String(), strings.TrimSpace(string(raw))) {
		t.Fatal("private key leaked to output")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestKeygenRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	env := func(string) string { return "" }
	if run([]string{"keygen", dir}, env, &out, &errb) != 0 {
		t.Fatal("first keygen failed")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "rig-signing.key"))
	out.Reset()
	if code := run([]string{"keygen", dir}, env, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "rig-signing.key"))
	if !bytes.Equal(before, after) || out.Len() != 0 {
		t.Fatal("existing key was touched or a key printed")
	}
}

func TestSignWritesRawSignature(t *testing.T) {
	f := filepath.Join(t.TempDir(), "SHA256SUMS")
	os.WriteFile(f, []byte(vectorMsg), 0o644)
	key := base64.StdEncoding.EncodeToString(vectorSeed())
	var out, errb bytes.Buffer
	env := func(k string) string {
		if k == "RIG_SIGNING_KEY" {
			return key
		}
		return ""
	}
	if code := run([]string{"sign", f}, env, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	sig, err := os.ReadFile(f + ".sig")
	if err != nil || len(sig) != 64 {
		t.Fatalf("sig len %d err %v", len(sig), err)
	}
	if got := hex.EncodeToString(sig); got != vectorSig {
		t.Fatalf("signature %s, want %s", got, vectorSig)
	}
	pub := ed25519.NewKeyFromSeed(vectorSeed()).Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte(vectorMsg), sig) {
		t.Fatal("written signature does not verify over the file bytes")
	}
}

func TestSignBadKeyExits1WithoutEcho(t *testing.T) {
	f := filepath.Join(t.TempDir(), "SHA256SUMS")
	os.WriteFile(f, []byte("x"), 0o644)
	for _, val := range []string{"", "not-base64-SECRETVALUE!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		var out, errb bytes.Buffer
		code := run([]string{"sign", f}, func(string) string { return val }, &out, &errb)
		if code != 1 {
			t.Fatalf("value %q: exit %d, want 1", val, code)
		}
		if val != "" && strings.Contains(out.String()+errb.String(), val) {
			t.Fatalf("value %q echoed", val)
		}
		if _, err := os.Stat(f + ".sig"); err == nil {
			t.Fatal("sig written on failure")
		}
	}
}

func TestVerifyExits1OnUntrustedKey(t *testing.T) {
	dir := t.TempDir()
	sums := "abc  rig-linux-amd64\n"
	os.WriteFile(filepath.Join(dir, "rig-linux-amd64"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644)
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(vectorSeed()), []byte(sums))
	os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), sig, 0o644)
	var out, errb bytes.Buffer
	if code := run([]string{"verify", dir}, func(string) string { return "" }, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "signature") || out.Len() != 0 {
		t.Fatalf("stderr %q stdout %q", errb.String(), out.String())
	}
}

func TestVerifyNoDirUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"verify"}, func(string) string { return "" }, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestVector(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(vectorSeed())
	sig := ed25519.Sign(priv, []byte(vectorMsg))
	if got := hex.EncodeToString(sig); got != vectorSig {
		t.Fatalf("signature %s, want %s", got, vectorSig)
	}
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte(vectorMsg), sig) {
		t.Fatal("does not verify")
	}
}
