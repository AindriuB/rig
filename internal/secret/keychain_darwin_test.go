//go:build darwin

package secret

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSecurityTokenOnlyOnStdin(t *testing.T) {
	st := newStub(t, "security")
	kc, err := Keychain()
	if err != nil {
		t.Fatal(err)
	}
	want := Secret{User: "me", Token: testToken}
	if err := kc.Set(context.Background(), "jira:example.com", want); err != nil {
		t.Fatal(err)
	}
	argv := st.read(t, "argv")
	noToken(t, "argv", argv)
	if strings.TrimSpace(argv) != "-i" {
		t.Errorf("argv = %q, want only -i", argv)
	}
	in := st.read(t, "stdin")
	if !strings.HasPrefix(in, `add-generic-password -U -s rig -a "jira:example.com" -w `) {
		t.Errorf("stdin = %q", in)
	}
	noToken(t, "stdin (must be base64)", in)
	b64 := strings.TrimSpace(in[strings.LastIndex(in, " ")+1:])
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !strings.Contains(string(raw), testToken) {
		t.Errorf("stdin payload does not decode to the secret: %v", err)
	}
}

func TestSecurityGetDeleteNotFound(t *testing.T) {
	st := newStub(t, "security")
	kc, _ := Keychain()
	ctx := context.Background()

	st.write(t, "out", base64.StdEncoding.EncodeToString([]byte(`{"user":"me","token":"`+testToken+`"}`))+"\n")
	got, err := kc.Get(ctx, "a")
	if err != nil || got != (Secret{User: "me", Token: testToken}) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if want := "find-generic-password\n-s\nrig\n-a\na\n-w\n"; st.read(t, "argv") != want {
		t.Errorf("argv = %q", st.read(t, "argv"))
	}

	st.reset()
	st.write(t, "code", "44")
	if _, err := kc.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get exit 44 = %v, want ErrNotFound", err)
	}
	if err := kc.Delete(ctx, "a"); err != nil {
		t.Errorf("Delete missing = %v", err)
	}
	st.write(t, "code", "1")
	if err := kc.Delete(ctx, "a"); err == nil {
		t.Error("Delete exit 1 succeeded")
	}
}

func TestRealKeychain(t *testing.T) {
	if os.Getenv("RIG_TEST_KEYCHAIN") != "1" {
		t.Skip("set RIG_TEST_KEYCHAIN=1 to use the real keychain")
	}
	kc, _ := Keychain()
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
