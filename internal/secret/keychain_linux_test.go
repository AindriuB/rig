//go:build linux

package secret

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSecretToolTokenOnlyOnStdin(t *testing.T) {
	st := newStub(t, "secret-tool")
	kc, err := Keychain()
	if err != nil {
		t.Fatal(err)
	}
	if err := kc.Set(context.Background(), "jira:example.com", Secret{User: "me", Token: testToken}); err != nil {
		t.Fatal(err)
	}
	argv := st.read(t, "argv")
	noToken(t, "argv", argv)
	if want := "store\n--label=rig\nservice\nrig\naccount\njira:example.com\n"; argv != want {
		t.Errorf("argv = %q, want %q", argv, want)
	}
	if in := st.read(t, "stdin"); !strings.Contains(in, testToken) {
		t.Errorf("stdin = %q, want the secret", in)
	}
}

func TestSecretToolGetDeleteNotFound(t *testing.T) {
	st := newStub(t, "secret-tool")
	kc, _ := Keychain()
	ctx := context.Background()

	st.write(t, "out", `{"user":"me","token":"`+testToken+`"}`)
	got, err := kc.Get(ctx, "a")
	if err != nil || got != (Secret{User: "me", Token: testToken}) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if want := "lookup\nservice\nrig\naccount\na\n"; st.read(t, "argv") != want {
		t.Errorf("argv = %q", st.read(t, "argv"))
	}

	st.reset()
	st.write(t, "code", "1")
	if _, err := kc.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get exit 1 = %v, want ErrNotFound", err)
	}
	st.reset()
	if err := kc.Delete(ctx, "a"); err != nil {
		t.Errorf("Delete = %v", err)
	}
	if want := "clear\nservice\nrig\naccount\na\n"; st.read(t, "argv") != want {
		t.Errorf("argv = %q", st.read(t, "argv"))
	}
}

func TestKeychainWithoutSecretTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Keychain()
	if err == nil || !strings.Contains(err.Error(), "--store file") {
		t.Fatalf("Keychain() = %v, want an error naming --store file", err)
	}
}

func TestRealKeychain(t *testing.T) {
	if os.Getenv("RIG_TEST_KEYCHAIN") != "1" {
		t.Skip("set RIG_TEST_KEYCHAIN=1 to use the real keychain")
	}
	kc, err := Keychain()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	acct := "rig-test:" + t.Name()
	t.Cleanup(func() { kc.Delete(ctx, acct) })
	want := Secret{User: "u", Token: "t"}
	if err := kc.Set(ctx, acct, want); err != nil {
		t.Fatal(err)
	}
	if got, err := kc.Get(ctx, acct); err != nil || got != want {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := kc.Delete(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.Get(ctx, acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
}
