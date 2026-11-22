package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func plumbingRepo(t *testing.T, extra ...string) Repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	sh(t, dir, append([]string{"init", "-q", "-b", "main"}, extra...)...)
	return Repo{Dir: dir}
}

// commitData writes blob -> tree -> commit and returns all three ids.
func commitData(t *testing.T, r Repo, data string, parents ...string) (blob, tree, commit string) {
	t.Helper()
	var err error
	if blob, err = r.WriteBlob(ctx, []byte(data)); err != nil {
		t.Fatal(err)
	}
	if tree, err = r.MakeTree(ctx, []TreeEntry{{"100644", "blob", blob, "log.jsonl"}}); err != nil {
		t.Fatal(err)
	}
	if commit, err = r.CommitTree(ctx, tree, parents, "msg\n"); err != nil {
		t.Fatal(err)
	}
	return
}

// AC1: the calls work as argument vectors in a throwaway repository.
func TestPlumbingRoundTrip(t *testing.T) {
	r := plumbingRepo(t)
	_, _, c1 := commitData(t, r, "one\n")
	_, _, c2 := commitData(t, r, "one\ntwo\n", c1)
	if err := r.UpdateRef(ctx, "refs/rig/tasks/A", c1, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, "refs/rig/tasks/B", c2, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, "refs/rig/tasks/A", c2, c1); err != nil {
		t.Fatal(err)
	}
	if oid, ok, err := r.ResolveRef(ctx, "refs/rig/tasks/A"); err != nil || !ok || oid != c2 {
		t.Fatalf("ResolveRef = %q %v %v", oid, ok, err)
	}
	if _, ok, err := r.ResolveRef(ctx, "refs/rig/tasks/none"); err != nil || ok {
		t.Fatalf("missing ref: ok=%v err=%v", ok, err)
	}
	refs, err := r.ReadRefs(ctx, "refs/rig/tasks/")
	if err != nil || len(refs) != 2 || refs[0].Name != "refs/rig/tasks/A" || refs[0].OID != c2 || refs[1].Name != "refs/rig/tasks/B" {
		t.Fatalf("ReadRefs = %+v %v", refs, err)
	}
	if refs, err := r.ReadRefs(ctx, "refs/nothing/"); err != nil || len(refs) != 0 {
		t.Fatalf("empty prefix = %+v %v", refs, err)
	}
	if got := sh(t, r.Dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%s", "refs/rig/tasks/B"); got != "rig <rig@localhost>|rig <rig@localhost>|msg" {
		t.Fatalf("identity/message = %q", got)
	}
	if got := sh(t, r.Dir, "rev-list", "--parents", "-1", c2); !strings.HasSuffix(got, c1) {
		t.Fatalf("parent missing: %q", got)
	}

	big := bytes.Repeat([]byte("x"), 300000)
	bigOID, _ := r.WriteBlob(ctx, big)
	tree, _ := r.MakeTree(ctx, []TreeEntry{{"100644", "blob", bigOID, "big"}})
	c3, _ := r.CommitTree(ctx, tree, nil, "big")
	if err := r.UpdateRef(ctx, "refs/rig/tasks/C", c3, ""); err != nil {
		t.Fatal(err)
	}
	blobs, err := r.ReadBlobs(ctx, []string{"refs/rig/tasks/A:log.jsonl", "refs/rig/tasks/A:nope", "refs/rig/tasks/C:big", "refs/rig/tasks/B:log.jsonl"})
	if err != nil || len(blobs) != 4 {
		t.Fatalf("ReadBlobs = %v %v", blobs, err)
	}
	if string(blobs[0].Data) != "one\ntwo\n" || blobs[0].Spec != "refs/rig/tasks/A:log.jsonl" || blobs[0].Missing {
		t.Fatalf("blob 0 = %+v", blobs[0])
	}
	if !blobs[1].Missing || blobs[1].Data != nil {
		t.Fatalf("blob 1 = %+v", blobs[1])
	}
	if !bytes.Equal(blobs[2].Data, big) || string(blobs[3].Data) != "one\ntwo\n" {
		t.Fatal("blob 2 or 3 wrong")
	}
	if b, err := r.ReadBlobs(ctx, nil); err != nil || b != nil {
		t.Fatalf("no specs = %v %v", b, err)
	}
	if _, err := r.ReadBlobs(ctx, []string{"a\nb"}); err == nil {
		t.Fatal("newline spec accepted")
	}
}

