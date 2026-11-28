package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// makeDist writes two assets, SHA256SUMS and a signature from the throwaway
// key, and returns the directory and the key's base64 public half.
func makeDist(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	sums := ""
	for _, name := range []string{"rig-linux-amd64", "rig-darwin-arm64"} {
		data := []byte("binary " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		sums += hex.EncodeToString(h[:]) + "  " + name + "\n"
	}
	pub, priv := vector()
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, []byte(sums))
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), sig, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, base64.StdEncoding.EncodeToString(pub)
}

func trust(t *testing.T, key string) {
	t.Helper()
	old := trustedKeys
	trustedKeys = []string{key}
	t.Cleanup(func() { trustedKeys = old })
}

func TestVerifyDistRejectsUnembeddedKey(t *testing.T) {
	dir, _ := makeDist(t)
	var ce *CheckError
	if _, err := VerifyDist(dir); !errors.As(err, &ce) || ce.Check != "signature" {
		t.Fatalf("got %v, want signature CheckError", err)
	}
}

func TestVerifyDistAcceptsTrustedKey(t *testing.T) {
	dir, key := makeDist(t)
	trust(t, key)
	n, err := VerifyDist(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("verified %d assets, want 2", n)
	}
}

func TestVerifyDistRejectsNonRegularEntry(t *testing.T) {
	dir, key := makeDist(t)
	trust(t, key)
	if err := os.Mkdir(filepath.Join(dir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if n, err := VerifyDist(dir); err == nil {
		t.Fatalf("got %d, nil; want error for subdirectory", n)
	}
}

func TestVerifyDistFailures(t *testing.T) {
	t.Run("changed asset", func(t *testing.T) {
		dir, key := makeDist(t)
		trust(t, key)
		os.WriteFile(filepath.Join(dir, "rig-linux-amd64"), []byte("tampered"), 0o644)
		var ce *CheckError
		if _, err := VerifyDist(dir); !errors.As(err, &ce) || ce.Check != "checksum" {
			t.Fatalf("got %v, want checksum CheckError", err)
		}
	})
	t.Run("missing sig", func(t *testing.T) {
		dir, key := makeDist(t)
		trust(t, key)
		os.Remove(filepath.Join(dir, "SHA256SUMS.sig"))
		if _, err := VerifyDist(dir); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("listed but missing file", func(t *testing.T) {
		dir, key := makeDist(t)
		trust(t, key)
		os.Remove(filepath.Join(dir, "rig-darwin-arm64"))
		if _, err := VerifyDist(dir); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty dist", func(t *testing.T) {
		_, key := makeDist(t)
		trust(t, key)
		if _, err := VerifyDist(t.TempDir()); err == nil {
			t.Fatal("want error")
		}
	})
}
