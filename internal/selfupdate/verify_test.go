package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"testing"
)

// preKey marks a build made before the maintainer's public key was embedded.
// Without it, an empty production key list fails the build.
var preKey = flag.Bool("prekey", false, "allow an empty production key list (pre-key build)")

func vector() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

func TestProductionKeys(t *testing.T) {
	if len(trustedKeys) == 0 {
		if *preKey {
			t.Skip("pre-key build: trustedKeys is empty")
		}
		t.Fatal("trustedKeys is empty: add the public key from rigsign keygen (or run with -prekey for a pre-key build)")
	}
	keys, err := decodeKeys(trustedKeys)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if len(k) != 32 {
			t.Errorf("key %d is %d bytes", i, len(k))
		}
	}
}

func TestDecodeKeysRejectsBad(t *testing.T) {
	if _, err := decodeKeys([]string{base64.StdEncoding.EncodeToString([]byte("short"))}); err == nil {
		t.Error("want error")
	}
}

func checkName(t *testing.T, err error, want string) {
	t.Helper()
	var ce *CheckError
	if !errors.As(err, &ce) || ce.Check != want {
		t.Fatalf("err=%v want check %s", err, want)
	}
}

func TestVerifyWith(t *testing.T) {
	pub, priv := vector()
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	bin := []byte("binary")
	h := sha256.Sum256(bin)
	sums := []byte(hex.EncodeToString(h[:]) + "  rig-linux-amd64\n")
	sig := ed25519.Sign(priv, sums)
	asset := "rig-linux-amd64"

	if err := verifyWith([]ed25519.PublicKey{other, pub}, asset, bin, sums, sig); err != nil {
		t.Fatalf("rotation list: %v", err)
	}
	checkName(t, verifyWith([]ed25519.PublicKey{other}, asset, bin, sums, sig), "signature")
	checkName(t, verifyWith([]ed25519.PublicKey{pub}, asset, bin, sums, sig[:63]), "signature")
	checkName(t, verifyWith([]ed25519.PublicKey{pub}, asset, bin, sums, nil), "signature")
	checkName(t, verifyWith([]ed25519.PublicKey{pub}, asset, []byte("evil"), sums, sig), "checksum")
	checkName(t, verifyWith([]ed25519.PublicKey{pub}, "rig-darwin-arm64", bin, sums, sig), "checksum")
	// signature is checked before the hash
	checkName(t, verifyWith([]ed25519.PublicKey{other}, asset, []byte("evil"), sums, sig), "signature")
}

func TestVectorMessage(t *testing.T) {
	pub, priv := vector()
	msg := []byte("abc  rig-linux-amd64\n")
	if !ed25519.Verify(pub, msg, ed25519.Sign(priv, msg)) {
		t.Fatal("vector does not verify")
	}
}

func TestVerifyEmptyProductionKeys(t *testing.T) {
	if len(trustedKeys) != 0 {
		t.Skip("keys present")
	}
	checkName(t, Verify("rig-linux-amd64", nil, nil, make([]byte, 64)), "signature")
}