// AC2: a stale or wrongly-absent old gives ErrRefMoved.
func TestUpdateRefMoved(t *testing.T) {
	r := plumbingRepo(t)
	_, _, c1 := commitData(t, r, "1")
	_, _, c2 := commitData(t, r, "2", c1)
	_, _, c3 := commitData(t, r, "3", c2)
	ref := "refs/rig/tasks/X"
	if err := r.UpdateRef(ctx, ref, c1, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, ref, c2, ""); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("present when expected absent: %v", err)
	}
	if err := r.UpdateRef(ctx, ref, c3, c2); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("stale old: %v", err)
	}
	if err := r.UpdateRef(ctx, "refs/rig/tasks/Y", c3, c2); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("absent when old given: %v", err)
	}
	if oid, _, _ := r.ResolveRef(ctx, ref); oid != c1 {
		t.Fatalf("ref changed to %s", oid)
	}
	err := r.UpdateRef(ctx, "refs/rig/bad..name", c1, "")
	if err == nil || errors.Is(err, ErrRefMoved) {
		t.Fatalf("invalid ref name: %v", err)
	}
}

// AC3: none of the calls touches the index, HEAD or the working tree.
func TestPlumbingLeavesWorktreeAlone(t *testing.T) {
	r := plumbingRepo(t)
	sh(t, r.Dir, "commit", "-q", "--allow-empty", "-m", "first")
	if err := os.WriteFile(filepath.Join(r.Dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, r.Dir, "add", "f.txt")
	snap := func() string {
		idx, _ := os.ReadFile(filepath.Join(r.Dir, ".git", "index"))
		head, _ := os.ReadFile(filepath.Join(r.Dir, ".git", "HEAD"))
		return sh(t, r.Dir, "status", "--porcelain=v1") + "|" + sh(t, r.Dir, "rev-parse", "HEAD") + "|" + string(head) + "|" + string(idx)
	}
	before := snap()
	_, _, c := commitData(t, r, "data")
	if err := r.UpdateRef(ctx, "refs/rig/tasks/Z", c, ""); err != nil {
		t.Fatal(err)
	}
	_, _ = r.ReadRefs(ctx, "refs/rig/")
	_, _ = r.ReadBlobs(ctx, []string{"refs/rig/tasks/Z:log.jsonl"})
	_, _, _ = r.ResolveRef(ctx, "refs/rig/tasks/Z")
	if snap() != before {
		t.Fatal("index, HEAD or working tree changed")
	}
}

// AC4: works in a sha256 repository.
func TestPlumbingSHA256(t *testing.T) {
	if out, err := exec.Command("git", "init", "-q", "--object-format=sha256", t.TempDir()).CombinedOutput(); err != nil {
		t.Skipf("git lacks sha256 support: %v %s", err, out)
	}
	r := plumbingRepo(t, "--object-format=sha256")
	blob, _, c := commitData(t, r, "sha256 data\n")
	if len(blob) != 64 || len(c) != 64 {
		t.Fatalf("expected 64-char ids, got %d and %d", len(blob), len(c))
	}
	if err := r.UpdateRef(ctx, "refs/rig/tasks/S", c, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, "refs/rig/tasks/S", c, ""); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("got %v", err)
	}
	b, err := r.ReadBlobs(ctx, []string{"refs/rig/tasks/S:log.jsonl"})
	if err != nil || string(b[0].Data) != "sha256 data\n" {
		t.Fatalf("ReadBlobs = %+v %v", b, err)
	}
	if refs, err := r.ReadRefs(ctx, "refs/rig/"); err != nil || len(refs) != 1 || refs[0].OID != c {
		t.Fatalf("ReadRefs = %+v %v", refs, err)
	}
}

// A user's commit.gpgSign setting must not sign or block store commits.
func TestCommitTreeIgnoresGPGSign(t *testing.T) {
	r := plumbingRepo(t)
	sh(t, r.Dir, "config", "commit.gpgsign", "true")
	sh(t, r.Dir, "config", "gpg.program", "/nonexistent/gpg")
	_, _, c := commitData(t, r, "x\n")
	out, err := exec.Command("git", "-C", r.Dir, "cat-file", "commit", c).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	if strings.Contains(string(out), "gpgsig") {
		t.Fatalf("commit is signed:\n%s", out)
	}
}
