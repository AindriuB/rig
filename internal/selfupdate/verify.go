package selfupdate

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// CheckError reports which verification check failed: "signature" or
// "checksum".
type CheckError struct {
	Check  string
	Detail string
}

func (e *CheckError) Error() string {
	return "selfupdate: " + e.Check + " check failed: " + e.Detail
}

func decodeKeys(list []string) ([]ed25519.PublicKey, error) {
	var out []ed25519.PublicKey
	for i, s := range list {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("selfupdate: embedded key %d is not a base64 32-byte key", i)
		}
		out = append(out, ed25519.PublicKey(raw))
	}
	return out, nil
}

// Verify checks sig over sums with the embedded keys, then the SHA-256 of
// binary against the line for asset in sums.
func Verify(asset string, binary, sums, sig []byte) error {
	keys, err := decodeKeys(trustedKeys)
	if err != nil {
		return &CheckError{Check: "signature", Detail: err.Error()}
	}
	return verifyWith(keys, asset, binary, sums, sig)
}

func verifyWith(keys []ed25519.PublicKey, asset string, binary, sums, sig []byte) error {
	if len(sig) != ed25519.SignatureSize {
		return &CheckError{Check: "signature", Detail: fmt.Sprintf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)}
	}
	ok := false
	for _, k := range keys {
		if ed25519.Verify(k, sums, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return &CheckError{Check: "signature", Detail: "no embedded key accepts the signature over SHA256SUMS"}
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = strings.ToLower(f[0])
			break
		}
	}
	if want == "" {
		return &CheckError{Check: "checksum", Detail: asset + " is not listed in SHA256SUMS"}
	}
	got := sha256.Sum256(binary)
	if hex.EncodeToString(got[:]) != want {
		return &CheckError{Check: "checksum", Detail: asset + " does not match its SHA256SUMS line"}
	}
	return nil
}
