package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p, mode)
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStageSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	real := filepath.Join(root, "real")
	links := filepath.Join(root, "links")
	os.Mkdir(real, 0o755)
	os.Mkdir(links, 0o755)
	target := filepath.Join(real, "rig")
	link := filepath.Join(links, "rig")
	write(t, target, "old", 0o755)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	s, err := Stage(link, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got, _ := filepath.EvalSymlinks(filepath.Dir(s.Path())); got != want {
		t.Fatalf("staged in %s, want %s", got, want)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link not preserved: %v", err)
	}
	if read(t, link) != "new" || read(t, target) != "new" {
		t.Fatal("content not replaced through link")
	}
}

func TestCommitUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "rig")
	write(t, exe, "old", 0o750)
	s, err := Stage(exe, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new" {
		t.Fatal("not replaced")
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o750 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestStageNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "rig")
	write(t, exe, "old", 0o755)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	_, err := Stage(exe, []byte("new"))
	var nw *NotWritableError
	if !errors.As(err, &nw) || nw.Dir == "" {
		t.Fatalf("want NotWritableError, got %v", err)
	}
	if want, _ := filepath.EvalSymlinks(dir); nw.Dir != want {
		t.Fatalf("Dir %q, want %q", nw.Dir, want)
	}
	if read(t, exe) != "old" {
		t.Fatal("target changed")
	}
}

func TestDiscard(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rig")
	write(t, exe, "old", 0o755)
	s, err := Stage(exe, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("staged file remains: %v", err)
	}
	if read(t, exe) != "old" {
		t.Fatal("target changed")
	}
}

func TestWindowsSwap(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rig.exe")
	staged := filepath.Join(dir, "new")
	write(t, exe, "old", 0o755)
	write(t, staged, "new", 0o755)
	if err := windowsSwap(exe, staged); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new" || read(t, exe+".old") != "old" {
		t.Fatal("swap wrong")
	}
	// Second rename fails (staged missing): the old file must be restored.
	dir2 := t.TempDir()
	exe2 := filepath.Join(dir2, "rig.exe")
	write(t, exe2, "old", 0o755)
	if err := windowsSwap(exe2, filepath.Join(dir2, "missing")); err == nil {
		t.Fatal("want error")
	}
	if read(t, exe2) != "old" {
		t.Fatal("not rolled back")
	}
	if _, err := os.Stat(exe2 + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old remains after rollback")
	}
}

func TestCleanupOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rig")
	CleanupOld(exe) // absent: no panic
	write(t, exe+".old", "x", 0o644)
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old remains")
	}
}
