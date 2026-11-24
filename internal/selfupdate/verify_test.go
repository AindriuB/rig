package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

// vectorSig is task 01's committed signature over vectorMsg (seed 0x01..0x20).
const vectorSig = "23aa8a6bb7d7a3f3fc482ef3b7a0f51d2bd035d2e8f7f6aa01f587d87c51bb6a63a7a064b5f7936eec223f970dcf8ba9bc5c9c7e49680c049f8e8ba9e7e40506"

const vectorMsg = "abc  rig-linux-amd64
"

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
		t.Fatal("trustedKeys is empty: add the public key from rigsign keygen")
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
	pub, _ := vector()
	keys, err := decodeKeys([]string{base64.StdEncoding.EncodeToString(pub)})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(vectorSig)
	if err != nil {
		t.Fatal(err)
	}
	// The signature passes (the error is the later checksum stage, since "abc"
	// is not the hash of "x"); a changed message fails at the signature stage.
	checkName(t, verifyWith(keys, "rig-linux-amd64", []byte("x"), []byte(vectorMsg), sig), "checksum")
	checkName(t, verifyWith(keys, "rig-linux-amd64", []byte("x"), []byte(vectorMsg+"x"), sig), "signature")
}

func TestVerifyEmptyProductionKeys(t *testing.T) {
	if len(trustedKeys) != 0 {
		t.Skip("keys present")
	}
	checkName(t, Verify("rig-linux-amd64", nil, nil, make([]byte, 64)), "signature")
}
