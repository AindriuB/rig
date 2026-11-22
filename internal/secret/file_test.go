package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file store is not available on Windows")
	}
}

func TestFileRoundTrip(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	st := File(path)

	if _, err := st.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	want := Secret{User: "u@example.com", Token: "tok"}
	if err := st.Set(ctx, "a", want); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, "b", Secret{User: "x", Token: "y"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, "a")
	if err != nil || got != want {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, want)
	}
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if _, err := st.Get(ctx, "b"); err != nil {
		t.Fatalf("other account lost: %v", err)
	}
}

func TestFileModes(t *testing.T) {
	skipWindows(t)
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	if err := File(path).Set(context.Background(), "a", Secret{Token: "t"}); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover files in directory: %v", entries)
	}
}

func TestFileRefusesOpenPermissions(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		open func(file, dir string)
	}{
		{"file", func(file, dir string) { os.Chmod(file, 0o644) }},
		{"directory", func(file, dir string) { os.Chmod(dir, 0o755) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "d")
			file := filepath.Join(dir, "c.json")
			st := File(file)
			if err := st.Set(ctx, "a", Secret{Token: "t"}); err != nil {
				t.Fatal(err)
			}
			tc.open(file, dir)
			if _, err := st.Get(ctx, "a"); err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("Get = %v, want permission error", err)
			}
			if err := st.Set(ctx, "b", Secret{}); err == nil {
				t.Error("Set succeeded on an open path")
			}
			if err := st.Delete(ctx, "a"); err == nil {
				t.Error("Delete succeeded on an open path")
			}
		})
	}
}
